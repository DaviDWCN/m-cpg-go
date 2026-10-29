package mcp

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"m-cpg-go/pkg/config"
	"m-cpg-go/pkg/db"
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
