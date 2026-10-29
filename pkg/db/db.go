package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	_ "github.com/asg017/sqlite-vec-go-bindings/ncruces"
	_ "github.com/ncruces/go-sqlite3/driver"
	"os"
	"path/filepath"
	"strings"
)

type GraphDB struct {
	db *sql.DB
}

func (g *GraphDB) GetDB() *sql.DB {
	return g.db
}

type sqlExecutor interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

func (g *GraphDB) getExecutor(tx *sql.Tx) sqlExecutor {
	if tx != nil {
		return tx
	}
	return g.db
}

// RunInTransaction runs the provided function in an SQLite transaction
func (g *GraphDB) RunInTransaction(fn func(tx *sql.Tx) error) error {
	tx, err := g.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			tx.Rollback()
			panic(p)
		}
	}()
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}
	return nil
}

// InitDB initializes the SQLite database, creates schema, and returns GraphDB instance
func InitDB(dbPath string, vectorDim int) (*GraphDB, error) {
	// Ensure parent directory exists
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create db directory: %w", err)
	}

	// Open SQLite database
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database: %w", err)
	}
	db.SetMaxOpenConns(1)

	gdb := &GraphDB{db: db}
	if err := gdb.ensureSchema(vectorDim); err != nil {
		db.Close()
		return nil, err
	}

	return gdb, nil
}

func (g *GraphDB) ensureSchema(vectorDim int) error {
	// Enable WAL mode and other optimizations for concurrency and performance
	pragmas := []string{
		"PRAGMA foreign_keys=ON;",
		"PRAGMA journal_mode=WAL;",
		"PRAGMA synchronous=NORMAL;",
		"PRAGMA temp_store=MEMORY;",
		"PRAGMA cache_size=-20000;",
		"PRAGMA busy_timeout=5000;",
	}
	for _, pragma := range pragmas {
		if _, err := g.db.Exec(pragma); err != nil {
			return fmt.Errorf("failed to execute pragma %s: %w", pragma, err)
		}
	}

	// Create nodes table
	createNodesTable := `
	CREATE TABLE IF NOT EXISTS nodes (
		id TEXT PRIMARY KEY,
		type TEXT NOT NULL,
		name TEXT NOT NULL,
		fqn TEXT NOT NULL,
		code TEXT,
		docstring TEXT,
		project_id TEXT,
		properties TEXT,
		file_path TEXT
	);`
	if _, err := g.db.Exec(createNodesTable); err != nil {
		return fmt.Errorf("failed to create nodes table: %w", err)
	}

	// Handle migration for existing nodes table
	alterNodesTableFilePath := `ALTER TABLE nodes ADD COLUMN file_path TEXT;`
	_, alterNodesErr := g.db.Exec(alterNodesTableFilePath)
	if alterNodesErr != nil && !strings.Contains(alterNodesErr.Error(), "duplicate column name") {
		fmt.Fprintf(os.Stderr, "[DB] Note: Could not alter nodes table file_path column: %v\n", alterNodesErr)
	}

	// Create edges table
	createEdgesTable := `
	CREATE TABLE IF NOT EXISTS edges (
		source TEXT NOT NULL,
		target TEXT NOT NULL,
		label TEXT NOT NULL,
		properties TEXT,
		PRIMARY KEY (source, target, label),
		FOREIGN KEY (source) REFERENCES nodes(id) ON DELETE CASCADE,
		FOREIGN KEY (target) REFERENCES nodes(id) ON DELETE CASCADE
	);`
	if _, err := g.db.Exec(createEdgesTable); err != nil {
		return fmt.Errorf("failed to create edges table: %w", err)
	}

	// Create vectors table
	createVectorsTable := fmt.Sprintf(`CREATE VIRTUAL TABLE IF NOT EXISTS vectors USING vec0(embedding float[%d]);`, vectorDim)
	createVectorsMetaTable := `CREATE TABLE IF NOT EXISTS vectors_meta (
		rowid INTEGER PRIMARY KEY,
		node_id TEXT UNIQUE NOT NULL,
		metadata TEXT,
		FOREIGN KEY (node_id) REFERENCES nodes(id) ON DELETE CASCADE
	);`
	if _, err := g.db.Exec(createVectorsTable); err != nil {
		return fmt.Errorf("failed to create vectors table: %w", err)
	}
	if _, err := g.db.Exec(createVectorsMetaTable); err != nil {
		return fmt.Errorf("failed to create vectors meta table: %w", err)
	}

	// Note: We do NOT use an SQLite trigger here to clean up virtual table "vectors".
	// SQLite restricts modifying virtual tables from nested triggers during foreign key cascade deletes
	// and flags it as "unsafe use of virtual table". Instead, we perform manual cleanups.

	// Create events table for developer preferences and session logs
	createEventsTable := `
	CREATE TABLE IF NOT EXISTS events (
		id TEXT PRIMARY KEY,
		timestamp INTEGER NOT NULL,
		event_type TEXT NOT NULL,
		summary TEXT NOT NULL,
		details TEXT,
		embedding BLOB NOT NULL,
		status TEXT NOT NULL DEFAULT 'active',
		importance INTEGER NOT NULL DEFAULT 0,
		last_accessed INTEGER NOT NULL DEFAULT 0,
		project_id TEXT NOT NULL DEFAULT 'global'
	);`
	if _, err := g.db.Exec(createEventsTable); err != nil {
		return fmt.Errorf("failed to create events table: %w", err)
	}

	// Handle migration for existing events table
	alterEventsTable := `ALTER TABLE events ADD COLUMN status TEXT NOT NULL DEFAULT 'active';`
	_, alterErr := g.db.Exec(alterEventsTable)
	if alterErr != nil && !strings.Contains(alterErr.Error(), "duplicate column name") {
		fmt.Fprintf(os.Stderr, "[DB] Note: Could not alter events table status column: %v\n", alterErr)
	}

	alterEventsTableImp := `ALTER TABLE events ADD COLUMN importance INTEGER NOT NULL DEFAULT 0;`
	_, alterErrImp := g.db.Exec(alterEventsTableImp)
	if alterErrImp != nil && !strings.Contains(alterErrImp.Error(), "duplicate column name") {
		fmt.Fprintf(os.Stderr, "[DB] Note: Could not alter events table importance column: %v\n", alterErrImp)
	}

	alterEventsTableAcc := `ALTER TABLE events ADD COLUMN last_accessed INTEGER NOT NULL DEFAULT 0;`
	_, alterErrAcc := g.db.Exec(alterEventsTableAcc)
	if alterErrAcc != nil && !strings.Contains(alterErrAcc.Error(), "duplicate column name") {
		fmt.Fprintf(os.Stderr, "[DB] Note: Could not alter events table last_accessed column: %v\n", alterErrAcc)
	}

	alterEventsTableProjectID := `ALTER TABLE events ADD COLUMN project_id TEXT NOT NULL DEFAULT 'global';`
	_, alterProjErr := g.db.Exec(alterEventsTableProjectID)
	if alterProjErr != nil && !strings.Contains(alterProjErr.Error(), "duplicate column name") {
		fmt.Fprintf(os.Stderr, "[DB] Note: Could not alter events table project_id column: %v\n", alterProjErr)
	}

	// Create file_hashes table for incremental indexing
	createFileHashesTable := `
	CREATE TABLE IF NOT EXISTS file_hashes (
		file_path TEXT PRIMARY KEY,
		project_id TEXT NOT NULL,
		hash TEXT NOT NULL,
		last_indexed INTEGER NOT NULL
	);`
	if _, err := g.db.Exec(createFileHashesTable); err != nil {
		return fmt.Errorf("failed to create file_hashes table: %w", err)
	}

	// Create config_meta table for configuration validation
	createConfigMetaTable := `
	CREATE TABLE IF NOT EXISTS config_meta (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);`
	if _, err := g.db.Exec(createConfigMetaTable); err != nil {
		return fmt.Errorf("failed to create config_meta table: %w", err)
	}

	// Create concepts table
	createConceptsTable := `
	CREATE TABLE IF NOT EXISTS concepts (
		name TEXT PRIMARY KEY,
		frequency INTEGER NOT NULL DEFAULT 1
	);`
	if _, err := g.db.Exec(createConceptsTable); err != nil {
		return fmt.Errorf("failed to create concepts table: %w", err)
	}

	// Create event_concepts junction table
	createEventConceptsTable := `
	CREATE TABLE IF NOT EXISTS event_concepts (
		event_id TEXT NOT NULL,
		concept_name TEXT NOT NULL,
		PRIMARY KEY (event_id, concept_name),
		FOREIGN KEY (event_id) REFERENCES events(id) ON DELETE CASCADE,
		FOREIGN KEY (concept_name) REFERENCES concepts(name) ON DELETE CASCADE
	);`
	if _, err := g.db.Exec(createEventConceptsTable); err != nil {
		return fmt.Errorf("failed to create event_concepts table: %w", err)
	}

	createUnresolved := `
	CREATE TABLE IF NOT EXISTS unresolved_calls (
		source TEXT NOT NULL,
		callee TEXT NOT NULL,
		project_id TEXT NOT NULL,
		PRIMARY KEY (source, callee),
		FOREIGN KEY (source) REFERENCES nodes(id) ON DELETE CASCADE
	);`
	if _, err := g.db.Exec(createUnresolved); err != nil {
		return fmt.Errorf("failed to create unresolved_calls table: %w", err)
	}

	createSchemaMeta := `
	CREATE TABLE IF NOT EXISTS schema_meta (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);`
	if _, err := g.db.Exec(createSchemaMeta); err != nil {
		return fmt.Errorf("failed to create schema_meta table: %w", err)
	}
	// Create indexes for efficient retrieval and graph traversal
	indexes := []string{
		"CREATE INDEX IF NOT EXISTS idx_nodes_type ON nodes(type);",
		"CREATE INDEX IF NOT EXISTS idx_nodes_name ON nodes(name);",
		"CREATE INDEX IF NOT EXISTS idx_nodes_fqn ON nodes(fqn);",
		"CREATE INDEX IF NOT EXISTS idx_nodes_project ON nodes(project_id);",
		"CREATE INDEX IF NOT EXISTS idx_nodes_file_path ON nodes(file_path);",
		"CREATE INDEX IF NOT EXISTS idx_edges_source ON edges(source);",
		"CREATE INDEX IF NOT EXISTS idx_edges_target ON edges(target);",
		"CREATE INDEX IF NOT EXISTS idx_edges_label ON edges(label);",
		"CREATE INDEX IF NOT EXISTS idx_events_type ON events(event_type);",
		"CREATE INDEX IF NOT EXISTS idx_events_project ON events(project_id);",
		"CREATE INDEX IF NOT EXISTS idx_file_hashes_project ON file_hashes(project_id);",
		"CREATE INDEX IF NOT EXISTS idx_concepts_freq ON concepts(frequency DESC);",
		"CREATE INDEX IF NOT EXISTS idx_unresolved_project ON unresolved_calls(project_id);",
	}

	for _, idxQuery := range indexes {
		if _, err := g.db.Exec(idxQuery); err != nil {
			return fmt.Errorf("failed to create index (%s): %w", idxQuery, err)
		}
	}

	if err := g.ensureNodeFTS(); err != nil {
		return err
	}
	if _, err := g.db.Exec(`INSERT OR REPLACE INTO schema_meta (key, value) VALUES ('schema_version', '3');`); err != nil {
		return fmt.Errorf("failed to store schema version: %w", err)
	}

	if err := g.purgeCallPlaceholders(); err != nil {
		return err
	}

	return nil
}

