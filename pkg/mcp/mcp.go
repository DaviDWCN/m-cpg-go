package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

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

func StartServer(gdb *db.GraphDB, vStore *vector.VectorStore, cfg *config.Config) error {
	fmt.Fprintln(os.Stderr, "[MCP] Starting m-cpg-go stdio server...")

	// Initial load: Load all stored vectors from DB into the in-memory VectorStore
	if err := LoadVectorsIntoMemory(gdb, vStore); err != nil {
		fmt.Fprintf(os.Stderr, "[MCP] Warning: Failed to load vectors from DB: %v\n", err)
	}

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
				Description: "Checks if a proposed code snippet or functional description matches existing methods/files in the codebase to prevent semantic duplication.",
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
					},
					Required: []string{"summary", "details", "event_type"},
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
			Summary   string `json:"summary"`
			Details   string `json:"details"`
			EventType string `json:"event_type"`
		}
		if err := json.Unmarshal(args, &params); err != nil {
			return nil, err
		}

		err := RunRemember(params.Summary, params.Details, params.EventType, gdb, cfg)
		if err != nil {
			return &mcpToolCallResult{
				Content: []mcpContent{{Type: "text", Text: fmt.Sprintf("Failed to save memory: %v", err)}},
			}, nil
		}
		return &mcpToolCallResult{
			Content: []mcpContent{{Type: "text", Text: "Memory saved successfully!"}},
		}, nil

	case "m_cpg_get_preferences":
		res, err := RunGetPreferences(gdb)
		if err != nil {
			return &mcpToolCallResult{
				Content: []mcpContent{{Type: "text", Text: fmt.Sprintf("Failed to fetch preferences: %v", err)}},
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
	fmt.Fprintf(os.Stderr, "[MCP] Clearing old graph nodes for project: %s\n", projectID)
	gdb.ClearProject(projectID)

	var pyFiles, goFiles, mdFiles []string
	err := filepath.Walk(projectPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// Skip hidden dirs
			if strings.HasPrefix(info.Name(), ".") && info.Name() != "." {
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
		}
		return nil
	})
	if err != nil {
		return 0, 0, 0, err
	}

	allFiles := append(append(pyFiles, goFiles...), mdFiles...)
	fmt.Fprintf(os.Stderr, "[MCP] Found %d files to analyze (Python, Go, Markdown).\n", len(allFiles))

	nodesCount := 0
	edgesCount := 0

	for _, file := range allFiles {
		entities, relations, err := parser.ParseFile(file, projectID, projectPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[MCP] Warning: Failed to parse %s: %v\n", file, err)
			continue
		}

		// Insert nodes & generate vector embeddings
		for _, ent := range entities {
			// Choose what text to embed: prefer Docstring, fallback to Code (truncated), fallback to Name
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

			// Add to sqlite graph DB
			err = gdb.AddNode(ent.ID, ent.Type, ent.Name, ent.FQN, ent.Code, ent.Docstring, projectID, nil)
			if err != nil {
				fmt.Fprintf(os.Stderr, "[MCP] Failed to save node: %v\n", err)
				continue
			}
			nodesCount++

			// Fetch or generate vector embedding
			emb, err := vector.GetEmbedding(
				embedText,
				cfg.Embedding.Provider,
				cfg.Embedding.Model,
				cfg.Embedding.Endpoint,
				cfg.Embedding.APIKey,
			)
			if err == nil {
				// Save embedding to sqlite vectors table
				embBytes := vector.Float32SliceToBytes(emb)
				gdb.SaveVector(ent.ID, embBytes, map[string]any{"type": ent.Type, "fqn": ent.FQN, "name": ent.Name})
				
				// Update in-memory vector store
				vStore.AddVector(ent.ID, emb, map[string]any{"type": ent.Type, "fqn": ent.FQN, "name": ent.Name})
			}
		}

		// Insert edges
		for _, rel := range relations {
			err = gdb.AddEdge(rel.Source, rel.Target, rel.Label, nil)
			if err != nil {
				fmt.Fprintf(os.Stderr, "[MCP] Failed to save edge: %v\n", err)
				continue
			}
			edgesCount++
		}
	}

	return len(allFiles), nodesCount, edgesCount, nil
}

