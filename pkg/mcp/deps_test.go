package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m-cpg-go/pkg/config"
	"m-cpg-go/pkg/db"
	"m-cpg-go/pkg/vector"
)

func TestRunGetNodeDependenciesReadsCallEdges(t *testing.T) {
	gdb, err := db.InitDB(filepath.Join(t.TempDir(), "deps.db"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer gdb.Close()

	if err := gdb.AddNode(nil, "method_pkg.FuncA", "Method", "FuncA", "pkg.FuncA", "func FuncA() { FuncB() }", "", "p", "a.go", nil); err != nil {
		t.Fatal(err)
	}
	if err := gdb.AddNode(nil, "method_pkg.FuncB", "Method", "FuncB", "pkg.FuncB", "func FuncB() { FuncC() }", "", "p", "b.go", nil); err != nil {
		t.Fatal(err)
	}
	if err := gdb.AddNode(nil, "method_pkg.FuncC", "Method", "FuncC", "pkg.FuncC", "func FuncC() {}", "", "p", "c.go", nil); err != nil {
		t.Fatal(err)
	}
	if err := gdb.AddEdge(nil, "method_pkg.FuncA", "method_pkg.FuncB", "CALLS", nil); err != nil {
		t.Fatal(err)
	}
	if err := gdb.AddEdge(nil, "method_pkg.FuncB", "method_pkg.FuncC", "CALLS", nil); err != nil {
		t.Fatal(err)
	}

	out, err := RunGetNodeDependencies("pkg.FuncB", gdb)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "pkg.FuncA") || !strings.Contains(out, "pkg.FuncC") {
		t.Fatalf("dependencies missing caller or callee:\n%s", out)
	}

	graph, err := RunQueryGraph("Method(FuncA) -> CALLS -> Method(FuncB)", 1, gdb)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(graph, "pkg.FuncA") || !strings.Contains(graph, "pkg.FuncB") {
		t.Fatalf("graph query did not render endpoints:\n%s", graph)
	}
}

func TestIndexBindsCrossFileCall(t *testing.T) {
	dir := t.TempDir()
	caller := "package main\n\nfunc Caller() {\n\tUniqueHelper()\n}\n"
	callee := "package main\n\nfunc UniqueHelper() {}\n"
	if err := os.WriteFile(filepath.Join(dir, "caller.go"), []byte(caller), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "helper.go"), []byte(callee), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := config.LoadDefaultConfig()
	cfg.Embedding.Provider = "mock"
	cfg.ProjectID = "p"
	gdb, err := db.InitDB(filepath.Join(t.TempDir(), "index.db"), cfg.Embedding.Dimension)
	if err != nil {
		t.Fatal(err)
	}
	defer gdb.Close()

	if _, _, _, err := RunIndexing(dir, "p", gdb, vector.NewVectorStore(gdb.GetDB()), cfg); err != nil {
		t.Fatal(err)
	}

	out, err := RunGetNodeDependencies("caller.Caller", gdb)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "helper.UniqueHelper") {
		t.Fatalf("cross-file call was not bound:\n%s", out)
	}
}