// codeIndexLimit caps how much source each symbol contributes to the
// trigram index. The prefix covers a typical method body. The tail of a
// very long function stays in nodes.code and is still readable by id.
const codeIndexLimit = 2000

// ensureNodeFTS builds a trigram full-text index over symbol names,
// docstrings, and the first codeIndexLimit characters of source. The FTS
// table stores that prefix itself so method bodies stay searchable after a
// rebuild. Triggers keep it aligned with nodes, including INSERT OR REPLACE.
// A legacy name/docstring index is dropped and rebuilt.
func (g *GraphDB) ensureNodeFTS() error {
	var createSQL string
	err := g.db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'nodes_fts'`).Scan(&createSQL)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("failed to read nodes_fts schema: %w", err)
	}
	if err == nil && (!strings.Contains(createSQL, "code_index") || strings.Contains(createSQL, "content=")) {
		if err := g.dropNodeFTS(); err != nil {
			return err
		}
	}

	if _, err := g.db.Exec(`
		CREATE VIRTUAL TABLE IF NOT EXISTS nodes_fts USING fts5(
			name, fqn, docstring, code_index,
			tokenize='trigram'
		);`); err != nil {
		return fmt.Errorf("failed to create nodes_fts: %w", err)
	}
	triggers := []string{
		fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS nodes_fts_ai AFTER INSERT ON nodes BEGIN
			INSERT INTO nodes_fts(rowid, name, fqn, docstring, code_index)
			VALUES (new.rowid, new.name, new.fqn, new.docstring, %s);
		END;`, ftsCodePrefix("new.code")),
		`CREATE TRIGGER IF NOT EXISTS nodes_fts_ad AFTER DELETE ON nodes BEGIN
			DELETE FROM nodes_fts WHERE rowid = old.rowid;
		END;`,
		fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS nodes_fts_au AFTER UPDATE ON nodes BEGIN
			DELETE FROM nodes_fts WHERE rowid = old.rowid;
			INSERT INTO nodes_fts(rowid, name, fqn, docstring, code_index)
			VALUES (new.rowid, new.name, new.fqn, new.docstring, %s);
		END;`, ftsCodePrefix("new.code")),
	}
	for _, trigger := range triggers {
		if _, err := g.db.Exec(trigger); err != nil {
			return fmt.Errorf("failed to create nodes_fts trigger: %w", err)
		}
	}

	var nodeCount, ftsCount int
	if err := g.db.QueryRow(`SELECT COUNT(*) FROM nodes`).Scan(&nodeCount); err != nil {
		return err
	}
	if err := g.db.QueryRow(`SELECT COUNT(*) FROM nodes_fts`).Scan(&ftsCount); err != nil {
		return err
	}
	if nodeCount != ftsCount {
		if _, err := g.db.Exec(`DELETE FROM nodes_fts`); err != nil {
			return fmt.Errorf("failed to clear nodes_fts: %w", err)
		}
		if _, err := g.db.Exec(fmt.Sprintf(`
			INSERT INTO nodes_fts(rowid, name, fqn, docstring, code_index)
			SELECT rowid, name, fqn, docstring, %s FROM nodes`, ftsCodePrefix("code"))); err != nil {
			return fmt.Errorf("failed to rebuild nodes_fts: %w", err)
		}
	}
	return nil
}

func ftsCodePrefix(column string) string {
	return fmt.Sprintf("substr(ifnull(%s, ''), 1, %d)", column, codeIndexLimit)
}

func (g *GraphDB) dropNodeFTS() error {
	for _, name := range []string{"nodes_fts_ai", "nodes_fts_ad", "nodes_fts_au"} {
		if _, err := g.db.Exec("DROP TRIGGER IF EXISTS " + name); err != nil {
			return fmt.Errorf("failed to drop %s: %w", name, err)
		}
	}
	if _, err := g.db.Exec(`DROP TABLE IF EXISTS nodes_fts`); err != nil {
		return fmt.Errorf("failed to drop legacy nodes_fts: %w", err)
	}
	return nil
}

// purgeCallPlaceholders removes legacy call_* stub nodes created before calls
// were bound to real symbols. Edges cascade; vec0 rows are deleted explicitly.
func (g *GraphDB) purgeCallPlaceholders() error {
	rows, err := g.db.Query(`SELECT id FROM nodes WHERE type = 'Node' AND substr(id, 1, 5) = 'call_'`)
	if err != nil {
		return fmt.Errorf("failed to list call placeholders: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		var rowid int64
		err := g.db.QueryRow("SELECT rowid FROM vectors_meta WHERE node_id = ?", id).Scan(&rowid)
		if err == nil {
			if _, err := g.db.Exec("DELETE FROM vectors WHERE rowid = ?", rowid); err != nil {
				return fmt.Errorf("failed to delete placeholder vector: %w", err)
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	if _, err := g.db.Exec(`DELETE FROM nodes WHERE type = 'Node' AND substr(id, 1, 5) = 'call_'`); err != nil {
		return fmt.Errorf("failed to delete call placeholders: %w", err)
	}
	return nil
}

// AddNode inserts or replaces a node in the graph
func (g *GraphDB) AddNode(tx *sql.Tx, id, nodeType, name, fqn, code, docstring, projectID, filePath string, props map[string]any) error {
	propsJSON := "{}"
	if len(props) > 0 {
		data, err := json.Marshal(props)
		if err != nil {
			return fmt.Errorf("failed to marshal node properties: %w", err)
		}
		propsJSON = string(data)
	}

	query := `
	INSERT OR REPLACE INTO nodes (id, type, name, fqn, code, docstring, project_id, file_path, properties)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);
	`
	executor := g.getExecutor(tx)
	_, err := executor.Exec(query, id, nodeType, name, fqn, code, docstring, projectID, filePath, propsJSON)
	if err != nil {
		return fmt.Errorf("failed to add node: %w", err)
	}
	return nil
}

// AddEdge inserts or replaces an edge. Both endpoints must already exist.
// Missing symbols are recorded as unresolved calls instead of stub nodes.
func (g *GraphDB) AddEdge(tx *sql.Tx, source, target, label string, props map[string]any) error {
	propsJSON := "{}"
	if len(props) > 0 {
		data, err := json.Marshal(props)
		if err != nil {
			return fmt.Errorf("failed to marshal edge properties: %w", err)
		}
		propsJSON = string(data)
	}

	executor := g.getExecutor(tx)
	query := `
	INSERT OR REPLACE INTO edges (source, target, label, properties)
	VALUES (?, ?, ?, ?);
	`
	_, err := executor.Exec(query, source, target, label, propsJSON)
	if err != nil {
		return fmt.Errorf("failed to add edge: %w", err)
	}
	return nil
}

// GetNode retrieves a node by its ID
func (g *GraphDB) GetNode(id string) (map[string]any, error) {
	query := "SELECT id, type, name, fqn, code, docstring, project_id, properties FROM nodes WHERE id = ?;"
	row := g.db.QueryRow(query, id)

	var nodeId, nodeType, name, fqn, code, docstring, projectID, propsJSON string
	err := row.Scan(&nodeId, &nodeType, &name, &fqn, &code, &docstring, &projectID, &propsJSON)
	if err == sql.ErrNoRows {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("failed to scan node: %w", err)
	}

	var props map[string]any
	if err := json.Unmarshal([]byte(propsJSON), &props); err != nil {
		props = make(map[string]any)
	}

	node := map[string]any{
		"id":         nodeId,
		"type":       nodeType,
		"name":       name,
		"fqn":        fqn,
		"code":       code,
		"docstring":  docstring,
		"project_id": projectID,
		"properties": props,
	}
	return node, nil
}

// GetEdges retrieves all incoming and outgoing edges for a node
func (g *GraphDB) GetEdges(nodeID string) ([]map[string]any, error) {
	// Query Outgoing Edges
	outQuery := `
	SELECT e.source, e.target, e.label, e.properties, n.type, n.name, n.fqn
	FROM edges e
	JOIN nodes n ON e.target = n.id
	WHERE e.source = ?;`

	rows, err := g.db.Query(outQuery, nodeID)
	if err != nil {
		return nil, fmt.Errorf("failed to query outgoing edges: %w", err)
	}
	defer rows.Close()

	var edges []map[string]any
	for rows.Next() {
		var src, dst, label, propsJSON, targetType, targetName, targetFqn string
		if err := rows.Scan(&src, &dst, &label, &propsJSON, &targetType, &targetName, &targetFqn); err != nil {
			return nil, err
		}
		var props map[string]any
		if err := json.Unmarshal([]byte(propsJSON), &props); err != nil {
			props = make(map[string]any)
		}

		edges = append(edges, map[string]any{
			"source":      src,
			"target":      dst,
			"label":       label,
			"properties":  props,
			"direction":   "outgoing",
			"target_info": map[string]string{"type": targetType, "name": targetName, "fqn": targetFqn},
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating outgoing edges: %w", err)
	}

	// Query Incoming Edges
	inQuery := `
	SELECT e.source, e.target, e.label, e.properties, n.type, n.name, n.fqn
	FROM edges e
	JOIN nodes n ON e.source = n.id
	WHERE e.target = ?;`

	inRows, err := g.db.Query(inQuery, nodeID)
	if err != nil {
		return nil, fmt.Errorf("failed to query incoming edges: %w", err)
	}
	defer inRows.Close()

	for inRows.Next() {
		var src, dst, label, propsJSON, sourceType, sourceName, sourceFqn string
		if err := inRows.Scan(&src, &dst, &label, &propsJSON, &sourceType, &sourceName, &sourceFqn); err != nil {
			return nil, err
		}
		var props map[string]any
		if err := json.Unmarshal([]byte(propsJSON), &props); err != nil {
			props = make(map[string]any)
		}

		edges = append(edges, map[string]any{
			"source":      src,
			"target":      dst,
			"label":       label,
			"properties":  props,
			"direction":   "incoming",
			"source_info": map[string]string{"type": sourceType, "name": sourceName, "fqn": sourceFqn},
		})
	}
	if err := inRows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating incoming edges: %w", err)
	}

	return edges, nil
}

// GetNeighbors lists adjacent nodes for ranking and path search.
// Source text stays on the node row; callers that need it load the node by id.
func (g *GraphDB) GetNeighbors(nodeID string) ([]map[string]any, error) {
	query := `
	SELECT n.id, n.type, n.name, n.fqn, e.label
	FROM edges e
	JOIN nodes n ON e.target = n.id
	WHERE e.source = ? AND n.id != ?
	UNION
	SELECT n.id, n.type, n.name, n.fqn, e.label
	FROM edges e
	JOIN nodes n ON e.source = n.id
	WHERE e.target = ? AND n.id != ?;
	`
	rows, err := g.db.Query(query, nodeID, nodeID, nodeID, nodeID)
	if err != nil {
		return nil, fmt.Errorf("failed to query neighbors: %w", err)
	}
	defer rows.Close()

	var neighbors []map[string]any
	for rows.Next() {
		var id, nodeType, name, fqn, relation string
		if err := rows.Scan(&id, &nodeType, &name, &fqn, &relation); err != nil {
			return nil, err
		}

		neighbors = append(neighbors, map[string]any{
			"id":       id,
			"type":     nodeType,
			"name":     name,
			"fqn":      fqn,
			"relation": relation,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating neighbors: %w", err)
	}

	return neighbors, nil
}

// QueryNodes searches nodes by type, name, or FQN suffix matching
// SearchMultiHop executes a multi-hop graph query starting from the given nodeID,
// traversing CONTAINS and CALLS edges up to maxDepth.
func (g *GraphDB) SearchMultiHop(nodeID string, maxDepth int) ([]map[string]any, error) {
	query := `
	WITH RECURSIVE
	search_graph(id, type, name, fqn, depth, path) AS (
		SELECT id, type, name, fqn, 0, id
		FROM nodes
		WHERE id = ?
		UNION ALL
		SELECT n.id, n.type, n.name, n.fqn, sg.depth + 1, sg.path || '->' || n.id
		FROM edges e
		JOIN nodes n ON e.target = n.id
		JOIN search_graph sg ON e.source = sg.id
		WHERE sg.depth < ?
		  AND (e.label = 'CONTAINS' OR e.label = 'CALLS')
		  AND instr('->' || sg.path || '->', '->' || n.id || '->') = 0
	)
	SELECT id, type, name, fqn, MIN(depth) as min_depth
	FROM search_graph
	GROUP BY id, type, name, fqn
	ORDER BY min_depth;
	`

	rows, err := g.db.Query(query, nodeID, maxDepth)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []map[string]any
	for rows.Next() {
		var id, nodeType, name, fqn string
		var minDepth int
		if err := rows.Scan(&id, &nodeType, &name, &fqn, &minDepth); err != nil {
			continue
		}
		results = append(results, map[string]any{
			"id":    id,
			"type":  nodeType,
			"name":  name,
			"fqn":   fqn,
			"depth": minDepth,
		})
	}
	return results, nil
}

func (g *GraphDB) QueryNodes(nodeType, nameFilter, projectID string) ([]map[string]any, error) {
	sqlQuery := `SELECT id, type, name, fqn, code, docstring, project_id, properties FROM nodes WHERE 1=1`
	var args []any

	if nodeType != "" {
		sqlQuery += " AND type = ?"
		args = append(args, nodeType)
	}
	if nameFilter != "" {
		sqlQuery += " AND (name = ? OR fqn LIKE ?)"
		args = append(args, nameFilter, "%"+nameFilter)
	}
	if projectID != "" {
		sqlQuery += " AND project_id = ?"
		args = append(args, projectID)
	}

	rows, err := g.db.Query(sqlQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query nodes: %w", err)
	}
	defer rows.Close()

	var nodes []map[string]any
	for rows.Next() {
		var id, nType, name, fqn, code, docstring, pID, propsJSON string
		if err := rows.Scan(&id, &nType, &name, &fqn, &code, &docstring, &pID, &propsJSON); err != nil {
			return nil, err
		}
		var props map[string]any
		if err := json.Unmarshal([]byte(propsJSON), &props); err != nil {
			props = make(map[string]any)
		}

		nodes = append(nodes, map[string]any{
			"id":         id,
			"type":       nType,
			"name":       name,
			"fqn":        fqn,
			"code":       code,
			"docstring":  docstring,
			"project_id": pID,
			"properties": props,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating nodes: %w", err)
	}
	return nodes, nil
}

// ClearProject deletes all nodes and edges belonging to a project
func (g *GraphDB) ClearProject(projectID string) error {
	return g.RunInTransaction(func(tx *sql.Tx) error {
		// Get all node IDs in the project
		queryIDs := "SELECT id FROM nodes WHERE project_id = ?;"
		rows, err := tx.Query(queryIDs, projectID)
		if err != nil {
			return err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err == nil {
				ids = append(ids, id)
			}
		}
		rows.Close()

		if len(ids) > 0 {
			// Manually delete vectors from the virtual table
			for _, id := range ids {
				var rowid int64
				err := tx.QueryRow("SELECT rowid FROM vectors_meta WHERE node_id = ?", id).Scan(&rowid)
				if err == nil {
					tx.Exec("DELETE FROM vectors WHERE rowid = ?", rowid)
				}
			}

			// Delete metadata records from vectors_meta
			placeholders := make([]string, len(ids))
			args := make([]any, len(ids))
			for i, id := range ids {
				placeholders[i] = "?"
				args[i] = id
			}
			queryDeleteMeta := fmt.Sprintf("DELETE FROM vectors_meta WHERE node_id IN (%s);", strings.Join(placeholders, ","))
			if _, err := tx.Exec(queryDeleteMeta, args...); err != nil {
				return err
			}
		}

		// Delete edges
		deleteEdges := `
		DELETE FROM edges 
		WHERE source IN (SELECT id FROM nodes WHERE project_id = ?)
		   OR target IN (SELECT id FROM nodes WHERE project_id = ?);
		`
		if _, err := tx.Exec(deleteEdges, projectID, projectID); err != nil {
			return fmt.Errorf("failed to clear project edges: %w", err)
		}

		// Delete nodes
		deleteNodes := "DELETE FROM nodes WHERE project_id = ?;"
		if _, err := tx.Exec(deleteNodes, projectID); err != nil {
			return fmt.Errorf("failed to clear project nodes: %w", err)
		}

		return nil
	})
}

// SaveVector inserts or replaces a vector embedding associated with a node
func (g *GraphDB) SaveVector(tx *sql.Tx, nodeID string, embedding []byte, metadata map[string]any) error {
	metadataJSON := "{}"
	if len(metadata) > 0 {
		data, err := json.Marshal(metadata)
		if err != nil {
			return fmt.Errorf("failed to marshal vector metadata: %w", err)
		}
		metadataJSON = string(data)
	}

	// Insert or replace metadata to get a rowid
	queryMeta := `INSERT INTO vectors_meta (node_id, metadata) VALUES (?, ?) ON CONFLICT(node_id) DO UPDATE SET metadata=excluded.metadata RETURNING rowid;`
	executor := g.getExecutor(tx)
	var rowid int64
	err := executor.QueryRow(queryMeta, nodeID, metadataJSON).Scan(&rowid)
	if err != nil {
		return fmt.Errorf("failed to save vector meta and get rowid: %w", err)
	}

	// Insert vector using rowid
	executor.Exec(`DELETE FROM vectors WHERE rowid = ?`, rowid)
	query := `INSERT INTO vectors (rowid, embedding) VALUES (?, ?);`
	_, err = executor.Exec(query, rowid, embedding)
	if err != nil {
		return fmt.Errorf("failed to save vector: %w", err)
	}
	return nil
}

type VectorRecord struct {
	NodeID    string
	Embedding []byte
	Metadata  map[string]any
}

// LoadVectors loads all stored vectors from the database
func (g *GraphDB) LoadVectors() ([]VectorRecord, error) {
	query := "SELECT m.node_id, v.embedding, m.metadata FROM vectors v JOIN vectors_meta m ON v.rowid = m.rowid;"
	rows, err := g.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to load vectors: %w", err)
	}
	defer rows.Close()

	var list []VectorRecord
	for rows.Next() {
		var nodeID string
		var embedding []byte
		var metadataJSON string
		if err := rows.Scan(&nodeID, &embedding, &metadataJSON); err != nil {
			return nil, err
		}

		var metadata map[string]any
		if err := json.Unmarshal([]byte(metadataJSON), &metadata); err != nil {
			metadata = make(map[string]any)
		}

		list = append(list, VectorRecord{
			NodeID:    nodeID,
			Embedding: embedding,
			Metadata:  metadata,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating vectors: %w", err)
	}
	return list, nil
}

// SaveEvent inserts a new event (preference, error fix, session log) in the database
func (g *GraphDB) SaveEvent(tx *sql.Tx, id, eventType, summary, details string, timestamp int64, embedding []byte, status string, importance int, projectID string) error {
	if projectID == "" {
		projectID = "global"
	}
	query := `
	INSERT OR REPLACE INTO events (id, timestamp, event_type, summary, details, embedding, status, importance, last_accessed, project_id)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);
	`
	executor := g.getExecutor(tx)
	_, err := executor.Exec(query, id, timestamp, eventType, summary, details, embedding, status, importance, timestamp, projectID)
	if err != nil {
		return fmt.Errorf("failed to save event: %w", err)
	}
	return nil
}

// UpdateEventAccess updates the last_accessed timestamp for given event IDs
func (g *GraphDB) UpdateEventAccess(tx *sql.Tx, ids []string, timestamp int64) error {
	if len(ids) == 0 {
		return nil
	}

	placeholders := make([]string, len(ids))
	args := make([]any, 0, len(ids)+1)
	args = append(args, timestamp)
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}

	query := fmt.Sprintf(`UPDATE events SET last_accessed = ? WHERE id IN (%s);`, strings.Join(placeholders, ","))
	executor := g.getExecutor(tx)

	if _, err := executor.Exec(query, args...); err != nil {
		return fmt.Errorf("failed to batch update last_accessed: %w", err)
	}
	return nil
}

// RunTieringGC scans active events and reduces importance for those not accessed recently.
// If importance drops below 0, they are archived.
func (g *GraphDB) RunTieringGC(decayIntervalSeconds int64, currentTime int64) error {
	return g.RunInTransaction(func(tx *sql.Tx) error {
		// Find events that haven't been accessed within the decay interval
		cutoffTime := currentTime - decayIntervalSeconds

		query := `
		SELECT id, importance
		FROM events
		WHERE status = 'active' AND last_accessed < ?;
		`

		rows, err := tx.Query(query, cutoffTime)
		if err != nil {
			return fmt.Errorf("failed to query stale events: %w", err)
		}

		type eventUpdate struct {
			id            string
			newImportance int
		}
		var toUpdate []eventUpdate

		for rows.Next() {
			var id string
			var importance int
			if err := rows.Scan(&id, &importance); err != nil {
				continue
			}
			toUpdate = append(toUpdate, eventUpdate{id: id, newImportance: importance - 1})
		}
		rows.Close()

		if len(toUpdate) == 0 {
			return nil
		}

		// Process updates
		var toArchive []string

		updateImportanceQuery := `UPDATE events SET importance = ?, last_accessed = ? WHERE id = ?;`
		stmt, err := tx.Prepare(updateImportanceQuery)
		if err != nil {
			return fmt.Errorf("failed to prepare update statement: %w", err)
		}
		defer stmt.Close()

		for _, eu := range toUpdate {
			if eu.newImportance < 0 {
				toArchive = append(toArchive, eu.id)
			} else {
				// Update importance and reset last_accessed so it doesn't decay again immediately
				if _, err := stmt.Exec(eu.newImportance, currentTime, eu.id); err != nil {
					return fmt.Errorf("failed to update event importance: %w", err)
				}
			}
		}

		if len(toArchive) > 0 {
			// Reuse the ArchiveEvents method logic directly within this transaction
			placeholders := make([]string, len(toArchive))
			args := make([]any, len(toArchive))
			for i, id := range toArchive {
				placeholders[i] = "?"
				args[i] = id
			}

			archiveQuery := fmt.Sprintf(`UPDATE events SET status = 'archived' WHERE id IN (%s);`, strings.Join(placeholders, ","))
			if _, err := tx.Exec(archiveQuery, args...); err != nil {
				return fmt.Errorf("failed to batch archive events during GC: %w", err)
			}
			fmt.Fprintf(os.Stderr, "[DB] Tiering GC archived %d stale events.\n", len(toArchive))
		}

		return nil
	})
}

// GetAllActiveEvents retrieves all events where status = 'active'
func (g *GraphDB) GetAllActiveEvents(projectID string) ([]map[string]any, error) {
	if projectID == "" {
		projectID = "global"
	}
	query := `
	SELECT id, timestamp, event_type, summary, details, embedding, status, importance, last_accessed
	FROM events
	WHERE status = 'active' AND (project_id = ? OR project_id = 'global')
	ORDER BY importance DESC, timestamp DESC;
	`
	rows, err := g.db.Query(query, projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to get active events: %w", err)
	}
	defer rows.Close()

	var events []map[string]any
	for rows.Next() {
		var id, eventType, summary, details, status string
		var timestamp, lastAccessed int64
		var embedding []byte
		var importance int
		if err := rows.Scan(&id, &timestamp, &eventType, &summary, &details, &embedding, &status, &importance, &lastAccessed); err != nil {
			return nil, fmt.Errorf("failed to scan event: %w", err)
		}
		events = append(events, map[string]any{
			"id":            id,
			"timestamp":     timestamp,
			"event_type":    eventType,
			"summary":       summary,
			"details":       details,
			"embedding":     embedding,
			"status":        status,
			"importance":    importance,
			"last_accessed": lastAccessed,
		})
	}
	return events, nil
}

// GetStaleEvents retrieves events that have not been accessed since the given timestamp threshold
func (g *GraphDB) GetStaleEvents(projectID string, accessedBefore int64) ([]map[string]any, error) {
	query := `
		SELECT id, event_type, summary, details, timestamp, last_accessed, importance
		FROM events
		WHERE status = 'active' AND last_accessed < ?
	`
	args := []any{accessedBefore}

	if projectID != "" {
		query += " AND project_id = ?"
		args = append(args, projectID)
	}

	rows, err := g.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []map[string]any
	for rows.Next() {
		var id, eventType, summary, details string
		var timestamp, lastAccessed int64
		var importance int
		if err := rows.Scan(&id, &eventType, &summary, &details, &timestamp, &lastAccessed, &importance); err != nil {
			return nil, err
		}
		events = append(events, map[string]any{
			"id":            id,
			"event_type":    eventType,
			"summary":       summary,
			"details":       details,
			"timestamp":     timestamp,
			"last_accessed": lastAccessed,
			"importance":    importance,
		})
	}
	return events, nil
}

// ArchiveEvents updates the status of the given event IDs to 'archived'
func (g *GraphDB) ArchiveEvents(tx *sql.Tx, ids []string) error {
	if len(ids) == 0 {
		return nil
	}

	// Use an IN clause for efficient batch updating
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}

	query := fmt.Sprintf(`UPDATE events SET status = 'archived' WHERE id IN (%s);`, strings.Join(placeholders, ","))
	executor := g.getExecutor(tx)

	if _, err := executor.Exec(query, args...); err != nil {
		return fmt.Errorf("failed to batch archive events: %w", err)
	}

	return nil
}

// GetRecentActiveEvents retrieves the most recent active events from the database
func (g *GraphDB) GetRecentActiveEvents(limit int, projectID string) ([]map[string]any, error) {
	if projectID == "" {
		projectID = "global"
	}
	query := `
	SELECT id, timestamp, event_type, summary, details, embedding, importance, last_accessed
	FROM events
	WHERE status = 'active' AND (project_id = ? OR project_id = 'global')
	ORDER BY importance DESC, timestamp DESC
	LIMIT ?;
	`
	rows, err := g.db.Query(query, projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query recent active events: %w", err)
	}
	defer rows.Close()

	var events []map[string]any
	for rows.Next() {
		var id, eventType, summary, details string
		var timestamp, lastAccessed int64
		var embedding []byte
		var importance int
		if err := rows.Scan(&id, &timestamp, &eventType, &summary, &details, &embedding, &importance, &lastAccessed); err != nil {
			return nil, err
		}

		events = append(events, map[string]any{
			"id":            id,
			"timestamp":     timestamp,
			"event_type":    eventType,
			"summary":       summary,
			"details":       details,
			"embedding":     embedding,
			"importance":    importance,
			"last_accessed": lastAccessed,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating active events: %w", err)
	}
	return events, nil
}

// GetRecentEvents retrieves the most recent events from the database
func (g *GraphDB) GetRecentEvents(limit int, projectID string) ([]map[string]any, error) {
	if projectID == "" {
		projectID = "global"
	}
	query := `
	SELECT id, timestamp, event_type, summary, details, embedding, importance, last_accessed
	FROM events
	WHERE (project_id = ? OR project_id = 'global')
	ORDER BY importance DESC, timestamp DESC
	LIMIT ?;
	`
	rows, err := g.db.Query(query, projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query recent events: %w", err)
	}
	defer rows.Close()

	var events []map[string]any
	for rows.Next() {
		var id, eventType, summary, details string
		var timestamp, lastAccessed int64
		var embedding []byte
		var importance int
		if err := rows.Scan(&id, &timestamp, &eventType, &summary, &details, &embedding, &importance, &lastAccessed); err != nil {
			return nil, err
		}

		events = append(events, map[string]any{
			"id":            id,
			"timestamp":     timestamp,
			"event_type":    eventType,
			"summary":       summary,
			"details":       details,
			"embedding":     embedding,
			"importance":    importance,
			"last_accessed": lastAccessed,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating events: %w", err)
	}
	return events, nil
}

// SaveConcepts inserts or updates concept frequencies and links them to an event
func (g *GraphDB) SaveConcepts(tx *sql.Tx, eventID string, concepts []string) error {
	if len(concepts) == 0 {
		return nil
	}

	executor := g.getExecutor(tx)

	for _, concept := range concepts {
		// Update concept frequency
		upsertConceptQuery := `
		INSERT INTO concepts (name, frequency)
		VALUES (?, 1)
		ON CONFLICT(name) DO UPDATE SET frequency = frequency + 1;
		`
		if _, err := executor.Exec(upsertConceptQuery, concept); err != nil {
			return fmt.Errorf("failed to upsert concept: %w", err)
		}

		// Link to event if eventID is provided
		if eventID != "" {
			linkQuery := `
			INSERT OR IGNORE INTO event_concepts (event_id, concept_name)
			VALUES (?, ?);
			`
			if _, err := executor.Exec(linkQuery, eventID, concept); err != nil {
				return fmt.Errorf("failed to link concept to event: %w", err)
			}
		}
	}
	return nil
}

// GetTopConcepts retrieves the highest frequency concepts
func (g *GraphDB) GetTopConcepts(limit int) ([]map[string]any, error) {
	query := `
	SELECT name, frequency
	FROM concepts
	ORDER BY frequency DESC
	LIMIT ?;
	`
	rows, err := g.db.Query(query, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query top concepts: %w", err)
	}
	defer rows.Close()

	var concepts []map[string]any
	for rows.Next() {
		var name string
		var frequency int
		if err := rows.Scan(&name, &frequency); err != nil {
			return nil, err
		}
		concepts = append(concepts, map[string]any{
			"name":      name,
			"frequency": frequency,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating concepts: %w", err)
	}
	return concepts, nil
}

func (g *GraphDB) Close() error {
	return g.db.Close()
}

// QueryPattern parses a simple DSL like `Method(A) -> CALLS -> Method(B)` and executes the query
// It uses a Recursive CTE to traverse multiple hops if maxDepth > 1, falling back to a direct JOIN for single hop queries.
func (g *GraphDB) QueryPattern(pattern string, maxDepth int) ([]map[string]any, error) {
	// Simple parsing
	// Format: Node(Type) -> EdgeLabel -> Node(Type)
	// Example: Method(*) -> CALLS -> Method(*)
	// Example: Class(User) -> CONTAINS -> Method(*)

	pattern = strings.TrimSpace(pattern)

	parts := strings.Split(pattern, "->")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid pattern format, expected Node(Type) -> LABEL -> Node(Type)")
	}

	sourceStr := strings.TrimSpace(parts[0])
	edgeStr := strings.TrimSpace(parts[1])
	targetStr := strings.TrimSpace(parts[2])

	parseNode := func(s string) (string, string) {
		s = strings.TrimSuffix(s, ")")
		parts := strings.SplitN(s, "(", 2)
		if len(parts) != 2 {
			return "", ""
		}
		return parts[0], parts[1]
	}

	srcType, srcName := parseNode(sourceStr)
	targetType, targetName := parseNode(targetStr)

	if srcType == "" || targetType == "" {
		return nil, fmt.Errorf("invalid node format, expected NodeType(Name)")
	}

	var args []any

	// Create conditions for source node
	srcConds := []string{"1=1"}
	if srcType != "*" && srcType != "" {
		srcConds = append(srcConds, "type = ?")
		args = append(args, srcType)
	}
	if srcName != "*" && srcName != "" {
		srcConds = append(srcConds, "name LIKE ?")
		args = append(args, "%"+srcName+"%")
	}

	// Base edge condition for recursion
	edgeConds := []string{"1=1"}
	if edgeStr != "*" && edgeStr != "" {
		edgeConds = append(edgeConds, "label = ?")
		args = append(args, edgeStr)
	}

	// Target node condition at end of path
	tgtConds := []string{"1=1"}
	if targetType != "*" && targetType != "" {
		tgtConds = append(tgtConds, "n.type = ?")
		args = append(args, targetType)
	}
	if targetName != "*" && targetName != "" {
		tgtConds = append(tgtConds, "n.name LIKE ?")
		args = append(args, "%"+targetName+"%")
	}

	// Make sure we pass depth parameter
	args = append(args, maxDepth)

	query := fmt.Sprintf(`
	WITH RECURSIVE
	search_graph(source_id, source_type, source_name, source_fqn,
				 edge_label, edge_properties,
				 target_id, depth, path) AS (
		SELECT
			n1.id, n1.type, n1.name, n1.fqn,
			e.label, e.properties,
			e.target, 1, n1.id || '->' || e.target
		FROM edges e
		JOIN nodes n1 ON e.source = n1.id
		WHERE (%s) AND (%s)

		UNION ALL

		SELECT
			sg.source_id, sg.source_type, sg.source_name, sg.source_fqn,
			e.label, e.properties,
			e.target, sg.depth + 1, sg.path || '->' || e.target
		FROM edges e
		JOIN search_graph sg ON e.source = sg.target_id
		WHERE sg.depth < ?
		  AND (%s)
		  AND instr('->' || sg.path || '->', '->' || e.target || '->') = 0
	)
	SELECT
		sg.source_id, sg.source_type, sg.source_name, sg.source_fqn,
		sg.edge_label, sg.edge_properties,
		n.id, n.type, n.name, n.fqn, sg.depth
	FROM search_graph sg
	JOIN nodes n ON sg.target_id = n.id
	WHERE (%s)
	ORDER BY sg.depth, sg.source_id, n.id LIMIT 100
	`, strings.Join(srcConds, " AND "), strings.Join(edgeConds, " AND "), strings.Join(edgeConds, " AND "), strings.Join(tgtConds, " AND "))

	// Duplicate the edge condition arguments for the UNION ALL part
	var expandedArgs []any
	if srcType != "*" && srcType != "" {
		expandedArgs = append(expandedArgs, srcType)
	}
	if srcName != "*" && srcName != "" {
		expandedArgs = append(expandedArgs, "%"+srcName+"%")
	}
	if edgeStr != "*" && edgeStr != "" {
		expandedArgs = append(expandedArgs, edgeStr)
	}
	expandedArgs = append(expandedArgs, maxDepth)
	if edgeStr != "*" && edgeStr != "" {
		expandedArgs = append(expandedArgs, edgeStr)
	}
	if targetType != "*" && targetType != "" {
		expandedArgs = append(expandedArgs, targetType)
	}
	if targetName != "*" && targetName != "" {
		expandedArgs = append(expandedArgs, "%"+targetName+"%")
	}

	rows, err := g.db.Query(query, expandedArgs...)
	if err != nil {
		return nil, fmt.Errorf("failed to query pattern: %w", err)
	}
	defer rows.Close()

	var results []map[string]any
	for rows.Next() {
		var srcId, srcType, srcName, srcFqn, edgeLabel, edgeProps, targetId, targetType, targetName, targetFqn string
		var depth int
		if err := rows.Scan(&srcId, &srcType, &srcName, &srcFqn, &edgeLabel, &edgeProps, &targetId, &targetType, &targetName, &targetFqn, &depth); err != nil {
			return nil, err
		}

		res := map[string]any{
			"source": map[string]any{
				"id": srcId, "type": srcType, "name": srcName, "fqn": srcFqn,
			},
			"edge": map[string]any{
				"label": edgeLabel, "properties": edgeProps,
			},
			"target": map[string]any{
				"id": targetId, "type": targetType, "name": targetName, "fqn": targetFqn,
			},
			"depth": depth,
		}
		results = append(results, res)
	}
	return results, nil
}

// FileHashRecord represents a record in the file_hashes table
type FileHashRecord struct {
	FilePath    string
	ProjectID   string
	Hash        string
	LastIndexed int64
}

// GetFileHash retrieves the hash for a file path in a project
func (g *GraphDB) GetFileHash(filePath, projectID string) (string, error) {
	query := "SELECT hash FROM file_hashes WHERE file_path = ? AND project_id = ?;"
	var hash string
	err := g.db.QueryRow(query, filePath, projectID).Scan(&hash)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return hash, err
}

// SaveFileHash saves or updates a file hash
func (g *GraphDB) SaveFileHash(tx *sql.Tx, filePath, projectID, hash string, timestamp int64) error {
	query := `
	INSERT OR REPLACE INTO file_hashes (file_path, project_id, hash, last_indexed)
	VALUES (?, ?, ?, ?);
	`
	executor := g.getExecutor(tx)
	_, err := executor.Exec(query, filePath, projectID, hash, timestamp)
	return err
}

// DeleteFileHash deletes a file hash record
func (g *GraphDB) DeleteFileHash(tx *sql.Tx, filePath, projectID string) error {
	query := "DELETE FROM file_hashes WHERE file_path = ? AND project_id = ?;"
	executor := g.getExecutor(tx)
	_, err := executor.Exec(query, filePath, projectID)
	return err
}

// DeleteNodesByFile deletes all nodes and their cascaded relations/vectors for a specific file in a project
func (g *GraphDB) DeleteNodesByFile(tx *sql.Tx, filePath, projectID string) error {
	executor := g.getExecutor(tx)

	// Get all node IDs in the file to clean up virtual table vectors manually
	queryIDs := "SELECT id FROM nodes WHERE file_path = ? AND project_id = ?;"
	rows, err := executor.Query(queryIDs, filePath, projectID)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()

	if len(ids) > 0 {
		// Manually delete vectors from the virtual table
		for _, id := range ids {
			var rowid int64
			err := executor.QueryRow("SELECT rowid FROM vectors_meta WHERE node_id = ?", id).Scan(&rowid)
			if err == nil {
				executor.Exec("DELETE FROM vectors WHERE rowid = ?", rowid)
			}
		}

		// Delete metadata records from vectors_meta
		placeholders := make([]string, len(ids))
		args := make([]any, len(ids))
		for i, id := range ids {
			placeholders[i] = "?"
			args[i] = id
		}
		queryDeleteMeta := fmt.Sprintf("DELETE FROM vectors_meta WHERE node_id IN (%s);", strings.Join(placeholders, ","))
		if _, err := executor.Exec(queryDeleteMeta, args...); err != nil {
			return err
		}
	}

	query := "DELETE FROM nodes WHERE file_path = ? AND project_id = ?;"
	_, err = executor.Exec(query, filePath, projectID)
	return err
}

// GetProjectFiles returns all indexed file paths for a project
func (g *GraphDB) GetProjectFiles(projectID string) (map[string]string, error) {
	query := "SELECT file_path, hash FROM file_hashes WHERE project_id = ?;"
	rows, err := g.db.Query(query, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	files := make(map[string]string)
	for rows.Next() {
		var filePath, hash string
		if err := rows.Scan(&filePath, &hash); err != nil {
			return nil, err
		}
		files[filePath] = hash
	}
	return files, nil
}

// GetConfigMeta retrieves a configuration metadata value by key
func (g *GraphDB) GetConfigMeta(key string) (string, error) {
	query := "SELECT value FROM config_meta WHERE key = ?;"
	var value string
	err := g.db.QueryRow(query, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

// SaveConfigMeta saves or updates a configuration metadata key-value pair
func (g *GraphDB) SaveConfigMeta(tx *sql.Tx, key, value string) error {
	query := "INSERT OR REPLACE INTO config_meta (key, value) VALUES (?, ?);"
	executor := g.getExecutor(tx)
	_, err := executor.Exec(query, key, value)
	return err
}

// EdgeRecord is a persisted graph edge captured across a reindex.
type EdgeRecord struct {
	Source     string
	Target     string
	Label      string
	Properties string
}

// SnapshotIncomingEdges returns edges that point at nodes in filePath from
// nodes stored in other files. Reindexing deletes the file's nodes and would
// otherwise drop these edges via ON DELETE CASCADE.
func (g *GraphDB) SnapshotIncomingEdges(tx *sql.Tx, filePath, projectID string) ([]EdgeRecord, error) {
	executor := g.getExecutor(tx)
	query := `
	SELECT e.source, e.target, e.label, e.properties
	FROM edges e
	JOIN nodes tgt ON e.target = tgt.id
	WHERE tgt.file_path = ? AND tgt.project_id = ?
	  AND e.source NOT IN (
		SELECT id FROM nodes WHERE file_path = ? AND project_id = ?
	  );`
	rows, err := executor.Query(query, filePath, projectID, filePath, projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to snapshot incoming edges: %w", err)
	}
	defer rows.Close()

	var edges []EdgeRecord
	for rows.Next() {
		var edge EdgeRecord
		if err := rows.Scan(&edge.Source, &edge.Target, &edge.Label, &edge.Properties); err != nil {
			return nil, err
		}
		edges = append(edges, edge)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return edges, nil
}

// RestoreEdges reinserts edges whose endpoints still exist.
func (g *GraphDB) RestoreEdges(tx *sql.Tx, edges []EdgeRecord) error {
	if len(edges) == 0 {
		return nil
	}
	executor := g.getExecutor(tx)
	for _, edge := range edges {
		var n int
		if err := executor.QueryRow(`SELECT COUNT(*) FROM nodes WHERE id IN (?, ?)`, edge.Source, edge.Target).Scan(&n); err != nil {
			return err
		}
		if n != 2 {
			continue
		}
		props := edge.Properties
		if props == "" {
			props = "{}"
		}
		if _, err := executor.Exec(
			`INSERT OR REPLACE INTO edges (source, target, label, properties) VALUES (?, ?, ?, ?)`,
			edge.Source, edge.Target, edge.Label, props,
		); err != nil {
			return fmt.Errorf("failed to restore edge: %w", err)
		}
	}
	return nil
}

// SaveUnresolvedCall records a call that could not be bound to a symbol.
func (g *GraphDB) SaveUnresolvedCall(tx *sql.Tx, source, callee, projectID string) error {
	if source == "" || callee == "" {
		return nil
	}
	executor := g.getExecutor(tx)
	_, err := executor.Exec(
		`INSERT OR IGNORE INTO unresolved_calls (source, callee, project_id) VALUES (?, ?, ?)`,
		source, callee, projectID,
	)
	if err != nil {
		return fmt.Errorf("failed to save unresolved call: %w", err)
	}
	return nil
}

// crossFileCallDenylist skips names that collide across a codebase and would
// bind unrelated call sites when they happen to be unique in one project.
var crossFileCallDenylist = map[string]bool{
	"error": true, "string": true, "main": true, "init": true,
	"get": true, "set": true, "run": true, "close": true,
	"read": true, "write": true, "len": true, "cap": true,
	"make": true, "append": true, "println": true, "printf": true,
	"sprintf": true, "fatal": true, "fatalf": true, "log": true,
	"print": true, "format": true, "errorf": true,
}

// ResolveProjectCalls binds unresolved calls to a single method or class in
// the same project. Ambiguous names stay unresolved.
func (g *GraphDB) ResolveProjectCalls(tx *sql.Tx, projectID string) (int, error) {
	executor := g.getExecutor(tx)
	rows, err := executor.Query(
		`SELECT id, name, fqn FROM nodes WHERE project_id = ? AND type IN ('Method', 'Class')`,
		projectID,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to load symbols: %w", err)
	}
	byName := make(map[string][]string)
	type symbol struct{ id, fqn string }
	var symbols []symbol
	for rows.Next() {
		var id, name, fqn string
		if err := rows.Scan(&id, &name, &fqn); err != nil {
			rows.Close()
			return 0, err
		}
		byName[name] = append(byName[name], id)
		symbols = append(symbols, symbol{id, fqn})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	callRows, err := executor.Query(
		`SELECT source, callee FROM unresolved_calls WHERE project_id = ?`,
		projectID,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to load unresolved calls: %w", err)
	}
	type pending struct{ source, callee string }
	var pendingCalls []pending
	for callRows.Next() {
		var source, callee string
		if err := callRows.Scan(&source, &callee); err != nil {
			callRows.Close()
			return 0, err
		}
		pendingCalls = append(pendingCalls, pending{source, callee})
	}
	callRows.Close()
	if err := callRows.Err(); err != nil {
		return 0, err
	}

	resolved := 0
	for _, call := range pendingCalls {
		simple := call.callee
		if i := strings.LastIndex(simple, "."); i >= 0 {
			simple = simple[i+1:]
		}
		if crossFileCallDenylist[strings.ToLower(simple)] {
			continue
		}

		var ids []string
		if strings.Contains(call.callee, ".") {
			for _, sym := range symbols {
				if sym.fqn == call.callee || strings.HasSuffix(sym.fqn, "."+call.callee) {
					ids = append(ids, sym.id)
				}
			}
		}
		if len(ids) == 0 && !strings.Contains(call.callee, ".") {
			ids = byName[simple]
		}
		if len(ids) != 1 || ids[0] == call.source {
			continue
		}
		if _, err := executor.Exec(
			`INSERT OR REPLACE INTO edges (source, target, label, properties) VALUES (?, ?, 'CALLS', '{}')`,
			call.source, ids[0],
		); err != nil {
			return resolved, fmt.Errorf("failed to bind call: %w", err)
		}
		if _, err := executor.Exec(
			`DELETE FROM unresolved_calls WHERE source = ? AND callee = ?`,
			call.source, call.callee,
		); err != nil {
			return resolved, err
		}
		resolved++
	}
	return resolved, nil
}

// GetNodeByFQN loads one node by its exact fully qualified name.
func (g *GraphDB) GetNodeByFQN(fqn, projectID string) (map[string]any, error) {
	query := `SELECT id, type, name, fqn, code, docstring, project_id, properties FROM nodes WHERE fqn = ?`
	args := []any{fqn}
	if projectID != "" {
		query += ` AND project_id = ?`
		args = append(args, projectID)
	}
	query += ` LIMIT 1;`
	row := g.db.QueryRow(query, args...)

	var nodeID, nodeType, name, gotFQN, code, docstring, gotProject, propsJSON string
	err := row.Scan(&nodeID, &nodeType, &name, &gotFQN, &code, &docstring, &gotProject, &propsJSON)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to scan node by fqn: %w", err)
	}
	var props map[string]any
	if err := json.Unmarshal([]byte(propsJSON), &props); err != nil {
		props = make(map[string]any)
	}
	return map[string]any{
		"id":         nodeID,
		"type":       nodeType,
		"name":       name,
		"fqn":        gotFQN,
		"code":       code,
		"docstring":  docstring,
		"project_id": gotProject,
		"properties": props,
	}, nil
}

// CallEdge is one directed CALLS relationship with the other endpoint's summary.
type CallEdge struct {
	ID   string
	Type string
	Name string
	FQN  string
}

// GetCallEdges returns callers and callees connected by CALLS edges.
func (g *GraphDB) GetCallEdges(nodeID string) (callers []CallEdge, callees []CallEdge, err error) {
	callers, err = g.queryCallEdge(`
		SELECT n.id, n.type, n.name, n.fqn
		FROM edges e
		JOIN nodes n ON e.source = n.id
		WHERE e.target = ? AND e.label = 'CALLS'
	`, nodeID)
	if err != nil {
		return nil, nil, err
	}
	callees, err = g.queryCallEdge(`
		SELECT n.id, n.type, n.name, n.fqn
		FROM edges e
		JOIN nodes n ON e.target = n.id
		WHERE e.source = ? AND e.label = 'CALLS'
	`, nodeID)
	if err != nil {
		return nil, nil, err
	}
	return callers, callees, nil
}

func (g *GraphDB) queryCallEdge(query, nodeID string) ([]CallEdge, error) {
	rows, err := g.db.Query(query, nodeID)
	if err != nil {
		return nil, fmt.Errorf("failed to query call edges: %w", err)
	}
	defer rows.Close()
	var edges []CallEdge
	for rows.Next() {
		var edge CallEdge
		if err := rows.Scan(&edge.ID, &edge.Type, &edge.Name, &edge.FQN); err != nil {
			return nil, err
		}
		edges = append(edges, edge)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return edges, nil
}

// StructureNode is a file-structure row without source text.
type StructureNode struct {
	Type      string
	FQN       string
	Docstring string
}

// QueryStructureByFQNPrefix lists nodes whose FQN is the file module or a member of it.
func (g *GraphDB) QueryStructureByFQNPrefix(projectID, fqnPrefix string) ([]StructureNode, error) {
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(fqnPrefix)
	query := `
	SELECT type, fqn, docstring FROM nodes
	WHERE project_id = ? AND (fqn = ? OR fqn LIKE ? ESCAPE '\')
	ORDER BY fqn;`
	rows, err := g.db.Query(query, projectID, fqnPrefix, escaped+".%")
	if err != nil {
		return nil, fmt.Errorf("failed to query file structure: %w", err)
	}
	defer rows.Close()
	var nodes []StructureNode
	for rows.Next() {
		var node StructureNode
		if err := rows.Scan(&node.Type, &node.FQN, &node.Docstring); err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return nodes, nil
}

// LexicalHit is a symbol match ranked by how the query tokens landed.
type LexicalHit struct {
	ID    string
	Score int
}

// SearchLexical ranks nodes by identifier, docstring, and source overlap.
// Tokens of 3+ characters go through the FTS5 trigram index. Shorter tokens
// fall back to LIKE because trigram cannot index them. Column weights prefer
// a name hit over the same token appearing in a body.
func (g *GraphDB) SearchLexical(projectID, query string, limit int) ([]LexicalHit, error) {
	tokens := lexicalTokens(query)
	if len(tokens) == 0 || limit <= 0 {
		return nil, nil
	}
	if len(tokens) > 8 {
		tokens = tokens[:8]
	}

	var ftsTokens []string
	for _, token := range tokens {
		if len([]rune(token)) >= 3 {
			ftsTokens = append(ftsTokens, token)
		}
	}
	if len(ftsTokens) == 0 {
		return g.searchLexicalLike(projectID, tokens, limit)
	}

	parts := make([]string, len(ftsTokens))
	for i, token := range ftsTokens {
		parts[i] = `"` + strings.ReplaceAll(token, `"`, `""`) + `"`
	}
	rows, err := g.db.Query(`
		SELECT n.id
		FROM nodes_fts
		JOIN nodes n ON n.rowid = nodes_fts.rowid
		WHERE nodes_fts MATCH ? AND (? = '' OR n.project_id = ?)
		ORDER BY bm25(nodes_fts, 10.0, 8.0, 3.0, 0.5)
		LIMIT ?`,
		strings.Join(parts, " OR "), projectID, projectID, limit,
	)
	if err != nil {
		return g.searchLexicalLike(projectID, tokens, limit)
	}
	defer rows.Close()

	var hits []LexicalHit
	score := limit
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		hits = append(hits, LexicalHit{ID: id, Score: score})
		score--
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return hits, nil
}