func RunSearch(query string, topK int, gdb *db.GraphDB, vStore *vector.VectorStore, cfg *config.Config) (string, error) {
	// 1. Get embedding for the query
	emb, err := vector.GetEmbedding(
		query,
		cfg.Embedding.Provider,
		cfg.Embedding.Model,
		cfg.Embedding.Endpoint,
		cfg.Embedding.APIKey,
	)
	if err != nil {
		return "", fmt.Errorf("failed to embed query: %w", err)
	}

	// 2. Query VectorStore for matching IDs
	results := vStore.Search(emb, topK)
	if len(results) == 0 {
		return fmt.Sprintf("No relevant code memory found for query: '%s'", query), nil
	}

	// 3. Construct context output from graph & node data
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Hybrid Search Results for query: '%s'\n", query))
	sb.WriteString("==================================================\n\n")

	for i, res := range results {
		node, err := gdb.GetNode(res.ID)
		if err != nil || node == nil {
			continue
		}

		nodeType := node["type"].(string)
		fqn := node["fqn"].(string)
		code := node["code"].(string)
		docstring := node["docstring"].(string)

		sb.WriteString(fmt.Sprintf("%d. [%s] %s (Score: %.4f)\n", i+1, nodeType, fqn, res.Score))
		if docstring != "" {
			sb.WriteString(fmt.Sprintf("   Docstring: %s\n", strings.ReplaceAll(docstring, "\n", "\n   ")))
		}

		// Fetch neighboring classes/methods/modules
		neighbors, _ := gdb.GetNeighbors(res.ID)
		if len(neighbors) > 0 {
			var nList []string
			for _, n := range neighbors {
				nList = append(nList, fmt.Sprintf("%s (%s)", n["name"], n["type"]))
			}
			sb.WriteString(fmt.Sprintf("   Neighbors: %s\n", strings.Join(nList, ", ")))
		}

		if code != "" {
			// Limit printed code length if too long
			codeLines := strings.Split(code, "\n")
			if len(codeLines) > 30 {
				code = strings.Join(codeLines[:30], "\n") + "\n... [Code truncated, total lines: " + fmt.Sprintf("%d", len(codeLines)) + "]"
			}
			sb.WriteString(fmt.Sprintf("   Code:\n   ```\n   %s\n   ```\n", strings.ReplaceAll(code, "\n", "\n   ")))
		}
		sb.WriteString("--------------------------------------------------\n\n")
	}

	return sb.String(), nil
}

func LoadVectorsIntoMemory(gdb *db.GraphDB, vStore *vector.VectorStore) error {
	list, err := gdb.LoadVectors()
	if err != nil {
		return err
	}

	for _, v := range list {
		nodeID := v["node_id"].(string)
		embBytes := v["embedding"].([]byte)
		meta := v["metadata"].(map[string]any)

		emb := vector.BytesToFloat32Slice(embBytes)
		if len(emb) > 0 {
			vStore.AddVector(nodeID, emb, meta)
		}
	}

	fmt.Fprintf(os.Stderr, "[MCP] Loaded %d vectors into memory index.\n", len(list))
	return nil
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

// RunRemember saves a developer preference, compilation fix, or session log to the DB and generates vector embedding.
func RunRemember(summary, details, eventType string, gdb *db.GraphDB, cfg *config.Config) error {
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
	)
	if err != nil {
		return fmt.Errorf("failed to embed memory: %w", err)
	}

	embBytes := vector.Float32SliceToBytes(emb)
	err = gdb.SaveEvent(id, eventType, summary, details, timestamp, embBytes)
	if err != nil {
		return fmt.Errorf("failed to save memory to DB: %w", err)
	}

	fmt.Fprintf(os.Stderr, "[MCP] Successfully remembered event: %s\n", summary)
	return nil
}

// RunGetPreferences queries the most recent 10 events from memory
func RunGetPreferences(gdb *db.GraphDB) (string, error) {
	events, err := gdb.GetRecentEvents(10)
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
		nodes, err := gdb.QueryNodes("", "", "")
		if err == nil {
			for _, node := range nodes {
				name := node["name"].(string)
				nodeType := node["type"].(string)
				fqn := node["fqn"].(string)
				if strings.EqualFold(name, extractedName) {
					exactMatches = append(exactMatches, fmt.Sprintf("- [Exact Name Match] [%s] %s (Matches proposed name: '%s')\n", nodeType, fqn, extractedName))
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
