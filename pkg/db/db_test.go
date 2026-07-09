package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestGraphDB_Operations(t *testing.T) {
	// Create temporary db path
	tmpDir, err := os.MkdirTemp("", "m-cpg-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	gdb, err := InitDB(dbPath)
	if err != nil {
		t.Fatalf("failed to init db: %v", err)
	}
	defer gdb.Close()

	// 1. Test AddNode
	nodeID1 := "module_test_pkg"
	err = gdb.AddNode(nil, nodeID1, "Module", "test_pkg", "test_pkg", "package test_pkg", "This is a test module", "project_1", map[string]any{"version": "1.0"})
	if err != nil {
		t.Errorf("failed to add node 1: %v", err)
	}

	nodeID2 := "class_Calculator"
	err = gdb.AddNode(nil, nodeID2, "Class", "Calculator", "test_pkg.Calculator", "type Calculator struct{}", "Calculates things", "project_1", nil)
	if err != nil {
		t.Errorf("failed to add node 2: %v", err)
	}

	// 2. Test GetNode
	n1, err := gdb.GetNode(nodeID1)
	if err != nil {
		t.Errorf("failed to get node 1: %v", err)
	}
	if n1 == nil || n1["name"] != "test_pkg" {
		t.Errorf("node 1 retrieval mismatch: got %v", n1)
	}

	// 3. Test AddEdge
	err = gdb.AddEdge(nil, nodeID1, nodeID2, "CONTAINS", map[string]any{"order": 1})
	if err != nil {
		t.Errorf("failed to add edge: %v", err)
	}

	// 4. Test GetEdges
	edges, err := gdb.GetEdges(nodeID1)
	if err != nil {
		t.Errorf("failed to get edges: %v", err)
	}
	if len(edges) != 1 {
		t.Errorf("expected 1 edge, got %d", len(edges))
	} else {
		edge := edges[0]
		if edge["target"] != nodeID2 || edge["label"] != "CONTAINS" {
			t.Errorf("unexpected edge details: %v", edge)
		}
	}

	// 5. Test GetNeighbors
	neighbors, err := gdb.GetNeighbors(nodeID1)
	if err != nil {
		t.Errorf("failed to get neighbors: %v", err)
	}
	if len(neighbors) != 1 {
		t.Errorf("expected 1 neighbor, got %d", len(neighbors))
	} else if neighbors[0]["id"] != nodeID2 {
		t.Errorf("expected neighbor %s, got %s", nodeID2, neighbors[0]["id"])
	}

	// 6. Test Save and Load Vectors
	embData := []byte{1, 2, 3, 4, 5, 6, 7, 8} // dummy bytes
	err = gdb.SaveVector(nil, nodeID2, embData, map[string]any{"type": "Class"})
	if err != nil {
		t.Errorf("failed to save vector: %v", err)
	}

	vectors, err := gdb.LoadVectors()
	if err != nil {
		t.Errorf("failed to load vectors: %v", err)
	}
	if len(vectors) != 1 {
		t.Errorf("expected 1 vector, got %d", len(vectors))
	} else {
		vec := vectors[0]
		if vec.NodeID != nodeID2 {
			t.Errorf("expected vector for %s, got %s", nodeID2, vec.NodeID)
		}
	}

	// 7. Test ClearProject
	err = gdb.ClearProject("project_1")
	if err != nil {
		t.Errorf("failed to clear project: %v", err)
	}
	n2, _ := gdb.GetNode(nodeID2)
	if n2 != nil {
		t.Errorf("expected node 2 to be deleted after clearing project")
	}
}

func TestQueryPattern(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test_pattern.db")
	gdb, err := InitDB(dbPath)
	if err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	defer gdb.Close()

	// Seed data
	err = gdb.RunInTransaction(func(tx *sql.Tx) error {
		gdb.AddNode(tx, "m1", "Method", "FuncA", "pkg.FuncA", "", "", "proj", nil)
		gdb.AddNode(tx, "m2", "Method", "FuncB", "pkg.FuncB", "", "", "proj", nil)
		gdb.AddNode(tx, "c1", "Class", "ClassA", "pkg.ClassA", "", "", "proj", nil)

		gdb.AddEdge(tx, "m1", "m2", "CALLS", map[string]any{"weight": 2})
		gdb.AddEdge(tx, "c1", "m1", "CONTAINS", nil)

		return nil
	})

	if err != nil {
		t.Fatalf("seeding failed: %v", err)
	}

	// Test case 1: Method -> CALLS -> Method
	res, err := gdb.QueryPattern("Method(*) -> CALLS -> Method(FuncB)", 1)
	if err != nil {
		t.Fatalf("QueryPattern failed: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("expected 1 result, got %d", len(res))
	}

	srcMap := res[0]["source"].(map[string]any)
	if srcMap["name"] != "FuncA" {
		t.Errorf("expected source to be FuncA")
	}

	// Test case 2: Class -> CONTAINS -> Method
	res2, err := gdb.QueryPattern("Class(ClassA) -> CONTAINS -> Method(*)", 1)
	if err != nil {
		t.Fatalf("QueryPattern failed: %v", err)
	}
	if len(res2) != 1 {
		t.Fatalf("expected 1 result, got %d", len(res2))
	}

	srcMap2 := res2[0]["source"].(map[string]any)
	if srcMap2["name"] != "ClassA" {
		t.Errorf("expected source to be ClassA")
	}

	// Seed multihop data
	err = gdb.RunInTransaction(func(tx *sql.Tx) error {
		gdb.AddNode(tx, "m3", "Method", "FuncC", "pkg.FuncC", "", "", "proj", nil)
		gdb.AddEdge(tx, "m2", "m3", "CALLS", nil)
		return nil
	})
	if err != nil {
		t.Fatalf("seeding multihop failed: %v", err)
	}

	// Test case 3: Multihop Method -> CALLS -> Method(FuncC)
	res3, err := gdb.QueryPattern("Method(FuncA) -> CALLS -> Method(FuncC)", 2)
	if err != nil {
		t.Fatalf("QueryPattern failed: %v", err)
	}
	if len(res3) != 1 {
		t.Fatalf("expected 1 multihop result, got %d", len(res3))
	}
	if res3[0]["depth"].(int) != 2 {
		t.Errorf("expected depth 2, got %v", res3[0]["depth"])
	}
}
