package mcp

import (
	"bufio"
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"m-cpg-go/pkg/concept"
	"m-cpg-go/pkg/config"
	"m-cpg-go/pkg/db"
	"m-cpg-go/pkg/parser"
	"m-cpg-go/pkg/vector"
)

// JSON-RPC basic structures
type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   *rpcError   `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Tool structures
type mcpTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema struct {
		Type       string                 `json:"type"`
		Properties map[string]interface{} `json:"properties"`
		Required   []string               `json:"required"`
	} `json:"inputSchema"`
}

type mcpContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type mcpToolCallResult struct {
	Content []mcpContent `json:"content"`
}

// VerifyEmbeddingConfig checks if the active embedding configuration matches the DB's initialized configuration
func VerifyEmbeddingConfig(gdb *db.GraphDB, cfg *config.Config) error {
	storedProvider, err := gdb.GetConfigMeta("embedding_provider")
	if err != nil {
		return fmt.Errorf("failed to read embedding config from DB: %w", err)
	}

	if storedProvider == "" {
		// First-time DB initialization, store current config values
		return gdb.RunInTransaction(func(tx *sql.Tx) error {
			if err := gdb.SaveConfigMeta(tx, "embedding_provider", cfg.Embedding.Provider); err != nil {
				return err
			}
			if err := gdb.SaveConfigMeta(tx, "embedding_model", cfg.Embedding.Model); err != nil {
				return err
			}
			if err := gdb.SaveConfigMeta(tx, "embedding_dimension", fmt.Sprintf("%d", cfg.Embedding.Dimension)); err != nil {
				return err
			}
			return nil
		})
	}

	storedModel, _ := gdb.GetConfigMeta("embedding_model")
	storedDimStr, _ := gdb.GetConfigMeta("embedding_dimension")

	if storedProvider != cfg.Embedding.Provider || storedModel != cfg.Embedding.Model || storedDimStr != fmt.Sprintf("%d", cfg.Embedding.Dimension) {
		return fmt.Errorf("embedding model mismatch: database was initialized with %s (model: %s, dim: %s) but currently configured to %s (model: %s, dim: %d). Please clear/reset your database or restore configuration",
			storedProvider, storedModel, storedDimStr, cfg.Embedding.Provider, cfg.Embedding.Model, cfg.Embedding.Dimension)
	}

	return nil
}

func computeFileMD5(filePath string) (string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}
	hash := md5.Sum(data)
	return hex.EncodeToString(hash[:]), nil
}

func StartServer(gdb *db.GraphDB, vStore *vector.VectorStore, cfg *config.Config) error {
	fmt.Fprintln(os.Stderr, "[MCP] Starting m-cpg-go stdio server...")

	// Validate configuration
	if err := VerifyEmbeddingConfig(gdb, cfg); err != nil {
		return err
	}

	// Start Tiering GC background worker
	go func() {
		fmt.Fprintln(os.Stderr, "[MCP] Starting background Tiering GC worker...")
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			// Decay interval: 7 days = 7 * 24 * 60 * 60 = 604800 seconds
			if err := gdb.RunTieringGC(604800, time.Now().Unix()); err != nil {
				fmt.Fprintf(os.Stderr, "[MCP] Tiering GC Error: %v\n", err)
			}
		}
	}()

	reader := bufio.NewReader(os.Stdin)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				fmt.Fprintln(os.Stderr, "[MCP] Stdin reached EOF. Stopping server.")
				return nil
			}
			return fmt.Errorf("stdin read error: %w", err)
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var req jsonRPCRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			sendErrorResponse(nil, -32700, "Parse error: "+err.Error())
			continue
		}

		handleRequest(&req, gdb, vStore, cfg)
	}
}

func handleRequest(req *jsonRPCRequest, gdb *db.GraphDB, vStore *vector.VectorStore, cfg *config.Config) {
	switch req.Method {
	case "initialize":
		result := map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]interface{}{
				"tools": map[string]interface{}{},
			},
			"serverInfo": map[string]string{
				"name":    "m-cpg-go",
				"version": "0.1.0",
			},
		}
		sendSuccessResponse(req.ID, result)

	case "tools/list":
		tools := []mcpTool{
			{
				Name:        "m_cpg_index",
				Description: "Recursively indexes a directory (Python, Go, and Markdown files) into the local Code Graph & Vector database.",
				InputSchema: struct {
					Type       string                 `json:"type"`
					Properties map[string]interface{} `json:"properties"`
					Required   []string               `json:"required"`
				}{
					Type: "object",
					Properties: map[string]interface{}{
						"path": map[string]string{
							"type":        "string",
							"description": "Absolute path to the project directory to index.",
						},
						"project_id": map[string]string{
							"type":        "string",
							"description": "Unique identifier for this project.",
						},
					},
					Required: []string{"path", "project_id"},
				},
			},
			{
				Name:        "m_cpg_search",
				Description: "Performs a hybrid search (vector and graph relational queries) to retrieve relevant code snippets, docstrings, and relationships.",
				InputSchema: struct {
					Type       string                 `json:"type"`
					Properties map[string]interface{} `json:"properties"`
					Required   []string               `json:"required"`
				}{
					Type: "object",
					Properties: map[string]interface{}{
						"query": map[string]string{
							"type":        "string",
							"description": "Natural language query or code symbol (class/method name) to search for.",
						},
						"top_k": map[string]interface{}{
							"type":        "integer",
							"description": "Maximum number of code results to return.",
							"default":     5,
						},
					},
					Required: []string{"query"},
				},
			},
			{
				Name:        "m_cpg_find_duplicates",
				Description: "CRITICAL: You MUST use this tool BEFORE writing any new function, method, struct, or file to check if similar logic already exists. Checks if a proposed code snippet or functional description matches existing methods/files in the codebase to prevent semantic duplication.",
				InputSchema: struct {
					Type       string                 `json:"type"`
					Properties map[string]interface{} `json:"properties"`
					Required   []string               `json:"required"`
				}{
					Type: "object",
					Properties: map[string]interface{}{
						"code_snippet": map[string]string{
							"type":        "string",
							"description": "The function signature, code implementation, or design description planned to be written.",
						},
						"threshold": map[string]interface{}{
							"type":        "number",
							"description": "Cosine similarity threshold (default 0.70). Values above this are flagged as potential duplicates.",
							"default":     0.70,
						},
					},
					Required: []string{"code_snippet"},
				},
			},
			{
				Name:        "m_cpg_read_node",
				Description: "Retrieves the full code and docstring for a specific node given its Fully Qualified Name (FQN).",
				InputSchema: struct {
					Type       string                 `json:"type"`
					Properties map[string]interface{} `json:"properties"`
					Required   []string               `json:"required"`
				}{
					Type: "object",
					Properties: map[string]interface{}{
						"fqn": map[string]string{
							"type":        "string",
							"description": "The Fully Qualified Name (FQN) of the node to read (e.g., pkg.module.ClassName.MethodName).",
						},
					},
					Required: []string{"fqn"},
				},
			},
			{
				Name:        "m_cpg_get_file_structure",
				Description: "Retrieves the structural hierarchy (classes, methods, modules) of a specific file from the code graph database.",
				InputSchema: struct {
					Type       string                 `json:"type"`
					Properties map[string]interface{} `json:"properties"`
					Required   []string               `json:"required"`
				}{
					Type: "object",
					Properties: map[string]interface{}{
						"file_path": map[string]string{
							"type":        "string",
							"description": "Path to the file to inspect (relative to project path, e.g. pkg/db/db.go).",
						},
						"project_id": map[string]string{
							"type":        "string",
							"description": "Unique identifier for this project.",
						},
					},
					Required: []string{"file_path", "project_id"},
				},
			},
			{
				Name:        "m_cpg_remember",
				Description: "Saves a developer preference, compiler error fix, or session log to the persistent agent memory.",
				InputSchema: struct {
					Type       string                 `json:"type"`
					Properties map[string]interface{} `json:"properties"`
					Required   []string               `json:"required"`
				}{
					Type: "object",
					Properties: map[string]interface{}{
						"summary": map[string]string{
							"type":        "string",
							"description": "Short summary of the bug fix, command, or preference to remember.",
						},
						"details": map[string]string{
							"type":        "string",
							"description": "Detailed explanation, code snippet, or rule details.",
						},
						"event_type": map[string]string{
							"type":        "string",
							"description": "Type of memory: 'error_fix', 'preference', 'session_log'.",
						},
							"importance": map[string]interface{}{
								"type":        "integer",
								"description": "Optional importance score (0-10, default 0). High-importance memories will be injected into the Hot Context at session start.",
							},
					},
					Required: []string{"summary", "details", "event_type"},
				},
			},
			{
				Name:        "m_cpg_search_memory",
				Description: "Searches the agent's episodic and semantic memory for past bug fixes, preferences, and session context using vector similarity.",
				InputSchema: struct {
					Type       string                 `json:"type"`
					Properties map[string]interface{} `json:"properties"`
					Required   []string               `json:"required"`
				}{
					Type: "object",
					Properties: map[string]interface{}{
						"query": map[string]string{
							"type":        "string",
							"description": "The search query (e.g. 'how did we fix the sqlite locking issue?').",
						},
						"limit": map[string]interface{}{
							"type":        "integer",
							"description": "Maximum number of results to retrieve (default 5).",
						},
					},
					Required: []string{"query"},
				},
			},
			{
				Name:        "m_cpg_consolidate_memories",
				Description: "Consolidates fragmented agent memories (events) into a high-level insight, archiving the old events.",
				InputSchema: struct {
					Type       string                 `json:"type"`
					Properties map[string]interface{} `json:"properties"`
					Required   []string               `json:"required"`
				}{
					Type: "object",
					Properties: map[string]interface{}{
						"event_ids": map[string]interface{}{
							"type":        "array",
							"items":       map[string]string{"type": "string"},
							"description": "List of memory event IDs to archive.",
						},
						"insight": map[string]string{
							"type":        "string",
							"description": "The consolidated insight/pattern derived from the events.",
						},
					},
					Required: []string{"event_ids", "insight"},
				},
			},
			{
				Name:        "m_cpg_get_preferences",
				Description: "Retrieves the persistent developer preferences and recent error-fixing logs from the agent memory.",
				InputSchema: struct {
					Type       string                 `json:"type"`
					Properties map[string]interface{} `json:"properties"`
					Required   []string               `json:"required"`
				}{
					Type:       "object",
					Properties: map[string]interface{}{},
					Required:   []string{},
				},
			},
			{
				Name:        "m_cpg_ingest_conversation",
				Description: "Ingests a segment of conversation transcript or context into the Cold memory layer. This allows the system to passively retain conversation history for future context retrieval.",
				InputSchema: struct {
					Type       string                 `json:"type"`
					Properties map[string]interface{} `json:"properties"`
					Required   []string               `json:"required"`
				}{
					Type: "object",
					Properties: map[string]interface{}{
						"transcript": map[string]interface{}{
							"type":        "string",
							"description": "The raw transcript or context chunk of the conversation to ingest.",
						},
						"summary": map[string]interface{}{
							"type":        "string",
							"description": "A brief summary of what this conversation segment is about.",
						},
					},
					Required: []string{"transcript", "summary"},
				},
			},
			{
				Name:        "m_cpg_init_memory_bank",
				Description: "Initializes a standard Memory Bank directory in the project root to store project documentation and context.",
				InputSchema: struct {
					Type       string                 `json:"type"`
					Properties map[string]interface{} `json:"properties"`
					Required   []string               `json:"required"`
				}{
					Type: "object",
					Properties: map[string]interface{}{
						"path": map[string]string{
							"type":        "string",
							"description": "Absolute path to the project directory where the memory-bank should be created.",
						},
					},
					Required: []string{"path"},
				},
			},
			{
				Name:        "m_cpg_get_concept_hierarchy",
				Description: "Retrieves the highest-frequency abstract concepts extracted from memory, providing a high-level knowledge hierarchy.",
				InputSchema: struct {
					Type       string                 `json:"type"`
					Properties map[string]interface{} `json:"properties"`
					Required   []string               `json:"required"`
				}{
					Type: "object",
					Properties: map[string]interface{}{
						"limit": map[string]interface{}{
							"type":        "integer",
							"description": "Maximum number of top concepts to retrieve (default 20).",
						},
					},
					Required: []string{},
				},
			},
			{
				Name:        "m_cpg_kb_bootstrap",
				Description: "Initiates the Knowledge Base bootstrapper. It aggregates hot memory layers (recent events, active concepts) and prepares the session context dynamically. Call this tool ONCE at the very beginning of a new AI session to establish context.",
				InputSchema: struct {
					Type       string                 `json:"type"`
					Properties map[string]interface{} `json:"properties"`
					Required   []string               `json:"required"`
				}{
					Type:       "object",
					Properties: map[string]interface{}{},
					Required:   []string{},
				},
			},
			{
				Name:        "m_cpg_get_hot_context",
				Description: "Retrieves a dynamic, highly condensed markdown index representing Current Tasks, Unresolved Issues, and Active Entities (Hot Memory Layer) to be injected at session start.",
				InputSchema: struct {
					Type       string                 `json:"type"`
					Properties map[string]interface{} `json:"properties"`
					Required   []string               `json:"required"`
				}{
					Type:       "object",
					Properties: map[string]interface{}{},
					Required:   []string{},
				},
			},
			{
				Name:        "m_cpg_query_graph",
				Description: "Executes a structural multi-hop pattern query on the code graph (e.g., 'Method(A) -> CALLS -> Method(B)'). Useful for finding relationships like 'What does ClassX contain?' or 'What calls MethodY?'. Use '*' as a wildcard for names.",
				InputSchema: struct {
					Type       string                 `json:"type"`
					Properties map[string]interface{} `json:"properties"`
					Required   []string               `json:"required"`
				}{
					Type: "object",
					Properties: map[string]interface{}{
						"pattern": map[string]string{
							"type":        "string",
							"description": "The query pattern, e.g., 'Class(MyClass) -> CONTAINS -> Method(*)' or 'Method(*) -> CALLS -> Method(TargetFunc)'.",
						},
						"max_depth": map[string]interface{}{
							"type":        "integer",
							"description": "Maximum depth for recursive traversal (default 1).",
							"default":     1,
						},
					},
					Required: []string{"pattern"},
				},
			},
			{
				Name:        "m_cpg_get_node_dependencies",
				Description: "Retrieves the structural dependencies for a node (Callers and Callees) using its FQN. Useful for determining the impact of changing a method.",
				InputSchema: struct {
					Type       string                 `json:"type"`
					Properties map[string]interface{} `json:"properties"`
					Required   []string               `json:"required"`
				}{
					Type: "object",
					Properties: map[string]interface{}{
						"fqn": map[string]string{
							"type":        "string",
							"description": "The Fully Qualified Name (FQN) of the node (e.g., pkg.module.ClassName.MethodName).",
						},
					},
					Required: []string{"fqn"},
				},
			},
			{
				Name:        "m_cpg_find_path",
				Description: "Queries the graph for the shortest path between two nodes by their FQNs.",
				InputSchema: struct {
					Type       string                 `json:"type"`
					Properties map[string]interface{} `json:"properties"`
					Required   []string               `json:"required"`
				}{
					Type: "object",
					Properties: map[string]interface{}{
						"source_fqn": map[string]string{
							"type":        "string",
							"description": "The Fully Qualified Name (FQN) of the source node.",
						},
						"target_fqn": map[string]string{
							"type":        "string",
							"description": "The Fully Qualified Name (FQN) of the target node.",
						},
					},
					Required: []string{"source_fqn", "target_fqn"},
				},
			},
			{
				Name:        "m_cpg_generate_wiki",
				Description: "Generates visual memory_index.md and memory_log.md in the project workspace (memory-bank) so human and AI agents can visually browse the accumulated knowledge.",
				InputSchema: struct {
					Type       string                 `json:"type"`
					Properties map[string]interface{} `json:"properties"`
					Required   []string               `json:"required"`
				}{
					Type: "object",
					Properties: map[string]interface{}{
						"project_path": map[string]string{
							"type":        "string",
							"description": "Absolute path to the project directory where the memory-bank is located.",
						},
					},
					Required: []string{"project_path"},
				},
			},
			{
				Name:        "m_cpg_synthesize_memories",
				Description: "Acts as a knowledge curator to synthesize multiple fragmented events into a high-density 'Wiki' node, deprecating the old fragments.",
				InputSchema: struct {
					Type       string                 `json:"type"`
					Properties map[string]interface{} `json:"properties"`
					Required   []string               `json:"required"`
				}{
					Type: "object",
					Properties: map[string]interface{}{
						"event_ids": map[string]interface{}{
							"type":        "array",
							"items":       map[string]string{"type": "string"},
							"description": "List of old memory event IDs to archive/deprecate.",
						},
						"summary": map[string]string{
							"type":        "string",
							"description": "Short summary of the synthesized wiki node.",
						},
						"synthesis": map[string]string{
							"type":        "string",
							"description": "The rich, synthesized markdown content integrating all the knowledge.",
						},
					},
					Required: []string{"event_ids", "summary", "synthesis"},
				},
			},
			{
				Name:        "m_cpg_lint_memory",
				Description: "Scans the agent's memory for knowledge rot (e.g. stale events that haven't been accessed in a while). Returns events for the agent to review, update, or deprecate.",
				InputSchema: struct {
					Type       string                 `json:"type"`
					Properties map[string]interface{} `json:"properties"`
					Required   []string               `json:"required"`
				}{
					Type: "object",
					Properties: map[string]interface{}{
						"days_stale": map[string]interface{}{
							"type":        "integer",
							"description": "Number of days since last access to consider an event stale (default 30).",
							"default":     30,
						},
					},
					Required: []string{},
				},
			},
		}

		sendSuccessResponse(req.ID, map[string]interface{}{
			"tools": tools,
		})

	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			sendErrorResponse(req.ID, -32602, "Invalid params: "+err.Error())
			return
		}

		result, err := executeTool(params.Name, params.Arguments, gdb, vStore, cfg)
		if err != nil {
			sendErrorResponse(req.ID, -32603, "Internal tool error: "+err.Error())
			return
		}
		sendSuccessResponse(req.ID, result)

	default:
		// Unknown method, respond with standard JSON-RPC MethodNotFound error
		sendErrorResponse(req.ID, -32601, "Method not found: "+req.Method)
	}
}

func executeTool(name string, args json.RawMessage, gdb *db.GraphDB, vStore *vector.VectorStore, cfg *config.Config) (*mcpToolCallResult, error) {
	switch name {
	case "m_cpg_index":
		var params struct {
			Path      string `json:"path"`
			ProjectID string `json:"project_id"`
		}
		if err := json.Unmarshal(args, &params); err != nil {
			return nil, err
		}

		filesIndexed, nodesAdded, edgesAdded, err := RunIndexing(params.Path, params.ProjectID, gdb, vStore, cfg)
		if err != nil {
			return &mcpToolCallResult{
				Content: []mcpContent{{Type: "text", Text: fmt.Sprintf("Indexing failed: %v", err)}},
			}, nil
		}

		msg := fmt.Sprintf("Indexing completed successfully!\n- Files Scanned: %d\n- Code Graph Nodes Created: %d\n- Relationship Edges Created: %d", filesIndexed, nodesAdded, edgesAdded)
		return &mcpToolCallResult{
			Content: []mcpContent{{Type: "text", Text: msg}},
		}, nil

	case "m_cpg_search":
		var params struct {
			Query string `json:"query"`
			TopK  int    `json:"top_k"`
		}
		if err := json.Unmarshal(args, &params); err != nil {
			return nil, err
		}
		if params.TopK <= 0 {
			params.TopK = 5
		}

		contextText, err := RunSearch(params.Query, params.TopK, gdb, vStore, cfg)
		if err != nil {
			return &mcpToolCallResult{
				Content: []mcpContent{{Type: "text", Text: fmt.Sprintf("Search failed: %v", err)}},
			}, nil
		}

		return &mcpToolCallResult{
			Content: []mcpContent{{Type: "text", Text: contextText}},
		}, nil

	case "m_cpg_read_node":
		var params struct {
			Fqn string `json:"fqn"`
		}
		if err := json.Unmarshal(args, &params); err != nil {
			return nil, err
		}

		res, err := RunReadNode(params.Fqn, gdb)
		if err != nil {
			return &mcpToolCallResult{
				Content: []mcpContent{{Type: "text", Text: fmt.Sprintf("Failed to read node: %v", err)}},
			}, nil
		}
		return &mcpToolCallResult{
			Content: []mcpContent{{Type: "text", Text: res}},
		}, nil

	case "m_cpg_find_duplicates":
		var params struct {
			CodeSnippet string  `json:"code_snippet"`
			Threshold   float32 `json:"threshold"`
		}
		if err := json.Unmarshal(args, &params); err != nil {
			return nil, err
		}
		if params.Threshold <= 0 {
			params.Threshold = 0.70
		}

		res, err := RunFindDuplicates(params.CodeSnippet, params.Threshold, gdb, vStore, cfg)
		if err != nil {
			return &mcpToolCallResult{
				Content: []mcpContent{{Type: "text", Text: fmt.Sprintf("Failed to check duplicate: %v", err)}},
			}, nil
		}
		return &mcpToolCallResult{
			Content: []mcpContent{{Type: "text", Text: res}},
		}, nil

	case "m_cpg_get_file_structure":
		var params struct {
			FilePath  string `json:"file_path"`
			ProjectID string `json:"project_id"`
		}
		if err := json.Unmarshal(args, &params); err != nil {
			return nil, err
		}

		res, err := RunGetFileStructure(params.FilePath, params.ProjectID, gdb)
		if err != nil {
			return &mcpToolCallResult{
				Content: []mcpContent{{Type: "text", Text: fmt.Sprintf("Failed to get file structure: %v", err)}},
			}, nil
		}
		return &mcpToolCallResult{
			Content: []mcpContent{{Type: "text", Text: res}},
		}, nil

	case "m_cpg_remember":
		var params struct {
			Summary    string  `json:"summary"`
			Details    string  `json:"details"`
			EventType  string  `json:"event_type"`
			Importance *int    `json:"importance,omitempty"`
			ProjectID  *string `json:"project_id,omitempty"`
		}
		if err := json.Unmarshal(args, &params); err != nil {
			return nil, err
		}

		importance := 0
		if params.Importance != nil {
			importance = *params.Importance
		}

		projectID := cfg.ProjectID
		if params.ProjectID != nil && *params.ProjectID != "" {
			projectID = *params.ProjectID
		}

		err := RunRemember(params.Summary, params.Details, params.EventType, importance, projectID, gdb, cfg)
		if err != nil {
			return &mcpToolCallResult{
				Content: []mcpContent{{Type: "text", Text: fmt.Sprintf("Failed to save memory: %v", err)}},
			}, nil
		}
		return &mcpToolCallResult{
			Content: []mcpContent{{Type: "text", Text: "Memory saved successfully!"}},
		}, nil

	case "m_cpg_search_memory":
		var params struct {
			Query     string  `json:"query"`
			Limit     *int    `json:"limit,omitempty"`
			ProjectID *string `json:"project_id,omitempty"`
		}
		if err := json.Unmarshal(args, &params); err != nil {
			return nil, err
		}
		limit := 5
		if params.Limit != nil && *params.Limit > 0 {
			limit = *params.Limit
		}

		projectID := cfg.ProjectID
		if params.ProjectID != nil && *params.ProjectID != "" {
			projectID = *params.ProjectID
		}

		res, err := RunSearchMemory(params.Query, limit, projectID, gdb, cfg)
		if err != nil {
			return &mcpToolCallResult{
				Content: []mcpContent{{Type: "text", Text: fmt.Sprintf("Failed to search memory: %v", err)}},
			}, nil
		}
		return &mcpToolCallResult{
			Content: []mcpContent{{Type: "text", Text: res}},
		}, nil

	case "m_cpg_consolidate_memories":
		var params struct {
			EventIDs []string `json:"event_ids"`
			Insight  string   `json:"insight"`
		}
		if err := json.Unmarshal(args, &params); err != nil {
			return nil, err
		}

		res, err := RunConsolidateMemories(params.EventIDs, params.Insight, gdb)
		if err != nil {
			return &mcpToolCallResult{
				Content: []mcpContent{{Type: "text", Text: fmt.Sprintf("Failed to consolidate memory: %v", err)}},
			}, nil
		}
		return &mcpToolCallResult{
			Content: []mcpContent{{Type: "text", Text: res}},
		}, nil

	case "m_cpg_get_preferences":
		projectID := cfg.ProjectID
		res, err := RunGetPreferences(gdb, projectID)
		if err != nil {
			return &mcpToolCallResult{
				Content: []mcpContent{{Type: "text", Text: fmt.Sprintf("Failed to fetch preferences: %v", err)}},
			}, nil
		}
		return &mcpToolCallResult{
			Content: []mcpContent{{Type: "text", Text: res}},
		}, nil

	case "m_cpg_init_memory_bank":
		var params struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(args, &params); err != nil {
			return nil, err
		}

		res, err := RunInitMemoryBank(params.Path)
		if err != nil {
			return &mcpToolCallResult{
				Content: []mcpContent{{Type: "text", Text: fmt.Sprintf("Failed to initialize memory bank: %v", err)}},
			}, nil
		}
		return &mcpToolCallResult{
			Content: []mcpContent{{Type: "text", Text: res}},
		}, nil

	case "m_cpg_kb_bootstrap":
		// kb_bootstrap essentially aliases getting the hot context, but sets the semantic expectation
		// for agent initialization logic as defined in the Knowledge Base docs.
		projectID := cfg.ProjectID
		res, err := RunGetHotContext(gdb, projectID)
		if err != nil {
			return &mcpToolCallResult{
				Content: []mcpContent{{Type: "text", Text: fmt.Sprintf("Failed to bootstrap KB: %v", err)}},
			}, nil
		}

		header := "# 🧠 Knowledge Base Bootstrapped successfully\n\n"
		header += "The session context has been loaded from the Hot Memory Layer.\n\n"

		return &mcpToolCallResult{
			Content: []mcpContent{{Type: "text", Text: header + res}},
		}, nil

	case "m_cpg_ingest_conversation":
		var params struct {
			Transcript string  `json:"transcript"`
			Summary    string  `json:"summary"`
			ProjectID  *string `json:"project_id,omitempty"`
		}
		if err := json.Unmarshal(args, &params); err != nil {
			return nil, fmt.Errorf("invalid arguments: %v", err)
		}
		if params.Transcript == "" || params.Summary == "" {
			return nil, fmt.Errorf("transcript and summary are required")
		}

		projectID := cfg.ProjectID
		if params.ProjectID != nil && *params.ProjectID != "" {
			projectID = *params.ProjectID
		}

		res, err := RunIngestConversation(gdb, cfg, params.Transcript, params.Summary, projectID)
		if err != nil {
			return &mcpToolCallResult{
				Content: []mcpContent{{Type: "text", Text: fmt.Sprintf("Failed to ingest conversation: %v", err)}},
			}, nil
		}
		return &mcpToolCallResult{
			Content: []mcpContent{{Type: "text", Text: res}},
		}, nil

	case "m_cpg_get_hot_context":
		projectID := cfg.ProjectID
		res, err := RunGetHotContext(gdb, projectID)
		if err != nil {
			return &mcpToolCallResult{
				Content: []mcpContent{{Type: "text", Text: fmt.Sprintf("Failed to get hot context: %v", err)}},
			}, nil
		}
		return &mcpToolCallResult{
			Content: []mcpContent{{Type: "text", Text: res}},
		}, nil

	case "m_cpg_get_concept_hierarchy":
		var params struct {
			Limit *int `json:"limit,omitempty"`
		}
		if err := json.Unmarshal(args, &params); err != nil {
			return nil, err
		}
		limit := 20
		if params.Limit != nil && *params.Limit > 0 {
			limit = *params.Limit
		}

		res, err := RunGetConceptHierarchy(limit, gdb)
		if err != nil {
			return &mcpToolCallResult{
				Content: []mcpContent{{Type: "text", Text: fmt.Sprintf("Failed to get concept hierarchy: %v", err)}},
			}, nil
		}
		return &mcpToolCallResult{
			Content: []mcpContent{{Type: "text", Text: res}},
		}, nil

	case "m_cpg_query_graph":
		var params struct {
			Pattern  string `json:"pattern"`
			MaxDepth *int   `json:"max_depth,omitempty"`
		}
		if err := json.Unmarshal(args, &params); err != nil {
			return nil, err
		}
		maxDepth := 1
		if params.MaxDepth != nil && *params.MaxDepth > 0 {
			maxDepth = *params.MaxDepth
		}

		res, err := RunQueryGraph(params.Pattern, maxDepth, gdb)
		if err != nil {
			return &mcpToolCallResult{
				Content: []mcpContent{{Type: "text", Text: fmt.Sprintf("Failed to query graph: %v", err)}},
			}, nil
		}
		return &mcpToolCallResult{
			Content: []mcpContent{{Type: "text", Text: res}},
		}, nil

	case "m_cpg_get_node_dependencies":
		var params struct {
			Fqn string `json:"fqn"`
		}
		if err := json.Unmarshal(args, &params); err != nil {
			return nil, err
		}

		res, err := RunGetNodeDependencies(params.Fqn, gdb)
		if err != nil {
			return &mcpToolCallResult{
				Content: []mcpContent{{Type: "text", Text: fmt.Sprintf("Failed to get node dependencies: %v", err)}},
			}, nil
		}
		return &mcpToolCallResult{
			Content: []mcpContent{{Type: "text", Text: res}},
		}, nil

	case "m_cpg_find_path":
		var params struct {
			SourceFqn string `json:"source_fqn"`
			TargetFqn string `json:"target_fqn"`
		}
		if err := json.Unmarshal(args, &params); err != nil {
			return nil, err
		}
		res, err := RunFindPath(params.SourceFqn, params.TargetFqn, gdb)
		if err != nil {
			return &mcpToolCallResult{
				Content: []mcpContent{{Type: "text", Text: fmt.Sprintf("Failed to find path: %v", err)}},
			}, nil
		}
		return &mcpToolCallResult{
			Content: []mcpContent{{Type: "text", Text: res}},
		}, nil

	case "m_cpg_generate_wiki":
		var params struct {
			ProjectPath string `json:"project_path"`
		}
		if err := json.Unmarshal(args, &params); err != nil {
			return nil, err
		}
		res, err := RunGenerateWiki(params.ProjectPath, cfg.ProjectID, gdb)
		if err != nil {
			return &mcpToolCallResult{
				Content: []mcpContent{{Type: "text", Text: fmt.Sprintf("Failed to generate wiki: %v", err)}},
			}, nil
		}
		return &mcpToolCallResult{
			Content: []mcpContent{{Type: "text", Text: res}},
		}, nil

	case "m_cpg_synthesize_memories":
		var params struct {
			EventIDs  []string `json:"event_ids"`
			Summary   string   `json:"summary"`
			Synthesis string   `json:"synthesis"`
		}
		if err := json.Unmarshal(args, &params); err != nil {
			return nil, err
		}

		res, err := RunSynthesizeMemories(params.EventIDs, params.Summary, params.Synthesis, cfg.ProjectID, gdb, cfg)
		if err != nil {
			return &mcpToolCallResult{
				Content: []mcpContent{{Type: "text", Text: fmt.Sprintf("Failed to synthesize memories: %v", err)}},
			}, nil
		}
		return &mcpToolCallResult{
			Content: []mcpContent{{Type: "text", Text: res}},
		}, nil

	case "m_cpg_lint_memory":
		var params struct {
			DaysStale *int `json:"days_stale,omitempty"`
		}
		if err := json.Unmarshal(args, &params); err != nil {
			return nil, err
		}
		daysStale := 30
		if params.DaysStale != nil && *params.DaysStale > 0 {
			daysStale = *params.DaysStale
		}
		res, err := RunLintMemory(daysStale, cfg.ProjectID, gdb)
		if err != nil {
			return &mcpToolCallResult{
				Content: []mcpContent{{Type: "text", Text: fmt.Sprintf("Failed to lint memory: %v", err)}},
			}, nil
		}
		return &mcpToolCallResult{
			Content: []mcpContent{{Type: "text", Text: res}},
		}, nil

	default:
		return nil, fmt.Errorf("unknown tool name: %s", name)
	}
}

func RunIndexing(projectPath, projectID string, gdb *db.GraphDB, vStore *vector.VectorStore, cfg *config.Config) (int, int, int, error) {
	// Validate configuration
	if err := VerifyEmbeddingConfig(gdb, cfg); err != nil {
		return 0, 0, 0, err
	}

	var pyFiles, goFiles, mdFiles, javaFiles []string
	err := filepath.Walk(projectPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			name := info.Name()
			// Skip hidden dirs and build/dependency folders
			if (strings.HasPrefix(name, ".") && name != ".") ||
				name == "target" ||
				name == "node_modules" ||
				name == "build" ||
				name == "dist" ||
				name == "bin" ||
				name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		switch ext {
		case ".py":
			pyFiles = append(pyFiles, path)
		case ".go":
			goFiles = append(goFiles, path)
		case ".md":
			mdFiles = append(mdFiles, path)
		case ".java":
			javaFiles = append(javaFiles, path)
		}
		return nil
	})
	if err != nil {
		return 0, 0, 0, err
	}

	allFiles := append(append(append(pyFiles, goFiles...), mdFiles...), javaFiles...)
	
	// Get stored files
	storedFiles, err := gdb.GetProjectFiles(projectID)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("failed to retrieve stored file hashes: %w", err)
	}

	foundPaths := make(map[string]bool)
	var changedFiles []string
	changedHashes := make(map[string]string)

	for _, file := range allFiles {
		foundPaths[file] = true
		hash, err := computeFileMD5(file)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[MCP] Warning: Failed to compute hash for %s: %v\n", file, err)
			changedFiles = append(changedFiles, file)
			changedHashes[file] = ""
			continue
		}

		storedHash, exists := storedFiles[file]
		if !exists || storedHash != hash {
			changedFiles = append(changedFiles, file)
			changedHashes[file] = hash
		}
	}

	// Find deleted files
	var deletedFiles []string
	for file := range storedFiles {
		if !foundPaths[file] {
			deletedFiles = append(deletedFiles, file)
		}
	}

	if len(changedFiles) == 0 && len(deletedFiles) == 0 {
		fmt.Fprintln(os.Stderr, "[MCP] No changed or deleted files. Index is up to date.")
		return len(allFiles), 0, 0, nil
	}

	fmt.Fprintf(os.Stderr, "[MCP] Indexing project '%s': %d changed/new files, %d deleted files.\n", projectID, len(changedFiles), len(deletedFiles))

	// Clean up database entries for changed and deleted files
	err = gdb.RunInTransaction(func(tx *sql.Tx) error {
		for _, file := range changedFiles {
			if err := gdb.DeleteNodesByFile(tx, file, projectID); err != nil {
				return err
			}
			if err := gdb.DeleteFileHash(tx, file, projectID); err != nil {
				return err
			}
		}
		for _, file := range deletedFiles {
			if err := gdb.DeleteNodesByFile(tx, file, projectID); err != nil {
				return err
			}
			if err := gdb.DeleteFileHash(tx, file, projectID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, 0, 0, fmt.Errorf("failed to prune stale index records: %w", err)
	}

	if len(changedFiles) == 0 {
		// Only deletions occurred
		return len(allFiles), 0, 0, nil
	}

	// 1. Parse changed files in CPU memory
	var parsedEntities []parser.CodeEntity
	var parsedRelations []parser.CodeRelation
	for _, file := range changedFiles {
		entities, relations, err := parser.ParseFile(file, projectID, projectPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[MCP] Warning: Failed to parse %s: %v\n", file, err)
			continue
		}
		parsedEntities = append(parsedEntities, entities...)
		parsedRelations = append(parsedRelations, relations...)
	}

	if len(parsedEntities) == 0 {
		// Update hashes even if files parsed to empty
		now := time.Now().Unix()
		err = gdb.RunInTransaction(func(tx *sql.Tx) error {
			for _, file := range changedFiles {
				if hash, ok := changedHashes[file]; ok && hash != "" {
					if err := gdb.SaveFileHash(tx, file, projectID, hash, now); err != nil {
						return err
					}
				}
			}
			return nil
		})
		return len(allFiles), 0, 0, err
	}

	// 2. Parallel embedding calculation using goroutines
	numWorkers := 8
	if numWorkers > len(parsedEntities) {
		numWorkers = len(parsedEntities)
	}

	type embedTask struct {
		entityIndex int
		text        string
	}
	type embedResult struct {
		entityIndex int
		embedding   []float32
		err         error
	}

	tasksChan := make(chan embedTask, len(parsedEntities))
	resultsChan := make(chan embedResult, len(parsedEntities))

	var wg sync.WaitGroup
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for task := range tasksChan {
				emb, err := vector.GetEmbedding(
					task.text,
					cfg.Embedding.Provider,
					cfg.Embedding.Model,
					cfg.Embedding.Endpoint,
					cfg.Embedding.APIKey,
					cfg.Embedding.Dimension,
				)
				resultsChan <- embedResult{
					entityIndex: task.entityIndex,
					embedding:   emb,
					err:         err,
				}
			}
		}()
	}

	for i, ent := range parsedEntities {
		if ent.Type == "Method" {
			continue // Skip calculating embeddings for methods to prevent database bloat
		}
		embedText := ent.Docstring
		if embedText == "" {
			if len(ent.Code) > 1000 {
				embedText = ent.Code[:1000]
			} else {
				embedText = ent.Code
			}
		}
		if embedText == "" {
			embedText = ent.Name
		}
		tasksChan <- embedTask{entityIndex: i, text: embedText}
	}
	close(tasksChan)

	go func() {
		wg.Wait()
		close(resultsChan)
	}()

	embeddings := make([][]float32, len(parsedEntities))
	for res := range resultsChan {
		if res.err != nil {
			// On error, generate pseudo-embedding deterministically
			ent := parsedEntities[res.entityIndex]
			embedText := ent.Docstring
			if embedText == "" {
				if len(ent.Code) > 1000 {
					embedText = ent.Code[:1000]
				} else {
					embedText = ent.Code
				}
			}
			if embedText == "" {
				embedText = ent.Name
			}
			res.embedding = vector.GeneratePseudoEmbedding(embedText, cfg.Embedding.Dimension)
		}
		embeddings[res.entityIndex] = res.embedding
	}

	nodesCount := 0
	edgesCount := 0

	// 3. Write all entries to database in batch transactions
	batchSize := 1000

	// Insert nodes & vectors in batches
	for batchStart := 0; batchStart < len(parsedEntities); batchStart += batchSize {
		batchEnd := batchStart + batchSize
		if batchEnd > len(parsedEntities) {
			batchEnd = len(parsedEntities)
		}
		batchEntities := parsedEntities[batchStart:batchEnd]

		err = gdb.RunInTransaction(func(tx *sql.Tx) error {
			for i, ent := range batchEntities {
				globalIndex := batchStart + i
				err = gdb.AddNode(tx, ent.ID, ent.Type, ent.Name, ent.FQN, ent.Code, ent.Docstring, projectID, ent.FilePath, nil)
				if err != nil {
					return fmt.Errorf("failed to save node: %w", err)
				}
				nodesCount++

				emb := embeddings[globalIndex]
				if len(emb) > 0 {
					embBytes := vector.Float32SliceToBytes(emb)
					err = gdb.SaveVector(tx, ent.ID, embBytes, map[string]any{"type": ent.Type, "fqn": ent.FQN, "name": ent.Name})
					if err != nil {
						return fmt.Errorf("failed to save vector: %w", err)
					}
				}
			}
			return nil
		})
		if err != nil {
			return 0, 0, 0, err
		}
	}

	// Keep track of newly inserted node IDs to bypass placeholder generation in edges insertion
	insertedNodeIDs := make(map[string]bool)
	for _, ent := range parsedEntities {
		insertedNodeIDs[ent.ID] = true
	}

	// Insert edges in batches
	for batchStart := 0; batchStart < len(parsedRelations); batchStart += batchSize {
		batchEnd := batchStart + batchSize
		if batchEnd > len(parsedRelations) {
			batchEnd = len(parsedRelations)
		}
		batchRelations := parsedRelations[batchStart:batchEnd]

		err = gdb.RunInTransaction(func(tx *sql.Tx) error {
			for _, rel := range batchRelations {
				var err error
				if insertedNodeIDs[rel.Source] && insertedNodeIDs[rel.Target] {
					// Fast path: both nodes exist, execute direct insert
					propsJSON := "{}"
					if len(rel.Properties) > 0 {
						data, _ := json.Marshal(rel.Properties)
						propsJSON = string(data)
					}
					query := `INSERT OR REPLACE INTO edges (source, target, label, properties) VALUES (?, ?, ?, ?);`
					_, err = tx.Exec(query, rel.Source, rel.Target, rel.Label, propsJSON)
				} else {
					// Fallback path: one of the nodes might be external
					err = gdb.AddEdge(tx, rel.Source, rel.Target, rel.Label, rel.Properties)
				}
				if err != nil {
					return fmt.Errorf("failed to save edge: %w", err)
				}
				edgesCount++
			}
			return nil
		})
		if err != nil {
			return 0, 0, 0, err
		}
	}

	// 4. Update file hashes
	now := time.Now().Unix()
	err = gdb.RunInTransaction(func(tx *sql.Tx) error {
		for _, file := range changedFiles {
			if hash, ok := changedHashes[file]; ok && hash != "" {
				if err := gdb.SaveFileHash(tx, file, projectID, hash, now); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return 0, 0, 0, fmt.Errorf("failed to save new file hashes: %w", err)
	}

	return len(allFiles), nodesCount, edgesCount, nil
}

func RunSearch(query string, topK int, gdb *db.GraphDB, vStore *vector.VectorStore, cfg *config.Config) (string, error) {
	if err := VerifyEmbeddingConfig(gdb, cfg); err != nil {
		return "", err
	}
	// 1. Get embedding for the query
	emb, err := vector.GetEmbedding(
		query,
		cfg.Embedding.Provider,
		cfg.Embedding.Model,
		cfg.Embedding.Endpoint,
		cfg.Embedding.APIKey,
		cfg.Embedding.Dimension,
	)
	if err != nil {
		return "", fmt.Errorf("failed to embed query: %w", err)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Hybrid Search Results for query: '%s'\n", query))
	sb.WriteString("==================================================\n\n")

	// 2. Query VectorStore for matching IDs (Code Nodes) using expanded limits for RRF
	results := vStore.Search(emb, topK*3)

	// RRF Maps
	vectorRanks := make(map[string]int)
	graphScores := make(map[string]float32)

	for i, res := range results {
		vectorRanks[res.ID] = i + 1
		neighbors, _ := gdb.GetNeighbors(res.ID)
		for _, n := range neighbors {
			nID := n["id"].(string)
			relation, _ := n["relation"].(string)
			weight := float32(1.0)
			if relation == "CALLS" {
				weight = 2.0 // Weigh CALLS edges more heavily than CONTAINS or others
			}
			graphScores[nID] += res.Score * weight
		}
	}

	// Sort graph neighbors by accumulated score to determine graph ranks
	type graphNodeScore struct {
		ID    string
		Score float32
	}
	var gnsList []graphNodeScore
	for id, score := range graphScores {
		gnsList = append(gnsList, graphNodeScore{id, score})
	}
	sort.Slice(gnsList, func(i, j int) bool {
		return gnsList[i].Score > gnsList[j].Score
	})

	graphRanks := make(map[string]int)
	for i, gns := range gnsList {
		graphRanks[gns.ID] = i + 1
	}

	// Calculate RRF for all unique nodes
	rrfScores := make(map[string]float64)
	allNodes := make(map[string]bool)
	for id := range vectorRanks {
		allNodes[id] = true
	}
	for id := range graphRanks {
		allNodes[id] = true
	}

	for id := range allNodes {
		var score float64
		if rank, ok := vectorRanks[id]; ok {
			score += 1.0 / (60.0 + float64(rank))
		}
		if rank, ok := graphRanks[id]; ok {
			score += 1.0 / (60.0 + float64(rank))
		}
		rrfScores[id] = score
	}

	// Sort final RRF scores
	type rrfNodeScore struct {
		ID    string
		Score float64
	}
	var rrfList []rrfNodeScore
	for id, score := range rrfScores {
		rrfList = append(rrfList, rrfNodeScore{id, score})
	}
	sort.Slice(rrfList, func(i, j int) bool {
		return rrfList[i].Score > rrfList[j].Score
	})

	if topK > len(rrfList) {
		topK = len(rrfList)
	}
	finalResults := rrfList[:topK]

	if len(finalResults) > 0 {
		sb.WriteString("## Code Graph Memories (Hybrid RRF Ranked):\n")
		for i, res := range finalResults {
			node, err := gdb.GetNode(res.ID)
			if err != nil || node == nil {
				continue
			}

			nodeType, _ := node["type"].(string)
			fqn, _ := node["fqn"].(string)
			code, _ := node["code"].(string)
			docstring, _ := node["docstring"].(string)

			sb.WriteString(fmt.Sprintf("%d. [%s] %s (RRF Score: %.4f)\n", i+1, nodeType, fqn, res.Score))
			if docstring != "" {
				sb.WriteString(fmt.Sprintf("   Docstring: %s\n", strings.ReplaceAll(docstring, "\n", "\n   ")))
			}

			// Fetch neighboring classes/methods/modules for context
			neighbors, _ := gdb.GetNeighbors(res.ID)
			if len(neighbors) > 0 {
				var nList []string
				for _, n := range neighbors {
					nameStr, _ := n["name"].(string)
					typeStr, _ := n["type"].(string)
					nList = append(nList, fmt.Sprintf("%s (%s)", nameStr, typeStr))
				}
				sb.WriteString(fmt.Sprintf("   Neighbors: %s\n", strings.Join(nList, ", ")))
			}

			if code != "" {
				sb.WriteString(fmt.Sprintf("   [Code omitted for progressive disclosure. Use m_cpg_read_node with fqn '%s' to read full code]\n", fqn))
			}
			sb.WriteString("--------------------------------------------------\n")
		}
		sb.WriteString("\n")
	}

	// 3. Search Memory Events (The kb_search aspect)
	// We use the topK for memory events as well
	memResult, _ := RunSearchMemory(query, topK, cfg.ProjectID, gdb, cfg)
	if memResult != "No active memories found matching the query." {
		sb.WriteString("## Episodic / Semantic Memories:\n")
		// Strip the "Found X memories matching 'query':\n\n" header from RunSearchMemory output
		lines := strings.Split(memResult, "\n")
		if len(lines) > 2 {
			sb.WriteString(strings.Join(lines[2:], "\n"))
		}
	}

	if sb.Len() <= len(fmt.Sprintf("Hybrid Search Results for query: '%s'\n==================================================\n\n", query)) {
		return fmt.Sprintf("No relevant code or event memory found for query: '%s'", query), nil
	}

	return sb.String(), nil
}

// RunLintMemory retrieves events that haven't been accessed for the specified number of days and flags them for AI review.
func RunLintMemory(daysStale int, projectID string, gdb *db.GraphDB) (string, error) {
	staleThreshold := time.Now().AddDate(0, 0, -daysStale).Unix()

	events, err := gdb.GetStaleEvents(projectID, staleThreshold)
	if err != nil {
		return "", fmt.Errorf("failed to fetch stale events: %w", err)
	}

	if len(events) == 0 {
		return fmt.Sprintf("Good news! No stale memories found older than %d days.", daysStale), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Memory Lint Report: Found %d stale events not accessed in over %d days.\n", len(events), daysStale))
	sb.WriteString("========================================================================\n\n")

	for i, ev := range events {
		id := ev["id"].(string)
		eventType := ev["event_type"].(string)
		summary := ev["summary"].(string)
		details := ev["details"].(string)
		lastAccessed := time.Unix(ev["last_accessed"].(int64), 0).Format("2006-01-02")

		sb.WriteString(fmt.Sprintf("%d. [ID: %s] Type: %s (Last Accessed: %s)\n", i+1, id, eventType, lastAccessed))
		sb.WriteString(fmt.Sprintf("   Summary: %s\n", summary))
		if len(details) > 150 {
			sb.WriteString(fmt.Sprintf("   Details: %s...\n", strings.ReplaceAll(details[:150], "\n", " ")))
		} else {
			sb.WriteString(fmt.Sprintf("   Details: %s\n", strings.ReplaceAll(details, "\n", " ")))
		}
		sb.WriteString("------------------------------------------------------------------------\n")
	}

	sb.WriteString("\nAction Required: Please review these events. If they are outdated (knowledge rot), use `m_cpg_consolidate_memories` or `m_cpg_synthesize_memories` to deprecate/archive them, or manually write a new memory to supersede them.")

	return sb.String(), nil
}

// RunConsolidateMemories archives the provided event IDs and returns a message to the agent to write the insight.
func RunConsolidateMemories(eventIDs []string, insight string, gdb *db.GraphDB) (string, error) {
	if len(eventIDs) == 0 {
		return "No event IDs provided for consolidation.", nil
	}

	if err := gdb.ArchiveEvents(nil, eventIDs); err != nil {
		return "", fmt.Errorf("failed to archive events: %w", err)
	}

	// Extract and save concepts from the insight
	concepts := concept.ExtractConcepts(insight)
	if err := gdb.SaveConcepts(nil, "", concepts); err != nil {
		fmt.Fprintf(os.Stderr, "[MCP] Warning: Failed to save concepts for insight: %v\n", err)
	}

	var sb strings.Builder
	sb.WriteString("Successfully archived the following fragmented events:\n")
	for _, id := range eventIDs {
		sb.WriteString(fmt.Sprintf("- %s\n", id))
	}
	sb.WriteString("\n")
	sb.WriteString("Consolidated Insight:\n")
	sb.WriteString(insight)
	sb.WriteString("\n\n")
	sb.WriteString("Next steps for Agent: Please append this consolidated insight into 'memory-bank/systemPatterns.md' or 'memory-bank/activeContext.md' using standard file editing tools to ensure it is kept in the working memory.")

	return sb.String(), nil
}



func sendSuccessResponse(id interface{}, result interface{}) {
	resp := jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
	data, _ := json.Marshal(resp)
	fmt.Println(string(data))
}

func sendErrorResponse(id interface{}, code int, message string) {
	resp := jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: &rpcError{
			Code:    code,
			Message: message,
		},
	}
	data, _ := json.Marshal(resp)
	fmt.Println(string(data))
}

// RunGetFileStructure scans the DB for structural nodes (Classes/Methods) inside a specific file path.
func RunGetFileStructure(filePath, projectID string, gdb *db.GraphDB) (string, error) {
	// Standardize separators to "/" and strip extension to match parser FQN logic
	normalized := filepath.ToSlash(filePath)
	ext := filepath.Ext(normalized)
	normalized = strings.TrimSuffix(normalized, ext)
	fqnPattern := strings.ReplaceAll(normalized, "/", ".")

	// If the file is __init__.py, FQN is just the package name (parent dir)
	if strings.HasSuffix(fqnPattern, ".__init__") {
		fqnPattern = strings.TrimSuffix(fqnPattern, ".__init__")
	}

	nodes, err := gdb.QueryNodes("", "", projectID)
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("File Structure for '%s' (derived FQN pattern: '%s'):\n", filePath, fqnPattern))
	sb.WriteString("==================================================\n")

	foundCount := 0
	for _, node := range nodes {
		nodeFQN := node["fqn"].(string)
		nodeType := node["type"].(string)
		docstring := node["docstring"].(string)

		// Match exactly or check if it starts with "fqnPattern." (member classes/methods)
		if nodeFQN == fqnPattern || strings.HasPrefix(nodeFQN, fqnPattern+".") {
			foundCount++
			sb.WriteString(fmt.Sprintf("- [%s] %s\n", nodeType, nodeFQN))
			if docstring != "" {
				// Trim to single line for structure review
				firstLine := strings.Split(docstring, "\n")[0]
				if len(firstLine) > 60 {
					firstLine = firstLine[:60] + "..."
				}
				sb.WriteString(fmt.Sprintf("  Docstring: %s\n", firstLine))
			}
		}
	}

	if foundCount == 0 {
		return fmt.Sprintf("No structural nodes found for file: %s", filePath), nil
	}

	return sb.String(), nil
}

// RunSearchMemory retrieves active events related to the query using vector similarity
// RunSearchMemory retrieves active events related to the query using vector similarity
func RunSearchMemory(query string, limit int, projectID string, gdb *db.GraphDB, cfg *config.Config) (string, error) {
	if err := VerifyEmbeddingConfig(gdb, cfg); err != nil {
		return "", err
	}
	emb, err := vector.GetEmbedding(
		query,
		cfg.Embedding.Provider,
		cfg.Embedding.Model,
		cfg.Embedding.Endpoint,
		cfg.Embedding.APIKey,
		cfg.Embedding.Dimension,
	)
	if err != nil {
		return "", fmt.Errorf("failed to embed query: %w", err)
	}

	events, err := gdb.GetAllActiveEvents(projectID)
	if err != nil {
		return "", fmt.Errorf("failed to retrieve active events: %w", err)
	}

	type ScoredEvent struct {
		Event map[string]any
		Score float32
	}
	var scoredEvents []ScoredEvent

	for _, ev := range events {
		embBytes, ok := ev["embedding"].([]byte)
		if !ok {
			continue
		}
		evEmb := vector.BytesToFloat32Slice(embBytes)
		score := vector.CosineSimilarity(emb, evEmb)
		scoredEvents = append(scoredEvents, ScoredEvent{Event: ev, Score: score})
	}

	// Sort descending by score
	sort.Slice(scoredEvents, func(i, j int) bool {
		return scoredEvents[i].Score > scoredEvents[j].Score
	})

	if limit > len(scoredEvents) {
		limit = len(scoredEvents)
	}
	topEvents := scoredEvents[:limit]

	if len(topEvents) == 0 {
		return "No active memories found matching the query.", nil
	}

	// Update last_accessed for the retrieved events
	var topEventIDs []string
	for _, se := range topEvents {
		if id, ok := se.Event["id"].(string); ok {
			topEventIDs = append(topEventIDs, id)
		}
	}
	if len(topEventIDs) > 0 {
		if err := gdb.UpdateEventAccess(nil, topEventIDs, time.Now().Unix()); err != nil {
			fmt.Fprintf(os.Stderr, "[MCP] Warning: Failed to update event access times: %v\n", err)
		}
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d memories matching '%s':\n\n", len(topEvents), query))
	for i, se := range topEvents {
		sb.WriteString(fmt.Sprintf("%d. [Score: %.3f] (ID: %s, Type: %s)\n", i+1, se.Score, se.Event["id"], se.Event["event_type"]))
		sb.WriteString(fmt.Sprintf("   Summary: %s\n", se.Event["summary"]))
		if details, ok := se.Event["details"].(string); ok && details != "" {
			// truncate details if too long
			if len(details) > 300 {
				details = details[:300] + "..."
			}
			sb.WriteString(fmt.Sprintf("   Details: %s\n", strings.ReplaceAll(details, "\n", "\n   ")))
		}
		sb.WriteString("\n")
	}

	return sb.String(), nil
}

// RunRemember saves a developer preference, compilation fix, or session log to the DB and generates vector embedding.
func RunRemember(summary, details, eventType string, importance int, projectID string, gdb *db.GraphDB, cfg *config.Config) error {
	if err := VerifyEmbeddingConfig(gdb, cfg); err != nil {
		return err
	}
	id := uuid.New().String()
	timestamp := time.Now().Unix()

	// Embed summary and details combined
	embedText := fmt.Sprintf("[%s] %s\n%s", eventType, summary, details)
	emb, err := vector.GetEmbedding(
		embedText,
		cfg.Embedding.Provider,
		cfg.Embedding.Model,
		cfg.Embedding.Endpoint,
		cfg.Embedding.APIKey,
		cfg.Embedding.Dimension,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[MCP] Warning: Failed to embed memory: %v. Falling back to pseudo-embedding.\n", err)
		emb = vector.GeneratePseudoEmbedding(embedText, cfg.Embedding.Dimension)
	}

	embBytes := vector.Float32SliceToBytes(emb)
	err = gdb.SaveEvent(nil, id, eventType, summary, details, timestamp, embBytes, "active", importance, projectID)
	if err != nil {
		return fmt.Errorf("failed to save memory to DB: %w", err)
	}

	// Extract and save abstract concepts
	concepts := concept.ExtractConcepts(summary + " " + details)
	if err := gdb.SaveConcepts(nil, id, concepts); err != nil {
		fmt.Fprintf(os.Stderr, "[MCP] Warning: Failed to save concepts for memory %s: %v\n", id, err)
	}

	fmt.Fprintf(os.Stderr, "[MCP] Successfully remembered event: %s\n", summary)
	return nil
}

// RunGetConceptHierarchy retrieves top frequency concepts from the database
func RunGetConceptHierarchy(limit int, gdb *db.GraphDB) (string, error) {
	concepts, err := gdb.GetTopConcepts(limit)
	if err != nil {
		return "", err
	}

	if len(concepts) == 0 {
		return "No extracted concepts found in the knowledge hierarchy.", nil
	}

	var sb strings.Builder
	sb.WriteString("High-Level Knowledge Hierarchy (Top Abstract Concepts):\n")
	sb.WriteString("=========================================================\n")

	for i, c := range concepts {
		name := c["name"].(string)
		frequency := c["frequency"].(int)
		sb.WriteString(fmt.Sprintf("%d. %s (Frequency: %d)\n", i+1, name, frequency))
	}

	return sb.String(), nil
}

// RunIngestConversation ingests a transcript chunk as an event (Cold Layer proxy)
func RunIngestConversation(gdb *db.GraphDB, cfg *config.Config, transcript, summary string, projectID string) (string, error) {
	if err := VerifyEmbeddingConfig(gdb, cfg); err != nil {
		return "", err
	}
	err := gdb.RunInTransaction(func(tx *sql.Tx) error {
		id := "conv_" + uuid.New().String()
		now := time.Now().Unix()
		details := fmt.Sprintf("Conversation Transcript:\n%s", transcript)

		// Create vector embedding for the transcript summary
		embText := summary + "\n" + transcript
		emb, err := vector.GetEmbedding(embText, cfg.Embedding.Provider, cfg.Embedding.Model, cfg.Embedding.Endpoint, cfg.Embedding.APIKey, cfg.Embedding.Dimension)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[MCP] Warning: Failed to generate embedding for conversation: %v. Falling back to pseudo-embedding.\n", err)
			emb = vector.GeneratePseudoEmbedding(embText, cfg.Embedding.Dimension)
		}
		var embBytes []byte
		if emb != nil {
			embBytes = vector.Float32SliceToBytes(emb)
		}

		// Save as an event with type 'conversation', importance 2 (default cold layer)
		err = gdb.SaveEvent(tx, id, "conversation", summary, details, now, embBytes, "archived", 2, projectID)
		if err != nil {
			return err
		}
		return nil
	})

	if err != nil {
		return "", err
	}

	return "Conversation successfully ingested into the Cold memory layer.", nil
}

// RunGetHotContext queries the most recent 5 active events and top 5 concepts to generate a dynamic hot context index
func RunGetHotContext(gdb *db.GraphDB, projectID string) (string, error) {
	events, err := gdb.GetRecentActiveEvents(5, projectID)
	if err != nil {
		return "", err
	}

	concepts, err := gdb.GetTopConcepts(5)
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	sb.WriteString("# AI Agent Hot Context (Dynamic Index)\n\n")

	sb.WriteString("## Active Entities / Concepts\n")
	if len(concepts) > 0 {
		for _, c := range concepts {
			name := c["name"].(string)
			frequency := c["frequency"].(int)
			sb.WriteString(fmt.Sprintf("- **%s** (Freq: %d)\n", name, frequency))
		}
	} else {
		sb.WriteString("No active concepts found.\n")
	}
	sb.WriteString("\n")

	sb.WriteString("## Current Tasks / Unresolved Issues\n")
	if len(events) > 0 {
		for i, ev := range events {
			t := time.Unix(ev["timestamp"].(int64), 0).Format("2006-01-02 15:04:05")

			// Render fire emoji for high importance events
			importanceLabel := ""
			if imp, ok := ev["importance"].(int); ok && imp >= 5 {
				importanceLabel = " 🔥"
			} else if imp, ok := ev["importance"].(int64); ok && imp >= 5 {
				importanceLabel = " 🔥"
			}

			sb.WriteString(fmt.Sprintf("%d. **[%s]** %s (%s)%s\n", i+1, ev["event_type"], ev["summary"], t, importanceLabel))
			if details := ev["details"].(string); details != "" {
				lines := strings.Split(details, "\n")
				for _, line := range lines {
					sb.WriteString(fmt.Sprintf("   > %s\n", line))
				}
			}
		}
	} else {
		sb.WriteString("No unresolved tasks found.\n")
	}

	return sb.String(), nil
}

// RunGetPreferences queries the most recent 10 events from memory
func RunGetPreferences(gdb *db.GraphDB, projectID string) (string, error) {
	events, err := gdb.GetRecentEvents(10, projectID)
	if err != nil {
		return "", err
	}

	if len(events) == 0 {
		return "No persistent preferences or debug event memories found in agent database.", nil
	}

	var sb strings.Builder
	sb.WriteString("Persistent Developer Preferences & Recent Fixes:\n")
	sb.WriteString("==================================================\n")

	for i, ev := range events {
		t := time.Unix(ev["timestamp"].(int64), 0).Format("2006-01-02 15:04:05")
		sb.WriteString(fmt.Sprintf("%d. [%s] %s (%s)\n", i+1, ev["event_type"], ev["summary"], t))
		if details := ev["details"].(string); details != "" {
			// Print details indented
			lines := strings.Split(details, "\n")
			for _, line := range lines {
				sb.WriteString(fmt.Sprintf("   %s\n", line))
			}
		}
		sb.WriteString("--------------------------------------------------\n")
	}

	return sb.String(), nil
}

// RunFindDuplicates compares proposed code snippet against existing database nodes to flag semantic duplication.
func RunFindDuplicates(codeSnippet string, threshold float32, gdb *db.GraphDB, vStore *vector.VectorStore, cfg *config.Config) (string, error) {
	// 1. Try to extract a function/method/class name from the snippet for a strict name check
	var exactMatches []string
	nameRegexes := []*regexp.Regexp{
		regexp.MustCompile(`(?:def|func)\s+([a-zA-Z0-9_]+)`),
		regexp.MustCompile(`class\s+([a-zA-Z0-9_]+)`),
		regexp.MustCompile(`type\s+([a-zA-Z0-9_]+)\s+(?:struct|interface)`),
	}

	extractedName := ""
	for _, re := range nameRegexes {
		if matches := re.FindStringSubmatch(codeSnippet); len(matches) > 1 {
			extractedName = matches[1]
			break
		}
	}

	if extractedName != "" {
		// Query SQLite for exact name match in classes/methods
		nodes, err := gdb.QueryNodes("", extractedName, cfg.ProjectID)
		if err == nil {
			for _, node := range nodes {
				name, okName := node["name"].(string)
				nodeType, okType := node["type"].(string)
				fqn, okFqn := node["fqn"].(string)

				if okName && okType && okFqn {
					if strings.EqualFold(name, extractedName) {
						exactMatches = append(exactMatches, fmt.Sprintf("- [Exact Name Match] [%s] %s (Matches proposed name: '%s')\n", nodeType, fqn, extractedName))
					}
				}
			}
		}
	}

	// 2. Get embedding for the proposed code snippet
	emb, err := vector.GetEmbedding(
		codeSnippet,
		cfg.Embedding.Provider,
		cfg.Embedding.Model,
		cfg.Embedding.Endpoint,
		cfg.Embedding.APIKey,
		cfg.Embedding.Dimension,
	)
	if err != nil {
		return "", fmt.Errorf("failed to embed code snippet: %w", err)
	}

	// 3. Query VectorStore for matching IDs (Top-5)
	results := vStore.Search(emb, 5)

	var duplicates []string
	for _, res := range results {
		if res.Score >= threshold {
			node, err := gdb.GetNode(res.ID)
			if err != nil || node == nil {
				continue
			}

			nodeType := node["type"].(string)
			fqn := node["fqn"].(string)
			docstring := node["docstring"].(string)

			// Skip if it's already in exact matches to avoid spam
			isClash := false
			if extractedName != "" && strings.EqualFold(node["name"].(string), extractedName) {
				isClash = true
			}

			if !isClash {
				var dsb strings.Builder
				dsb.WriteString(fmt.Sprintf("- [Semantic Match] [%s] %s (Similarity Score: %.4f)\n", nodeType, fqn, res.Score))
				if docstring != "" {
					firstLine := strings.Split(docstring, "\n")[0]
					if len(firstLine) > 80 {
						firstLine = firstLine[:80] + "..."
					}
					dsb.WriteString(fmt.Sprintf("  Docstring: %s\n", firstLine))
				}
				duplicates = append(duplicates, dsb.String())
			}
		}
	}

	if len(duplicates) == 0 && len(exactMatches) == 0 {
		return "Great news! No semantic duplicates or similar files/methods were found in the codebase matching your proposed snippet.", nil
	}

	var sb strings.Builder
	sb.WriteString("⚠️ WARNING: Potential Semantic Duplication Found!\n")
	sb.WriteString("==================================================\n")
	sb.WriteString("Before creating a new file or method, please examine if you can reuse, extend, or refactor the following existing elements:\n\n")

	for _, clash := range exactMatches {
		sb.WriteString(clash)
		sb.WriteString("\n")
	}

	for _, d := range duplicates {
		sb.WriteString(d)
		sb.WriteString("\n")
	}
	sb.WriteString("--------------------------------------------------\n")
	sb.WriteString("Recommendation: Follow DRY principles. Extend the existing method or class instead of copy-pasting or creating greenfield files.")

	return sb.String(), nil
}

// RunReadNode retrieves the code and docstring for a specific node given its FQN.
func RunReadNode(fqn string, gdb *db.GraphDB) (string, error) {
	// Querying by FQN filter to prevent fetching the whole database
	nodes, err := gdb.QueryNodes("", fqn, "")
	if err != nil {
		return "", err
	}

	for _, node := range nodes {
		nodeFQN, ok := node["fqn"].(string)
		if !ok {
			continue
		}
		if nodeFQN == fqn {
			nodeType, _ := node["type"].(string)
			code, _ := node["code"].(string)
			docstring, _ := node["docstring"].(string)

			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("Node [%s] %s:\n", nodeType, fqn))
			sb.WriteString("==================================================\n")
			if docstring != "" {
				sb.WriteString(fmt.Sprintf("Docstring:\n%s\n\n", docstring))
			}
			if code != "" {
				sb.WriteString(fmt.Sprintf("Code:\n```\n%s\n```\n", code))
			} else {
				sb.WriteString("No code found for this node.\n")
			}
			return sb.String(), nil
		}
	}

	return fmt.Sprintf("Node with FQN '%s' not found.", fqn), nil
}

// RunQueryGraph executes a structural query pattern (e.g. Method(A) -> CALLS -> Method(B)) on the graph database.
func RunQueryGraph(pattern string, maxDepth int, gdb *db.GraphDB) (string, error) {
	results, err := gdb.QueryPattern(pattern, maxDepth)
	if err != nil {
		return "", err
	}

	if len(results) == 0 {
		return fmt.Sprintf("No results found for pattern: '%s' (max_depth: %d)", pattern, maxDepth), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Graph Query Results for pattern: '%s' (max_depth: %d)\n", pattern, maxDepth))
	sb.WriteString("==================================================\n\n")

	for i, res := range results {
		id, _ := res["id"].(string)
		nodeType, _ := res["type"].(string)
		name, _ := res["name"].(string)
		fqn, _ := res["fqn"].(string)

		// The generic QueryPattern usually returns matched nodes, we can display them
		sb.WriteString(fmt.Sprintf("%d. [%s] %s (FQN: %s)\n", i+1, nodeType, name, fqn))
		sb.WriteString(fmt.Sprintf("   [Code omitted. Use m_cpg_read_node with fqn '%s' to read full code]\n", fqn))
		sb.WriteString(fmt.Sprintf("   Node ID: %s\n", id))
		sb.WriteString("--------------------------------------------------\n")
	}

	return sb.String(), nil
}

// RunGetNodeDependencies fetches the callers and callees of a specific node given its FQN
func RunGetNodeDependencies(fqn string, gdb *db.GraphDB) (string, error) {
	nodes, err := gdb.QueryNodes("", fqn, "")
	if err != nil {
		return "", err
	}

	var targetNodeID string
	var targetNodeType string
	for _, node := range nodes {
		nodeFQN, ok := node["fqn"].(string)
		if ok && nodeFQN == fqn {
			targetNodeID = node["id"].(string)
			targetNodeType = node["type"].(string)
			break
		}
	}

	if targetNodeID == "" {
		return fmt.Sprintf("Node with FQN '%s' not found.", fqn), nil
	}

	// Now we fetch callers (nodes that CALL targetNodeID)
	// and callees (nodes that targetNodeID CALLS)
	// We can use gdb.QueryPattern but that doesn't explicitly group by direction easily
	// unless we do two queries.

	callers, err := gdb.QueryPattern(fmt.Sprintf("*(*) -> CALLS -> %s(%s)", targetNodeType, targetNodeID), 1)
	if err != nil {
		return "", fmt.Errorf("failed to fetch callers: %v", err)
	}

	callees, err := gdb.QueryPattern(fmt.Sprintf("%s(%s) -> CALLS -> *(*)", targetNodeType, targetNodeID), 1)
	if err != nil {
		return "", fmt.Errorf("failed to fetch callees: %v", err)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Dependencies for Node [%s] %s:\n", targetNodeType, fqn))
	sb.WriteString("==================================================\n\n")

	sb.WriteString("### Callers (Methods/Functions that call this node):\n")
	if len(callers) == 0 {
		sb.WriteString("  - None found.\n")
	} else {
		for _, c := range callers {
			cNodeType, _ := c["type"].(string)
			cName, _ := c["name"].(string)
			cFQN, _ := c["fqn"].(string)
			if cFQN == fqn {
				continue // Skip self if QueryPattern returned it
			}
			sb.WriteString(fmt.Sprintf("  - [%s] %s (FQN: %s)\n", cNodeType, cName, cFQN))
		}
	}
	sb.WriteString("\n")

	sb.WriteString("### Callees (Methods/Functions called by this node):\n")
	if len(callees) == 0 {
		sb.WriteString("  - None found.\n")
	} else {
		for _, c := range callees {
			cNodeType, _ := c["type"].(string)
			cName, _ := c["name"].(string)
			cFQN, _ := c["fqn"].(string)
			if cFQN == fqn {
				continue
			}
			sb.WriteString(fmt.Sprintf("  - [%s] %s (FQN: %s)\n", cNodeType, cName, cFQN))
		}
	}

	return sb.String(), nil
}

// RunFindPath searches for the shortest path between two nodes in the graph using BFS.
func RunFindPath(sourceFqn, targetFqn string, gdb *db.GraphDB) (string, error) {
	sourceNodes, err := gdb.QueryNodes("", sourceFqn, "")
	if err != nil {
		return "", err
	}
	var sourceNodeID string
	for _, node := range sourceNodes {
		if nodeFQN, ok := node["fqn"].(string); ok && nodeFQN == sourceFqn {
			sourceNodeID = node["id"].(string)
			break
		}
	}
	if sourceNodeID == "" {
		return fmt.Sprintf("Source node with FQN '%s' not found.", sourceFqn), nil
	}

	targetNodes, err := gdb.QueryNodes("", targetFqn, "")
	if err != nil {
		return "", err
	}
	var targetNodeID string
	for _, node := range targetNodes {
		if nodeFQN, ok := node["fqn"].(string); ok && nodeFQN == targetFqn {
			targetNodeID = node["id"].(string)
			break
		}
	}
	if targetNodeID == "" {
		return fmt.Sprintf("Target node with FQN '%s' not found.", targetFqn), nil
	}

	if sourceNodeID == targetNodeID {
		return fmt.Sprintf("Source and target nodes are the same ('%s').", sourceFqn), nil
	}

	// BFS
	queue := [][]string{{sourceNodeID}}
	visited := make(map[string]bool)
	visited[sourceNodeID] = true

	for len(queue) > 0 {
		path := queue[0]
		queue = queue[1:]
		current := path[len(path)-1]

		if current == targetNodeID {
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("Shortest path from '%s' to '%s':\n", sourceFqn, targetFqn))
			for i, nodeID := range path {
				node, err := gdb.GetNode(nodeID)
				if err != nil || node == nil {
					continue
				}
				nodeFqn, _ := node["fqn"].(string)
				nodeType, _ := node["type"].(string)
				if i > 0 {
					sb.WriteString(" -> ")
				}
				sb.WriteString(fmt.Sprintf("[%s] %s", nodeType, nodeFqn))
			}
			return sb.String(), nil
		}

		neighbors, err := gdb.GetNeighbors(current)
		if err == nil {
			for _, n := range neighbors {
				nID := n["id"].(string)
				if !visited[nID] {
					visited[nID] = true
					newPath := append([]string(nil), path...)
					newPath = append(newPath, nID)
					queue = append(queue, newPath)
				}
			}
		}
	}
	return fmt.Sprintf("No path found between '%s' and '%s'.", sourceFqn, targetFqn), nil
}

// RunGenerateWiki generates memory_index.md and memory_log.md in the project's memory-bank directory
func RunGenerateWiki(projectPath string, projectID string, gdb *db.GraphDB) (string, error) {
	mbDir := filepath.Join(projectPath, "memory-bank")
	if err := os.MkdirAll(mbDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create memory-bank directory: %w", err)
	}

	// 1. Generate memory_index.md (Content-oriented)
	concepts, err := gdb.GetTopConcepts(50)
	if err != nil {
		return "", fmt.Errorf("failed to get top concepts: %w", err)
	}

	events, err := gdb.GetAllActiveEvents(projectID)
	if err != nil {
		return "", fmt.Errorf("failed to get events: %w", err)
	}

	indexFile := filepath.Join(mbDir, "memory_index.md")
	var idxSb strings.Builder
	idxSb.WriteString("# Memory Index\n\nThis is a content-oriented catalog of everything the AI agent has learned, organized by abstract concepts.\n\n")

	idxSb.WriteString("## High-Level Concepts\n\n")
	for _, c := range concepts {
		name := c["name"].(string)
		frequency := c["frequency"].(int)
		idxSb.WriteString(fmt.Sprintf("- **%s** (Mentions: %d)\n", name, frequency))
	}
	idxSb.WriteString("\n## Memory Catalog\n\n")

	// Group events by type for index
	eventsByType := make(map[string][]map[string]any)
	for _, ev := range events {
		eventType := ev["event_type"].(string)
		eventsByType[eventType] = append(eventsByType[eventType], ev)
	}

	for eType, evs := range eventsByType {
		idxSb.WriteString(fmt.Sprintf("### %s\n", strings.Title(eType)))
		for _, ev := range evs {
			summary := ev["summary"].(string)
			timestamp := time.Unix(ev["timestamp"].(int64), 0).Format("2006-01-02")
			impStr := ""
			if imp, ok := ev["importance"].(int); ok && imp >= 5 {
				impStr = " 🔥"
			} else if imp64, ok := ev["importance"].(int64); ok && imp64 >= 5 {
				impStr = " 🔥"
			}
			idxSb.WriteString(fmt.Sprintf("- [%s] %s%s\n", timestamp, summary, impStr))
		}
		idxSb.WriteString("\n")
	}

	if err := os.WriteFile(indexFile, []byte(idxSb.String()), 0644); err != nil {
		return "", fmt.Errorf("failed to write memory_index.md: %w", err)
	}

	// 2. Generate memory_log.md (Chronological)
	// Sort all events by timestamp descending
	sort.Slice(events, func(i, j int) bool {
		return events[i]["timestamp"].(int64) > events[j]["timestamp"].(int64)
	})

	logFile := filepath.Join(mbDir, "memory_log.md")
	var logSb strings.Builder
	logSb.WriteString("# Memory Log\n\nAn append-only chronological record of operations and memories.\n\n")

	for _, ev := range events {
		timestamp := time.Unix(ev["timestamp"].(int64), 0).Format("2006-01-02 15:04:05")
		eventType := ev["event_type"].(string)
		summary := ev["summary"].(string)
		details := ev["details"].(string)

		logSb.WriteString(fmt.Sprintf("## [%s] %s | %s\n", timestamp, eventType, summary))
		if details != "" {
			logSb.WriteString(fmt.Sprintf("%s\n\n", details))
		}
	}

	if err := os.WriteFile(logFile, []byte(logSb.String()), 0644); err != nil {
		return "", fmt.Errorf("failed to write memory_log.md: %w", err)
	}

	return fmt.Sprintf("Successfully generated memory_index.md and memory_log.md in %s", mbDir), nil
}

// RunSynthesizeMemories archives old fragmented events and saves a new high-importance synthesis event.
func RunSynthesizeMemories(eventIDs []string, summary, synthesis, projectID string, gdb *db.GraphDB, cfg *config.Config) (string, error) {
	if err := VerifyEmbeddingConfig(gdb, cfg); err != nil {
		return "", err
	}

	// Archive old events
	if len(eventIDs) > 0 {
		if err := gdb.ArchiveEvents(nil, eventIDs); err != nil {
			return "", fmt.Errorf("failed to archive old fragmented events: %w", err)
		}
	}

	// Create new synthesis event
	id := uuid.New().String()
	timestamp := time.Now().Unix()
	eventType := "synthesis"
	importance := 8 // High importance for synthesized knowledge

	embedText := fmt.Sprintf("[%s] %s\n%s", eventType, summary, synthesis)
	emb, err := vector.GetEmbedding(
		embedText,
		cfg.Embedding.Provider,
		cfg.Embedding.Model,
		cfg.Embedding.Endpoint,
		cfg.Embedding.APIKey,
		cfg.Embedding.Dimension,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[MCP] Warning: Failed to embed synthesis: %v. Falling back to pseudo-embedding.\n", err)
		emb = vector.GeneratePseudoEmbedding(embedText, cfg.Embedding.Dimension)
	}

	embBytes := vector.Float32SliceToBytes(emb)
	err = gdb.SaveEvent(nil, id, eventType, summary, synthesis, timestamp, embBytes, "active", importance, projectID)
	if err != nil {
		return "", fmt.Errorf("failed to save synthesized memory to DB: %w", err)
	}

	// Extract and save abstract concepts
	concepts := concept.ExtractConcepts(summary + " " + synthesis)
	if err := gdb.SaveConcepts(nil, id, concepts); err != nil {
		fmt.Fprintf(os.Stderr, "[MCP] Warning: Failed to save concepts for synthesis %s: %v\n", id, err)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Successfully saved synthesized Wiki memory '%s' (ID: %s).\n", summary, id))
	if len(eventIDs) > 0 {
		sb.WriteString(fmt.Sprintf("Archived %d deprecated fragmented events.\n", len(eventIDs)))
	}
	return sb.String(), nil
}
