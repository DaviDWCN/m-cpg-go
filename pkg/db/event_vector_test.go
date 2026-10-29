package db

import (
	"path/filepath"
	"testing"

	"m-cpg-go/pkg/vector"
)

func TestSearchEventVectorsUsesIndex(t *testing.T) {
	gdb, err := InitDB(filepath.Join(t.TempDir(), "events.db"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer gdb.Close()

	near := vector.Float32SliceToBytes([]float32{1, 0, 0, 0})
	far := vector.Float32SliceToBytes([]float32{0, 1, 0, 0})
	pseudo := vector.Float32SliceToBytes([]float32{0, 0, 1, 0})
	if err := gdb.SaveEvent(nil, "near", "preference", "wal", "near", 1, near, "active", 1, "p", "api"); err != nil {
		t.Fatal(err)
	}
	if err := gdb.SaveEvent(nil, "far", "preference", "other", "far", 2, far, "active", 1, "p", "api"); err != nil {
		t.Fatal(err)
	}
	if err := gdb.SaveEvent(nil, "pseudo", "preference", "hash", "pseudo", 3, pseudo, "active", 1, "p", "pseudo"); err != nil {
		t.Fatal(err)
	}
	if err := gdb.SaveEvent(nil, "global", "preference", "shared", "global", 4, near, "active", 1, "global", "api"); err != nil {
		t.Fatal(err)
	}
	if err := gdb.SaveEvent(nil, "other-project", "preference", "hidden", "hidden", 5, near, "active", 1, "other", "api"); err != nil {
		t.Fatal(err)
	}
	if err := gdb.SaveEvent(nil, "archived", "note", "old", "old", 6, near, "archived", 1, "p", "api"); err != nil {
		t.Fatal(err)
	}
	if err := gdb.SaveEvent(nil, "talk", "conversation", "chat", "chat", 7, far, "archived", 1, "p", "api"); err != nil {
		t.Fatal(err)
	}

	hits, err := gdb.SearchEventVectors("p", near, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) < 2 || hits[0].ID != "global" && hits[0].ID != "near" {
		t.Fatalf("expected an exact vector first, got %+v", hits)
	}
	if hits[0].Distance > 0.01 {
		t.Fatalf("expected zero distance for an exact vector, got %v", hits[0].Distance)
	}
	seen := map[string]bool{}
	for _, hit := range hits {
		seen[hit.ID] = true
	}
	if seen["pseudo"] || seen["other-project"] || seen["archived"] {
		t.Fatalf("index returned a row that should be excluded: %+v", hits)
	}
	if !seen["near"] || !seen["global"] || !seen["talk"] {
		t.Fatalf("missing indexed project, global, or conversation hit: %+v", hits)
	}

	unindexed, err := gdb.ListUnindexedEventEmbeddings("p")
	if err != nil {
		t.Fatal(err)
	}
	if len(unindexed) != 1 || unindexed[0].ID != "pseudo" {
		t.Fatalf("expected only the pseudo event to stay unindexed, got %+v", unindexed)
	}

	if err := gdb.SaveEvent(nil, "near", "preference", "wal", "replaced", 8, pseudo, "active", 1, "p", "pseudo"); err != nil {
		t.Fatal(err)
	}
	hits, err = gdb.SearchEventVectors("p", near, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, hit := range hits {
		if hit.ID == "near" {
			t.Fatalf("replaced pseudo event should leave the index, got %+v", hits)
		}
	}
	unindexed, err = gdb.ListUnindexedEventEmbeddings("p")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range unindexed {
		if ev.ID == "near" {
			found = true
		}
	}
	if !found {
		t.Fatalf("replaced event should be scored from its blob, got %+v", unindexed)
	}
}
