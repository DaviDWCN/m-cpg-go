package vector

import (
	"path/filepath"
	"testing"

	"m-cpg-go/pkg/db"
)

func TestSearchClampsKAndOrdersByDistance(t *testing.T) {
	gdb, err := db.InitDB(filepath.Join(t.TempDir(), "vec.db"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer gdb.Close()

	if err := gdb.AddNode(nil, "near", "Method", "Near", "pkg.Near", "", "sqlite helper", "p", "a.go", nil); err != nil {
		t.Fatal(err)
	}
	if err := gdb.AddNode(nil, "far", "Method", "Far", "pkg.Far", "", "other", "p", "b.go", nil); err != nil {
		t.Fatal(err)
	}
	near := []float32{1, 0, 0, 0}
	far := []float32{0, 1, 0, 0}
	if err := gdb.SaveVector(nil, "near", Float32SliceToBytes(near), nil); err != nil {
		t.Fatal(err)
	}
	if err := gdb.SaveVector(nil, "far", Float32SliceToBytes(far), nil); err != nil {
		t.Fatal(err)
	}

	store := NewVectorStore(gdb.GetDB())
	results, err := store.Search(near, 10, "p")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 hits when k exceeds row count, got %d", len(results))
	}
	if results[0].ID != "near" {
		t.Fatalf("expected nearest id first, got %s", results[0].ID)
	}
}
