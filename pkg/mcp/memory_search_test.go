package mcp

import (
	"path/filepath"
	"strings"
	"testing"

	"m-cpg-go/pkg/config"
	"m-cpg-go/pkg/db"
)

func TestRememberPseudoStaysOutOfEventIndex(t *testing.T) {
	cfg := config.LoadDefaultConfig()
	cfg.Embedding.Provider = "mock"
	cfg.Embedding.Dimension = 32
	gdb, err := db.InitDB(filepath.Join(t.TempDir(), "mem.db"), cfg.Embedding.Dimension)
	if err != nil {
		t.Fatal(err)
	}
	defer gdb.Close()

	if err := RunRemember("wal mode", "use WAL", "preference", 3, "p", gdb, cfg); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := gdb.GetDB().QueryRow(`SELECT COUNT(*) FROM event_vectors`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("pseudo memory was written to the vector index: %d", n)
	}

	out, err := RunSearchMemory("wal mode", 5, "p", gdb, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "wal mode") {
		t.Fatalf("pseudo memory was not found by the residual scan:\n%s", out)
	}
}
