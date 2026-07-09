package db

import (
	"database/sql"
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
	db, err := sql.Open("sqlite", dbPath)
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
	createVectorsTable := `
	CREATE TABLE IF NOT EXISTS vectors (
		node_id TEXT PRIMARY KEY,
		embedding BLOB NOT NULL,
		metadata TEXT,
		FOREIGN KEY (node_id) REFERENCES nodes(id) ON DELETE CASCADE
	);`
	if _, err := g.db.Exec(createVectorsTable); err != nil {
		return fmt.Errorf("failed to create vectors table: %w", err)
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
		fmt.Printf("[DB] Note: Could not alter events table (might already exist): %v\n", alterErr)
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
		"CREATE INDEX IF NOT EXISTS idx_vectors_node ON vectors(node_id);",
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

	query := `
	INSERT OR REPLACE INTO vectors (node_id, embedding, metadata)
	VALUES (?, ?, ?);
	`
	executor := g.getExecutor(tx)
	_, err := executor.Exec(query, nodeID, embedding, metadataJSON)
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
	query := "SELECT node_id, embedding, metadata FROM vectors;"
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
func (g *GraphDB) SaveEvent(tx *sql.Tx, id, eventType, summary, details string, timestamp int64, embedding []byte, status string) error {
	query := `
	INSERT OR REPLACE INTO events (id, timestamp, event_type, summary, details, embedding, status)
	VALUES (?, ?, ?, ?, ?, ?, ?);
	`
	executor := g.getExecutor(tx)
	_, err := executor.Exec(query, id, timestamp, eventType, summary, details, embedding, status)
	if err != nil {
		return fmt.Errorf("failed to save event: %w", err)
	}
	return nil
}

// GetAllActiveEvents retrieves all events where status = 'active'
func (g *GraphDB) GetAllActiveEvents() ([]map[string]any, error) {
	query := `
	SELECT id, timestamp, event_type, summary, details, embedding, status
	FROM events
	WHERE status = 'active'
	ORDER BY timestamp DESC;
	`
	rows, err := g.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to get active events: %w", err)
	}
	defer rows.Close()

	var events []map[string]any
	for rows.Next() {
		var id, eventType, summary, details, status string
		var timestamp int64
		var embedding []byte
		if err := rows.Scan(&id, &timestamp, &eventType, &summary, &details, &embedding, &status); err != nil {
			return nil, fmt.Errorf("failed to scan event: %w", err)
		}
		events = append(events, map[string]any{
			"id":         id,
			"timestamp":  timestamp,
			"event_type": eventType,
			"summary":    summary,
			"details":    details,
			"embedding":  embedding,
			"status":     status,
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
	SELECT id, timestamp, event_type, summary, details, embedding
	FROM events
	WHERE status = 'active'
	ORDER BY timestamp DESC
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
		var timestamp int64
		var embedding []byte
		if err := rows.Scan(&id, &timestamp, &eventType, &summary, &details, &embedding); err != nil {
			return nil, err
		}

		events = append(events, map[string]any{
			"id":         id,
			"timestamp":  timestamp,
			"event_type": eventType,
			"summary":    summary,
			"details":    details,
			"embedding":  embedding,
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
	SELECT id, timestamp, event_type, summary, details, embedding
	FROM events
	ORDER BY timestamp DESC
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
		var timestamp int64
		var embedding []byte
		if err := rows.Scan(&id, &timestamp, &eventType, &summary, &details, &embedding); err != nil {
			return nil, err
		}

		events = append(events, map[string]any{
			"id":         id,
			"timestamp":  timestamp,
			"event_type": eventType,
			"summary":    summary,
			"details":    details,
			"embedding":  embedding,
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
