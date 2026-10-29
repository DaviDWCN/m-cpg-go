package db

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestResolveProjectCalls(t *testing.T) {
	gdb, err := InitDB(filepath.Join(t.TempDir(), "resolve.db"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer gdb.Close()

	if err := gdb.AddNode(nil, "method_pkg.Caller", "Method", "Caller", "pkg.Caller", "", "", "p", "a.go", nil); err != nil {
		t.Fatal(err)
	}
	if err := gdb.AddNode(nil, "method_pkg.UniqueHelper", "Method", "UniqueHelper", "pkg.UniqueHelper", "", "", "p", "b.go", nil); err != nil {
		t.Fatal(err)
	}
	if err := gdb.AddNode(nil, "method_pkg.Println", "Method", "Println", "pkg.Println", "", "", "p", "b.go", nil); err != nil {
		t.Fatal(err)
	}
	if err := gdb.SaveUnresolvedCall(nil, "method_pkg.Caller", "UniqueHelper", "p"); err != nil {
		t.Fatal(err)
	}
	if err := gdb.SaveUnresolvedCall(nil, "method_pkg.Caller", "fmt.Println", "p"); err != nil {
		t.Fatal(err)
	}

	n, err := gdb.ResolveProjectCalls(nil, "p")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 resolved call, got %d", n)
	}
	_, callees, err := gdb.GetCallEdges("method_pkg.Caller")
	if err != nil {
		t.Fatal(err)
	}
	if len(callees) != 1 || callees[0].FQN != "pkg.UniqueHelper" {
		t.Fatalf("expected callee pkg.UniqueHelper, got %+v", callees)
	}
}

func TestSearchLexicalUsesFTS(t *testing.T) {
	gdb, err := InitDB(filepath.Join(t.TempDir(), "fts.db"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer gdb.Close()

	if err := gdb.AddNode(nil, "method_db.InitDB", "Method", "InitDB", "db.InitDB", "", "initializes the SQLite database", "p", "db.go", nil); err != nil {
		t.Fatal(err)
	}
	if err := gdb.AddNode(nil, "method_helper.UniqueHelper", "Method", "UniqueHelper", "helper.UniqueHelper", "", "代码图调用关系", "p", "h.go", nil); err != nil {
		t.Fatal(err)
	}
	if err := gdb.AddNode(nil, "method_pkg.ID", "Method", "ID", "pkg.ID", "", "", "p", "id.go", nil); err != nil {
		t.Fatal(err)
	}

	hits, err := gdb.SearchLexical("p", "SQLite", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].ID != "method_db.InitDB" {
		t.Fatalf("expected InitDB for SQLite, got %+v", hits)
	}

	hits, err = gdb.SearchLexical("p", "代码图", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].ID != "method_helper.UniqueHelper" {
		t.Fatalf("expected UniqueHelper for 代码图, got %+v", hits)
	}

	hits, err = gdb.SearchLexical("p", "ID", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].ID != "method_pkg.ID" {
		t.Fatalf("expected short-token LIKE fallback, got %+v", hits)
	}

	if err := gdb.AddNode(nil, "method_pkg.Wrap", "Method", "Wrap", "pkg.Wrap", "return Qx", "", "p", "w.go", nil); err != nil {
		t.Fatal(err)
	}
	hits, err = gdb.SearchLexical("p", "Qx", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].ID != "method_pkg.Wrap" {
		t.Fatalf("expected short token inside code, got %+v", hits)
	}
}

func TestSearchLexicalMatchesCodeBody(t *testing.T) {
	gdb, err := InitDB(filepath.Join(t.TempDir(), "fts.db"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer gdb.Close()

	if err := gdb.AddNode(nil, "method_pkg.Flush", "Method", "Flush", "pkg.Flush", "return db.checkpointwal()", "", "p", "f.go", nil); err != nil {
		t.Fatal(err)
	}
	if err := gdb.AddNode(nil, "method_pkg.Note", "Method", "Note", "pkg.Note", "", "mentions checkpointwal in the docstring", "p", "n.go", nil); err != nil {
		t.Fatal(err)
	}

	hits, err := gdb.SearchLexical("p", "checkpointwal", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) < 2 {
		t.Fatalf("expected code and docstring hits, got %+v", hits)
	}
	if hits[0].ID != "method_pkg.Note" {
		t.Fatalf("expected docstring hit to rank first, got %+v", hits)
	}
	foundCode := false
	for _, hit := range hits {
		if hit.ID == "method_pkg.Flush" {
			foundCode = true
		}
	}
	if !foundCode {
		t.Fatalf("expected code-only hit, got %+v", hits)
	}

	if err := gdb.AddNode(nil, "method_pkg.Flush", "Method", "Flush", "pkg.Flush", "return db.otherTokenzz()", "", "p", "f.go", nil); err != nil {
		t.Fatal(err)
	}
	hits, err = gdb.SearchLexical("p", "checkpointwal", 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, hit := range hits {
		if hit.ID == "method_pkg.Flush" {
			t.Fatalf("replaced source should leave the index, got %+v", hits)
		}
	}
	hits, err = gdb.SearchLexical("p", "otherTokenzz", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].ID != "method_pkg.Flush" {
		t.Fatalf("expected replaced source to be searchable, got %+v", hits)
	}
}

func TestFTSMigratesLegacyIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fts.db")
	gdb, err := InitDB(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	installLegacyNodeFTS(t, gdb)
	if err := gdb.AddNode(nil, "method_pkg.Flush", "Method", "Flush", "pkg.Flush", "return db.checkpointwal()", "", "p", "f.go", nil); err != nil {
		t.Fatal(err)
	}
	hits, err := gdb.SearchLexical("p", "checkpointwal", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("legacy index should not match source text, got %+v", hits)
	}
	gdb.Close()

	gdb, err = InitDB(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer gdb.Close()
	hits, err = gdb.SearchLexical("p", "checkpointwal", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].ID != "method_pkg.Flush" {
		t.Fatalf("rebuilt index should match source text, got %+v", hits)
	}
}

func installLegacyNodeFTS(t *testing.T, gdb *GraphDB) {
	t.Helper()
	statements := []string{
		`DROP TRIGGER IF EXISTS nodes_fts_ai`,
		`DROP TRIGGER IF EXISTS nodes_fts_ad`,
		`DROP TRIGGER IF EXISTS nodes_fts_au`,
		`DROP TABLE IF EXISTS nodes_fts`,
		`CREATE VIRTUAL TABLE nodes_fts USING fts5(
			name, fqn, docstring,
			content='nodes', content_rowid='rowid',
			tokenize='trigram'
		)`,
		`CREATE TRIGGER nodes_fts_ai AFTER INSERT ON nodes BEGIN
			INSERT INTO nodes_fts(rowid, name, fqn, docstring)
			VALUES (new.rowid, new.name, new.fqn, new.docstring);
		END`,
		`CREATE TRIGGER nodes_fts_ad AFTER DELETE ON nodes BEGIN
			INSERT INTO nodes_fts(nodes_fts, rowid, name, fqn, docstring)
			VALUES ('delete', old.rowid, old.name, old.fqn, old.docstring);
		END`,
		`CREATE TRIGGER nodes_fts_au AFTER UPDATE ON nodes BEGIN
			INSERT INTO nodes_fts(nodes_fts, rowid, name, fqn, docstring)
			VALUES ('delete', old.rowid, old.name, old.fqn, old.docstring);
			INSERT INTO nodes_fts(rowid, name, fqn, docstring)
			VALUES (new.rowid, new.name, new.fqn, new.docstring);
		END`,
	}
	for _, statement := range statements {
		if _, err := gdb.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSnapshotIncomingEdgesSurvivesReindex(t *testing.T) {
	gdb, err := InitDB(filepath.Join(t.TempDir(), "edges.db"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer gdb.Close()

	if err := gdb.AddNode(nil, "method_pkg.Caller", "Method", "Caller", "pkg.Caller", "", "", "p", "a.go", nil); err != nil {
		t.Fatal(err)
	}
	if err := gdb.AddNode(nil, "method_pkg.Target", "Method", "Target", "pkg.Target", "", "", "p", "b.go", nil); err != nil {
		t.Fatal(err)
	}
	if err := gdb.AddEdge(nil, "method_pkg.Caller", "method_pkg.Target", "CALLS", nil); err != nil {
		t.Fatal(err)
	}

	var saved []EdgeRecord
	err = gdb.RunInTransaction(func(tx *sql.Tx) error {
		incoming, err := gdb.SnapshotIncomingEdges(tx, "b.go", "p")
		if err != nil {
			return err
		}
		saved = incoming
		return gdb.DeleteNodesByFile(tx, "b.go", "p")
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(saved) != 1 || saved[0].Source != "method_pkg.Caller" || saved[0].Target != "method_pkg.Target" {
		t.Fatalf("expected the cross-file edge to be snapshotted, got %+v", saved)
	}

	if err := gdb.AddNode(nil, "method_pkg.Target", "Method", "Target", "pkg.Target", "", "", "p", "b.go", nil); err != nil {
		t.Fatal(err)
	}
	if err := gdb.RunInTransaction(func(tx *sql.Tx) error {
		return gdb.RestoreEdges(tx, saved)
	}); err != nil {
		t.Fatal(err)
	}
	_, callees, err := gdb.GetCallEdges("method_pkg.Caller")
	if err != nil {
		t.Fatal(err)
	}
	if len(callees) != 1 || callees[0].FQN != "pkg.Target" {
		t.Fatalf("expected restored callee, got %+v", callees)
	}
}
