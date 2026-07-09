package db

import (
	"database/sql"
	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/asg017/sqlite-vec-go-bindings/ncruces"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
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
func InitDB(dbPath string) (*GraphDB, error) {
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

	gdb := &GraphDB{db: db}
	if err := gdb.ensureSchema(); err != nil {
		db.Close()
		return nil, err
	}

	return gdb, nil
}

func (g *GraphDB) ensureSchema() error {
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
		properties TEXT
	);`
	if _, err := g.db.Exec(createNodesTable); err != nil {
		return fmt.Errorf("failed to create nodes table: %w", err)
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
	createVectorsTable := `CREATE VIRTUAL TABLE IF NOT EXISTS vectors USING vec0(embedding float[768]);`
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

	// Create events table for developer preferences and session logs
	createEventsTable := `
	CREATE TABLE IF NOT EXISTS events (
		id TEXT PRIMARY KEY,
		timestamp INTEGER NOT NULL,
		event_type TEXT NOT NULL,
		summary TEXT NOT NULL,
		details TEXT,
		embedding BLOB NOT NULL,
		status TEXT NOT NULL DEFAULT 'active'
	);`
	if _, err := g.db.Exec(createEventsTable); err != nil {
		return fmt.Errorf("failed to create events table: %w", err)
	}

	// Handle migration for existing events table
	// SQLite ALTER TABLE ADD COLUMN does not support IF NOT EXISTS in all versions natively without checking.
	// We run it and safely ignore the "duplicate column name" error.
	alterEventsTable := `ALTER TABLE events ADD COLUMN status TEXT NOT NULL DEFAULT 'active';`
	_, alterErr := g.db.Exec(alterEventsTable)
	if alterErr != nil && !strings.Contains(alterErr.Error(), "duplicate column name") {
		// Only log or ignore, do not fail completely if it's already there or something similar.
		// A fatal failure would prevent the app from starting.
		fmt.Printf("[DB] Note: Could not alter events table status column: %v\n", alterErr)
	}

	alterEventsTableImp := `ALTER TABLE events ADD COLUMN importance INTEGER NOT NULL DEFAULT 0;`
	_, alterErrImp := g.db.Exec(alterEventsTableImp)
	if alterErrImp != nil && !strings.Contains(alterErrImp.Error(), "duplicate column name") {
		fmt.Printf("[DB] Note: Could not alter events table importance column: %v\n", alterErrImp)
	}

	alterEventsTableAcc := `ALTER TABLE events ADD COLUMN last_accessed INTEGER NOT NULL DEFAULT 0;`
	_, alterErrAcc := g.db.Exec(alterEventsTableAcc)
	if alterErrAcc != nil && !strings.Contains(alterErrAcc.Error(), "duplicate column name") {
		fmt.Printf("[DB] Note: Could not alter events table last_accessed column: %v\n", alterErrAcc)
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

	// Create indexes for efficient retrieval and graph traversal
	indexes := []string{
		"CREATE INDEX IF NOT EXISTS idx_nodes_type ON nodes(type);",
		"CREATE INDEX IF NOT EXISTS idx_nodes_fqn ON nodes(fqn);",
		"CREATE INDEX IF NOT EXISTS idx_nodes_project ON nodes(project_id);",
		"CREATE INDEX IF NOT EXISTS idx_edges_source ON edges(source);",
		"CREATE INDEX IF NOT EXISTS idx_edges_target ON edges(target);",
		"CREATE INDEX IF NOT EXISTS idx_edges_label ON edges(label);",
		"CREATE INDEX IF NOT EXISTS idx_events_type ON events(event_type);",
		"CREATE INDEX IF NOT EXISTS idx_concepts_freq ON concepts(frequency DESC);",
	}

	for _, idxQuery := range indexes {
		if _, err := g.db.Exec(idxQuery); err != nil {
			return fmt.Errorf("failed to create index (%s): %w", idxQuery, err)
		}
	}

	return nil
}

// AddNode inserts or replaces a node in the graph
func (g *GraphDB) AddNode(tx *sql.Tx, id, nodeType, name, fqn, code, docstring, projectID string, props map[string]any) error {
	propsJSON := "{}"
	if len(props) > 0 {
		data, err := json.Marshal(props)
		if err != nil {
			return fmt.Errorf("failed to marshal node properties: %w", err)
		}
		propsJSON = string(data)
	}

	query := `
	INSERT OR REPLACE INTO nodes (id, type, name, fqn, code, docstring, project_id, properties)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?);
	`
	executor := g.getExecutor(tx)
	_, err := executor.Exec(query, id, nodeType, name, fqn, code, docstring, projectID, propsJSON)
	if err != nil {
		return fmt.Errorf("failed to add node: %w", err)
	}
	return nil
}

// AddEdge inserts or replaces an edge in the graph. Source and Target must exist first to prevent foreign key issues,
// but to make it simple we insert standard placeholders if they are missing (similar to KuzuAdapter).
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

	// Ensure source node placeholder exists
	ensureNodeQuery := `INSERT OR IGNORE INTO nodes (id, type, name, fqn, code, docstring, project_id, properties) VALUES (?, 'Node', ?, ?, '', '', '', '{}');`
	if _, err := executor.Exec(ensureNodeQuery, source, source, source); err != nil {
		return fmt.Errorf("failed to ensure source node: %w", err)
	}
	if _, err := executor.Exec(ensureNodeQuery, target, target, target); err != nil {
		return fmt.Errorf("failed to ensure target node: %w", err)
	}

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

// GetNeighbors retrieves neighboring nodes (nodes connected to the current node)
func (g *GraphDB) GetNeighbors(nodeID string) ([]map[string]any, error) {
	query := `
	SELECT DISTINCT n.id, n.type, n.name, n.fqn, n.code, n.docstring, n.project_id, n.properties
	FROM nodes n
	JOIN edges e ON (e.source = n.id AND e.target = ?) OR (e.target = n.id AND e.source = ?)
	WHERE n.id != ?;
	`
	rows, err := g.db.Query(query, nodeID, nodeID, nodeID)
	if err != nil {
		return nil, fmt.Errorf("failed to query neighbors: %w", err)
	}
	defer rows.Close()

	var neighbors []map[string]any
	for rows.Next() {
		var id, nodeType, name, fqn, code, docstring, projectID, propsJSON string
		if err := rows.Scan(&id, &nodeType, &name, &fqn, &code, &docstring, &projectID, &propsJSON); err != nil {
			return nil, err
		}
		var props map[string]any
		if err := json.Unmarshal([]byte(propsJSON), &props); err != nil {
			props = make(map[string]any)
		}

		neighbors = append(neighbors, map[string]any{
			"id":         id,
			"type":       nodeType,
			"name":       name,
			"fqn":        fqn,
			"code":       code,
			"docstring":  docstring,
			"project_id": projectID,
			"properties": props,
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
	// First delete all edges connected to these nodes (SQLite CASCADE foreign keys handles this,
	// but manual delete is safer in case foreign keys PRAGMA isn't fully enabled)
	deleteEdges := `
	DELETE FROM edges 
	WHERE source IN (SELECT id FROM nodes WHERE project_id = ?)
	   OR target IN (SELECT id FROM nodes WHERE project_id = ?);
	`
	if _, err := g.db.Exec(deleteEdges, projectID, projectID); err != nil {
		return fmt.Errorf("failed to clear project edges: %w", err)
	}

	deleteNodes := "DELETE FROM nodes WHERE project_id = ?;"
	if _, err := g.db.Exec(deleteNodes, projectID); err != nil {
		return fmt.Errorf("failed to clear project nodes: %w", err)
	}
	return nil
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
func (g *GraphDB) SaveEvent(tx *sql.Tx, id, eventType, summary, details string, timestamp int64, embedding []byte, status string, importance int) error {
	query := `
	INSERT OR REPLACE INTO events (id, timestamp, event_type, summary, details, embedding, status, importance, last_accessed)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);
	`
	executor := g.getExecutor(tx)
	_, err := executor.Exec(query, id, timestamp, eventType, summary, details, embedding, status, importance, timestamp)
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
			id string
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
			fmt.Printf("[DB] Tiering GC archived %d stale events.\n", len(toArchive))
		}

		return nil
	})
}

// GetAllActiveEvents retrieves all events where status = 'active'
func (g *GraphDB) GetAllActiveEvents() ([]map[string]any, error) {
	query := `
	SELECT id, timestamp, event_type, summary, details, embedding, status, importance, last_accessed
	FROM events
	WHERE status = 'active'
	ORDER BY importance DESC, timestamp DESC;
	`
	rows, err := g.db.Query(query)
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
func (g *GraphDB) GetRecentActiveEvents(limit int) ([]map[string]any, error) {
	query := `
	SELECT id, timestamp, event_type, summary, details, embedding, importance, last_accessed
	FROM events
	WHERE status = 'active'
	ORDER BY importance DESC, timestamp DESC
	LIMIT ?;
	`
	rows, err := g.db.Query(query, limit)
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
func (g *GraphDB) GetRecentEvents(limit int) ([]map[string]any, error) {
	query := `
	SELECT id, timestamp, event_type, summary, details, embedding, importance, last_accessed
	FROM events
	ORDER BY importance DESC, timestamp DESC
	LIMIT ?;
	`
	rows, err := g.db.Query(query, limit)
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
