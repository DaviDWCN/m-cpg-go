package mcp

import (
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"m-cpg-go/pkg/config"
	"m-cpg-go/pkg/db"
	"m-cpg-go/pkg/parser"
	"m-cpg-go/pkg/vector"
)

func computeFileMD5(filePath string) (string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}
	hash := md5.Sum(data)
	return hex.EncodeToString(hash[:]), nil
}

func RunIndexing(projectPath, projectID string, gdb *db.GraphDB, vStore *vector.VectorStore, cfg *config.Config) (int, int, int, error) {
	// Validate configuration
	if err := VerifyEmbeddingConfig(gdb, cfg); err != nil {
		return 0, 0, 0, err
	}

	projectPath, err := canonicalizePath(projectPath)
	if err != nil {
		return 0, 0, 0, err
	}

	var pyFiles, goFiles, mdFiles, javaFiles, scriptFiles []string
	err = filepath.Walk(projectPath, func(path string, info os.FileInfo, err error) error {
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

		path = canonicalizePathOrSelf(path)
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
		case ".ts", ".tsx", ".js", ".jsx":
			scriptFiles = append(scriptFiles, path)
		}
		return nil
	})
	if err != nil {
		return 0, 0, 0, err
	}

	allFiles := append(append(append(append(pyFiles, goFiles...), mdFiles...), javaFiles...), scriptFiles...)

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

	// Clean up database entries for changed and deleted files.
	// Incoming edges from other files are restored after the new nodes exist,
	// because deleting a node cascades those edges.
	var savedEdges []db.EdgeRecord
	err = gdb.RunInTransaction(func(tx *sql.Tx) error {
		for _, file := range append(append([]string{}, changedFiles...), deletedFiles...) {
			incoming, err := gdb.SnapshotIncomingEdges(tx, file, projectID)
			if err != nil {
				return err
			}
			savedEdges = append(savedEdges, incoming...)
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

	// 1. Parse changed files in CPU memory. Python files share one interpreter.
	parsedEntities, parsedRelations := parseChangedFiles(changedFiles, projectID, projectPath)

	if len(parsedEntities) == 0 {
		// Update hashes even if files parsed to empty
		now := time.Now().Unix()
		err = gdb.RunInTransaction(func(tx *sql.Tx) error {
			if err := gdb.RestoreEdges(tx, savedEdges); err != nil {
				return err
			}
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
		source      vector.EmbeddingSource
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
				emb, src, err := vector.GetEmbedding(
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
					source:      src,
					err:         err,
				}
			}
		}()
	}

	for i, ent := range parsedEntities {
		tasksChan <- embedTask{entityIndex: i, text: entityEmbedText(ent)}
	}
	close(tasksChan)

	go func() {
		wg.Wait()
		close(resultsChan)
	}()

	embeddings := make([][]float32, len(parsedEntities))
	for res := range resultsChan {
		// Pseudo vectors are a different geometry from API vectors. Keep them
		// out of vec0 so a fallback during indexing cannot poison search.
		if res.err != nil || res.source != vector.EmbeddingSourceAPI || len(res.embedding) != cfg.Embedding.Dimension {
			continue
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
				if rel.Target == "" {
					if rel.Callee != "" {
						if err := gdb.SaveUnresolvedCall(tx, rel.Source, rel.Callee, projectID); err != nil {
							return err
						}
					}
					continue
				}
				if !insertedNodeIDs[rel.Source] || !insertedNodeIDs[rel.Target] {
					if rel.Label == "CALLS" {
						callee := rel.Callee
						if callee == "" {
							callee = rel.Target
						}
						if err := gdb.SaveUnresolvedCall(tx, rel.Source, callee, projectID); err != nil {
							return err
						}
					}
					continue
				}
				propsJSON := "{}"
				if len(rel.Properties) > 0 {
					data, _ := json.Marshal(rel.Properties)
					propsJSON = string(data)
				}
				query := `INSERT OR REPLACE INTO edges (source, target, label, properties) VALUES (?, ?, ?, ?);`
				if _, err := tx.Exec(query, rel.Source, rel.Target, rel.Label, propsJSON); err != nil {
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

	// Hashes are written only after edges are restored and unresolved calls are
	// bound. A failure here leaves the files dirty so the next index retries.
	now := time.Now().Unix()
	if err := gdb.RunInTransaction(func(tx *sql.Tx) error {
		if err := gdb.RestoreEdges(tx, savedEdges); err != nil {
			return err
		}
		if _, err := gdb.ResolveProjectCalls(tx, projectID); err != nil {
			return err
		}
		for _, file := range changedFiles {
			if hash, ok := changedHashes[file]; ok && hash != "" {
				if err := gdb.SaveFileHash(tx, file, projectID, hash, now); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		return 0, 0, 0, fmt.Errorf("failed to finalize index: %w", err)
	}

	return len(allFiles), nodesCount, edgesCount, nil
}

func parseChangedFiles(files []string, projectID, projectPath string) ([]parser.CodeEntity, []parser.CodeRelation) {
	var pyFiles, other []string
	for _, file := range files {
		if strings.EqualFold(filepath.Ext(file), ".py") {
			pyFiles = append(pyFiles, file)
		} else {
			other = append(other, file)
		}
	}

	var entities []parser.CodeEntity
	var relations []parser.CodeRelation
	if len(pyFiles) > 0 {
		batch, err := parser.ParsePythonBatch(pyFiles, projectID, projectPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[MCP] Python batch parse failed (%v); parsing files individually.\n", err)
			other = append(other, pyFiles...)
		} else {
			for _, file := range pyFiles {
				item := batch[file]
				entities = append(entities, item.Entities...)
				relations = append(relations, item.Relations...)
			}
		}
	}
	for _, file := range other {
		ents, rels, err := parser.ParseFile(file, projectID, projectPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[MCP] Warning: Failed to parse %s: %v\n", file, err)
			continue
		}
		entities = append(entities, ents...)
		relations = append(relations, rels...)
	}
	return entities, relations
}

func entityEmbedText(ent parser.CodeEntity) string {
	switch ent.Type {
	case "Method":
		body := ent.Code
		if len([]rune(body)) > 500 {
			body = truncateString(body, 500)
		}
		parts := []string{ent.FQN}
		if ent.Docstring != "" {
			parts = append(parts, ent.Docstring)
		}
		if body != "" {
			parts = append(parts, body)
		}
		return strings.Join(parts, "\n")
	case "Module":
		if ent.Docstring != "" {
			return truncateString(ent.Docstring+"\n"+ent.Name, 1000)
		}
		return ent.Name
	default:
		text := ent.Docstring
		if text == "" {
			text = ent.Code
		}
		if text == "" {
			return ent.Name
		}
		return truncateString(text, 800)
	}
}

func canonicalizePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return abs, nil
	}
	return resolved, nil
}

func canonicalizePathOrSelf(path string) string {
	resolved, err := canonicalizePath(path)
	if err != nil {
		return path
	}
	return resolved
}