func (g *GraphDB) searchLexicalLike(projectID string, tokens []string, limit int) ([]LexicalHit, error) {

	var clauses []string
	args := []any{projectID}
	for _, token := range tokens {
		like := "%" + token + "%"
		clauses = append(clauses, "(name LIKE ? OR fqn LIKE ? OR docstring LIKE ? OR ifnull(code, '') LIKE ?)")
		args = append(args, like, like, like, like)
	}
	sqlQuery := `
	SELECT id, name, fqn, docstring, ifnull(code, '') FROM nodes
	WHERE project_id = ? AND (` + strings.Join(clauses, " OR ") + `)
	LIMIT 200;`
	rows, err := g.db.Query(sqlQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to search nodes lexically: %w", err)
	}
	defer rows.Close()

	var hits []LexicalHit
	for rows.Next() {
		var id, name, fqn, docstring, code string
		if err := rows.Scan(&id, &name, &fqn, &docstring, &code); err != nil {
			return nil, err
		}
		nameLower := strings.ToLower(name)
		fqnLower := strings.ToLower(fqn)
		docLower := strings.ToLower(docstring)
		codeLower := strings.ToLower(code)
		score := 0
		for _, token := range tokens {
			token = strings.ToLower(token)
			if nameLower == token {
				score += 5
			} else if strings.Contains(nameLower, token) {
				score += 3
			}
			if strings.Contains(fqnLower, token) {
				score += 2
			}
			if strings.Contains(docLower, token) {
				score += 1
			}
			if strings.Contains(codeLower, token) {
				score += 1
			}
		}
		if score > 0 {
			hits = append(hits, LexicalHit{ID: id, Score: score})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sortLexical(hits)
	if limit < len(hits) {
		hits = hits[:limit]
	}
	return hits, nil
}

func lexicalTokens(query string) []string {
	parts := strings.FieldsFunc(query, func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_')
	})
	var tokens []string
	seen := make(map[string]bool)
	add := func(part string) {
		if len([]rune(part)) < 2 {
			return
		}
		key := strings.ToLower(part)
		if seen[key] {
			return
		}
		seen[key] = true
		tokens = append(tokens, part)
	}
	for _, part := range parts {
		add(part)
	}
	var han []rune
	flushHan := func() {
		if len(han) >= 2 {
			add(string(han))
		}
		han = han[:0]
	}
	for _, r := range query {
		if r >= 0x4E00 && r <= 0x9FFF {
			han = append(han, r)
			continue
		}
		flushHan()
	}
	flushHan()
	return tokens
}

func sortLexical(hits []LexicalHit) {
	for i := 1; i < len(hits); i++ {
		j := i
		for j > 0 && (hits[j-1].Score < hits[j].Score || (hits[j-1].Score == hits[j].Score && hits[j-1].ID > hits[j].ID)) {
			hits[j-1], hits[j] = hits[j], hits[j-1]
			j--
		}
	}
}

// GetEventsForSearch returns active memories plus archived conversation events.
func (g *GraphDB) GetEventsForSearch(projectID string) ([]map[string]any, error) {
	if projectID == "" {
		projectID = "global"
	}
	query := `
	SELECT id, timestamp, event_type, summary, details, embedding, status, importance, last_accessed
	FROM events
	WHERE (status = 'active' OR event_type = 'conversation')
	  AND (project_id = ? OR project_id = 'global')
	ORDER BY importance DESC, timestamp DESC;
	`
	rows, err := g.db.Query(query, projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to get searchable events: %w", err)
	}
	defer rows.Close()

	var events []map[string]any
	for rows.Next() {
		var id, eventType, summary, details, status string
		var timestamp, lastAccessed int64
		var embedding []byte
		var importance int
		if err := rows.Scan(&id, &timestamp, &eventType, &summary, &details, &embedding, &status, &importance, &lastAccessed); err != nil {
			return nil, fmt.Errorf("failed to scan searchable event: %w", err)
		}
		events = append(events, map[string]any{
			"id":            id,
			"timestamp":     timestamp,
			"event_type":    eventType,
			"summary":       summary,
			"details":       details,
			"embedding":     embedding,
			"status":        status,
			"importance":    importance,
			"last_accessed": lastAccessed,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}
