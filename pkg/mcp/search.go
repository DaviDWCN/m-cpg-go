package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"m-cpg-go/pkg/config"
	"m-cpg-go/pkg/db"
	"m-cpg-go/pkg/vector"
)

func RunSearch(query string, topK int, gdb *db.GraphDB, vStore *vector.VectorStore, cfg *config.Config) (string, error) {
	if err := VerifyEmbeddingConfig(gdb, cfg); err != nil {
		return "", err
	}
	// 1. Embed the query once and reuse it for code search and memory search.
	emb, src, err := vector.GetEmbedding(
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

	// 2. Vector hits. A vec0 failure still leaves the lexical channel usable.
	vectorLimit := topK * 3
	if vectorLimit < topK {
		vectorLimit = topK
	}
	results, vecErr := vStore.Search(emb, vectorLimit, cfg.ProjectID)
	if vecErr != nil {
		fmt.Fprintf(os.Stderr, "[vector] search: %v\n", vecErr)
		results = nil
	}

	// RRF lists: vector distance, lexical symbol overlap, and one-hop neighbors.
	vectorRanks := make(map[string]int)
	lexicalRanks := make(map[string]int)
	graphScores := make(map[string]float32)

	lexicalHits, lexErr := gdb.SearchLexical(cfg.ProjectID, query, vectorLimit)
	if lexErr != nil {
		fmt.Fprintf(os.Stderr, "[search] lexical: %v\n", lexErr)
	}
	for i, hit := range lexicalHits {
		lexicalRanks[hit.ID] = i + 1
	}

	seedIDs := make([]string, 0, len(results)+len(lexicalHits))
	for i, res := range results {
		vectorRanks[res.ID] = i + 1
		seedIDs = append(seedIDs, res.ID)
	}
	for _, hit := range lexicalHits {
		if _, ok := vectorRanks[hit.ID]; !ok {
			seedIDs = append(seedIDs, hit.ID)
		}
	}
	if len(seedIDs) > 20 {
		seedIDs = seedIDs[:20]
	}
	for _, id := range seedIDs {
		neighbors, _ := gdb.GetNeighbors(id)
		seedScore := float32(1)
		for _, res := range results {
			if res.ID == id {
				seedScore = res.Score
				break
			}
		}
		for _, n := range neighbors {
			nID, _ := n["id"].(string)
			if nID == "" {
				continue
			}
			relation, _ := n["relation"].(string)
			weight := float32(1.0)
			if relation == "CALLS" {
				weight = 2.0
			}
			graphScores[nID] += seedScore * weight
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
	for id := range lexicalRanks {
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
		if rank, ok := lexicalRanks[id]; ok {
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
	memResult, _ := runSearchMemoryEmbedded(query, emb, src, topK, cfg.ProjectID, gdb)
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

	nodes, err := gdb.QueryStructureByFQNPrefix(projectID, fqnPattern)
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("File Structure for '%s' (derived FQN pattern: '%s'):\n", filePath, fqnPattern))
	sb.WriteString("==================================================\n")

	foundCount := 0
	for _, node := range nodes {
		foundCount++
		sb.WriteString(fmt.Sprintf("- [%s] %s\n", node.Type, node.FQN))
		if node.Docstring != "" {
			firstLine := strings.Split(node.Docstring, "\n")[0]
			if len(firstLine) > 60 {
				firstLine = truncateString(firstLine, 60) + "..."
			}
			sb.WriteString(fmt.Sprintf("  Docstring: %s\n", firstLine))
		}
	}

	if foundCount == 0 {
		return fmt.Sprintf("No structural nodes found for file: %s", filePath), nil
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
	emb, _, err := vector.GetEmbedding(
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
	results, err := vStore.Search(emb, 5, cfg.ProjectID)
	if err != nil {
		return "", fmt.Errorf("failed to search vectors: %w", err)
	}

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
						firstLine = truncateString(firstLine, 80) + "..."
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
	node, err := gdb.GetNodeByFQN(fqn, "")
	if err != nil {
		return "", err
	}
	if node == nil {
		return fmt.Sprintf("Node with FQN '%s' not found.", fqn), nil
	}

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
		source, _ := res["source"].(map[string]any)
		edge, _ := res["edge"].(map[string]any)
		target, _ := res["target"].(map[string]any)
		srcFQN, _ := source["fqn"].(string)
		srcType, _ := source["type"].(string)
		tgtFQN, _ := target["fqn"].(string)
		tgtType, _ := target["type"].(string)
		label, _ := edge["label"].(string)
		depth, _ := res["depth"].(int)

		sb.WriteString(fmt.Sprintf("%d. [%s] %s -[%s]-> [%s] %s (depth %d)\n", i+1, srcType, srcFQN, label, tgtType, tgtFQN, depth))
		sb.WriteString("--------------------------------------------------\n")
	}

	return sb.String(), nil
}

// RunGetNodeDependencies fetches the callers and callees of a specific node given its FQN
func RunGetNodeDependencies(fqn string, gdb *db.GraphDB) (string, error) {
	node, err := gdb.GetNodeByFQN(fqn, "")
	if err != nil {
		return "", err
	}
	if node == nil {
		return fmt.Sprintf("Node with FQN '%s' not found.", fqn), nil
	}
	targetNodeID, _ := node["id"].(string)
	targetNodeType, _ := node["type"].(string)

	callers, callees, err := gdb.GetCallEdges(targetNodeID)
	if err != nil {
		return "", fmt.Errorf("failed to fetch call edges: %w", err)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Dependencies for Node [%s] %s:\n", targetNodeType, fqn))
	sb.WriteString("==================================================\n\n")

	sb.WriteString("### Callers (Methods/Functions that call this node):\n")
	if len(callers) == 0 {
		sb.WriteString("  - None found.\n")
	} else {
		for _, c := range callers {
			sb.WriteString(fmt.Sprintf("  - [%s] %s (FQN: %s)\n", c.Type, c.Name, c.FQN))
		}
	}
	sb.WriteString("\n")

	sb.WriteString("### Callees (Methods/Functions called by this node):\n")
	if len(callees) == 0 {
		sb.WriteString("  - None found.\n")
	} else {
		for _, c := range callees {
			sb.WriteString(fmt.Sprintf("  - [%s] %s (FQN: %s)\n", c.Type, c.Name, c.FQN))
		}
	}

	return sb.String(), nil
}

// RunFindPath searches for the shortest path between two nodes in the graph using BFS.
func RunFindPath(sourceFqn, targetFqn string, gdb *db.GraphDB) (string, error) {
	sourceNode, err := gdb.GetNodeByFQN(sourceFqn, "")
	if err != nil {
		return "", err
	}
	if sourceNode == nil {
		return fmt.Sprintf("Source node with FQN '%s' not found.", sourceFqn), nil
	}
	sourceNodeID, _ := sourceNode["id"].(string)

	targetNode, err := gdb.GetNodeByFQN(targetFqn, "")
	if err != nil {
		return "", err
	}
	if targetNode == nil {
		return fmt.Sprintf("Target node with FQN '%s' not found.", targetFqn), nil
	}
	targetNodeID, _ := targetNode["id"].(string)

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
