package mcp_test

import (
	"m-cpg-go/pkg/config"
	"m-cpg-go/pkg/db"
	"m-cpg-go/pkg/mcp"
	"os"
	"strings"
	"testing"
)

func TestLLMWikiFeatureE2E(t *testing.T) {
	// Setup DB in temp file to isolate tests
	dbPath := "test_m_cpg_wiki.db"
	os.Remove(dbPath)
	defer os.Remove(dbPath)

	cfg := config.LoadDefaultConfig()
	cfg.DBPath = dbPath
	gdb, err := db.InitDB(cfg.DBPath, cfg.Embedding.Dimension)
	if err != nil {
		t.Fatalf("Failed to initialize DB: %v", err)
	}
	defer gdb.Close()

	projectID := "test_wiki_project"

	// 1. Test m_cpg_remember to ingest initial knowledge
	err = mcp.RunRemember("bug_fix", "Bug fix 1", "Fixed a bug in auth with emoji: 😊", 8, projectID, gdb, cfg)
	if err != nil {
		t.Fatalf("Failed to remember event 1: %v", err)
	}

	err = mcp.RunRemember("feature", "OAuth2", "Added OAuth2 support for logging in.", 8, projectID, gdb, cfg)
	if err != nil {
		t.Fatalf("Failed to remember event 2: %v", err)
	}

	events, err := gdb.GetAllActiveEvents(projectID)
	if err != nil {
		t.Fatalf("Failed to get events: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("Expected 2 events, got %d", len(events))
	}

	// 2. Test m_cpg_lint_memory
	// Set access time in the past directly via db
	executor := gdb.GetDB()
	_, err = executor.Exec("UPDATE events SET last_accessed = 0 WHERE project_id = ?", projectID)
	if err != nil {
		t.Fatalf("Failed to update last_accessed: %v", err)
	}

	lintMsg, err := mcp.RunLintMemory(30, projectID, gdb)
	if err != nil {
		t.Fatalf("Failed to run lint memory: %v", err)
	}
	if !strings.Contains(lintMsg, "Memory Lint Report: Found 2 stale events") {
		t.Errorf("Lint memory did not find stale events correctly: %s", lintMsg)
	}

	// 3. Test m_cpg_synthesize_memories
	eventIDs := []string{}
	for _, e := range events {
		eventIDs = append(eventIDs, e["id"].(string))
	}

	synthMsg, err := mcp.RunSynthesizeMemories(eventIDs, "Auth Overhaul", "Synthesized auth and oauth2 features. Including non-ascii é.", projectID, gdb, cfg)
	if err != nil {
		t.Fatalf("Failed to synthesize memories: %v", err)
	}
	if !strings.Contains(synthMsg, "Successfully saved synthesized Wiki memory") {
		t.Errorf("Synthesize memories output unexpected: %s", synthMsg)
	}

	eventsAfterSynth, err := gdb.GetAllActiveEvents(projectID)
	if err != nil {
		t.Fatalf("Failed to get events after synthesis: %v", err)
	}
	if len(eventsAfterSynth) != 1 {
		t.Fatalf("Expected 1 active event after synthesis, got %d", len(eventsAfterSynth))
	}
	if eventsAfterSynth[0]["event_type"].(string) != "synthesis" {
		t.Errorf("Expected event_type to be synthesis, got %s", eventsAfterSynth[0]["event_type"])
	}

	// 4. Test m_cpg_generate_wiki
	// Use a temporary directory for memory bank output
	testDir := "test_memory_bank_workspace"
	os.MkdirAll(testDir, 0755)
	defer os.RemoveAll(testDir)

	wikiMsg, err := mcp.RunGenerateWiki(testDir, projectID, gdb)
	if err != nil {
		t.Fatalf("Failed to generate wiki: %v", err)
	}
	if !strings.Contains(wikiMsg, "Successfully generated memory_index.md and memory_log.md") {
		t.Errorf("Generate wiki output unexpected: %s", wikiMsg)
	}

	// Verify files created
	indexContent, err := os.ReadFile(testDir + "/memory-bank/memory_index.md")
	if err != nil {
		t.Fatalf("Failed to read memory_index.md: %v", err)
	}
	if !strings.Contains(string(indexContent), "Auth Overhaul") {
		t.Errorf("memory_index.md missing synthesized content: %s", string(indexContent))
	}

	logContent, err := os.ReadFile(testDir + "/memory-bank/memory_log.md")
	if err != nil {
		t.Fatalf("Failed to read memory_log.md: %v", err)
	}
	if !strings.Contains(string(logContent), "Auth Overhaul") {
		t.Errorf("memory_log.md missing synthesized content: %s", string(logContent))
	}
}
