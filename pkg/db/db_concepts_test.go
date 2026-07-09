package db

import (
	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/asg017/sqlite-vec-go-bindings/ncruces"
	"os"
	"testing"
)

func TestConceptOperations(t *testing.T) {
	dbPath := "test_concepts.db"
	defer os.Remove(dbPath)

	g, err := InitDB(dbPath, 768)
	if err != nil {
		t.Fatalf("Failed to init DB: %v", err)
	}
	defer g.Close()

	// Add event
	err = g.SaveEvent(nil, "evt1", "test", "summary1", "details1", 12345, []byte{0, 1}, "active", 0)
	if err != nil {
		t.Fatalf("Failed to save event: %v", err)
	}

	// Save concepts
	concepts := []string{"knowledge graph", "memory", "knowledge graph"}
	err = g.SaveConcepts(nil, "evt1", concepts)
	if err != nil {
		t.Fatalf("Failed to save concepts: %v", err)
	}

	// Save more concepts for same event to test frequency increase
	err = g.SaveConcepts(nil, "evt1", []string{"knowledge graph"})
	if err != nil {
		t.Fatalf("Failed to save concepts again: %v", err)
	}

	// Get top concepts
	top, err := g.GetTopConcepts(5)
	if err != nil {
		t.Fatalf("Failed to get top concepts: %v", err)
	}

	if len(top) != 2 {
		t.Fatalf("Expected 2 unique concepts, got %d", len(top))
	}

	if top[0]["name"] != "knowledge graph" || top[0]["frequency"].(int) != 3 {
		t.Errorf("Expected 'knowledge graph' with frequency 3, got %v with frequency %v", top[0]["name"], top[0]["frequency"])
	}

	if top[1]["name"] != "memory" || top[1]["frequency"].(int) != 1 {
		t.Errorf("Expected 'memory' with frequency 1, got %v with frequency %v", top[1]["name"], top[1]["frequency"])
	}
}
