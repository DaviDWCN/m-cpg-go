package mcp

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"m-cpg-go/pkg/concept"
	"m-cpg-go/pkg/config"
	"m-cpg-go/pkg/db"
	"m-cpg-go/pkg/vector"
)

// RunLintMemory retrieves events that haven't been accessed for the specified number of days and flags them for AI review.
func RunLintMemory(daysStale int, projectID string, gdb *db.GraphDB) (string, error) {
	staleThreshold := time.Now().AddDate(0, 0, -daysStale).Unix()

	events, err := gdb.GetStaleEvents(projectID, staleThreshold)
	if err != nil {
		return "", fmt.Errorf("failed to fetch stale events: %w", err)
	}

	if len(events) == 0 {
		return fmt.Sprintf("Good news! No stale memories found older than %d days.", daysStale), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Memory Lint Report: Found %d stale events not accessed in over %d days.\n", len(events), daysStale))
	sb.WriteString("========================================================================\n\n")

	for i, ev := range events {
		id := ev["id"].(string)
		eventType := ev["event_type"].(string)
		summary := ev["summary"].(string)
		details := ev["details"].(string)
		lastAccessed := time.Unix(ev["last_accessed"].(int64), 0).Format("2006-01-02")

		sb.WriteString(fmt.Sprintf("%d. [ID: %s] Type: %s (Last Accessed: %s)\n", i+1, id, eventType, lastAccessed))
		sb.WriteString(fmt.Sprintf("   Summary: %s\n", summary))
		if len(details) > 150 {
			sb.WriteString(fmt.Sprintf("   Details: %s...\n", strings.ReplaceAll(truncateString(details, 150), "\n", " ")))
		} else {
			sb.WriteString(fmt.Sprintf("   Details: %s\n", strings.ReplaceAll(details, "\n", " ")))
		}
		sb.WriteString("------------------------------------------------------------------------\n")
	}

	sb.WriteString("\nAction Required: Please review these events. If they are outdated (knowledge rot), use `m_cpg_consolidate_memories` or `m_cpg_synthesize_memories` to deprecate/archive them, or manually write a new memory to supersede them.")

	return sb.String(), nil
}

// RunConsolidateMemories archives the provided event IDs and returns a message to the agent to write the insight.
func RunConsolidateMemories(eventIDs []string, insight string, gdb *db.GraphDB) (string, error) {
	if len(eventIDs) == 0 {
		return "No event IDs provided for consolidation.", nil
	}

	if err := gdb.ArchiveEvents(nil, eventIDs); err != nil {
		return "", fmt.Errorf("failed to archive events: %w", err)
	}

	// Extract and save concepts from the insight
	concepts := concept.ExtractConcepts(insight)
	if err := gdb.SaveConcepts(nil, "", concepts); err != nil {
		fmt.Fprintf(os.Stderr, "[MCP] Warning: Failed to save concepts for insight: %v\n", err)
	}

	var sb strings.Builder
	sb.WriteString("Successfully archived the following fragmented events:\n")
	for _, id := range eventIDs {
		sb.WriteString(fmt.Sprintf("- %s\n", id))
	}
	sb.WriteString("\n")
	sb.WriteString("Consolidated Insight:\n")
	sb.WriteString(insight)
	sb.WriteString("\n\n")
	sb.WriteString("Next steps for Agent: Please append this consolidated insight into 'memory-bank/systemPatterns.md' or 'memory-bank/activeContext.md' using standard file editing tools to ensure it is kept in the working memory.")

	return sb.String(), nil
}

// memoryScoreThreshold drops near-zero cosine hits so a search does not
// refresh last_accessed on unrelated memories.
const memoryScoreThreshold float32 = 0.20

// RunSearchMemory retrieves memories related to the query using vector similarity.
func RunSearchMemory(query string, limit int, projectID string, gdb *db.GraphDB, cfg *config.Config) (string, error) {
	if err := VerifyEmbeddingConfig(gdb, cfg); err != nil {
		return "", err
	}
	emb, src, err := vector.GetEmbedding(
		query,
		cfg.Embedding.Provider,
		cfg.Embedding.Model,
		cfg.Embedding.Endpoint,
		cfg.Embedding.APIKey,
		cfg.Embedding.Dimension,
	)
	if err != nil {
		return "", fmt.Errorf("failed to embed query: %w", err)
	}
	return runSearchMemoryEmbedded(query, emb, src, limit, projectID, gdb)
}

type scoredMemory struct {
	id    string
	score float32
}

func runSearchMemoryEmbedded(query string, emb []float32, src vector.EmbeddingSource, limit int, projectID string, gdb *db.GraphDB) (string, error) {
	if limit <= 0 {
		return "No active memories found matching the query.", nil
	}
	scores := make(map[string]float32)
	if src == vector.EmbeddingSourceAPI {
		hits, err := gdb.SearchEventVectors(projectID, vector.Float32SliceToBytes(emb), limit)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[memory] vector search: %v\n", err)
			return searchMemoryScan(query, emb, limit, projectID, gdb)
		}
		for _, hit := range hits {
			scores[hit.ID] = 1 - hit.Distance
		}
	}
	unindexed, err := gdb.ListUnindexedEventEmbeddings(projectID)
	if err != nil {
		return "", fmt.Errorf("failed to retrieve active events: %w", err)
	}
	for _, ev := range unindexed {
		evEmb := vector.BytesToFloat32Slice(ev.Embedding)
		score := vector.CosineSimilarity(emb, evEmb)
		if prev, ok := scores[ev.ID]; !ok || score > prev {
			scores[ev.ID] = score
		}
	}
	return formatScoredMemories(query, scores, limit, gdb)
}

// searchMemoryScan scores every stored blob. It is the fallback when the
// event vector index cannot be queried.
func searchMemoryScan(query string, emb []float32, limit int, projectID string, gdb *db.GraphDB) (string, error) {
	events, err := gdb.GetEventsForSearch(projectID)
	if err != nil {
		return "", fmt.Errorf("failed to retrieve active events: %w", err)
	}
	scores := make(map[string]float32)
	for _, ev := range events {
		embBytes, ok := ev["embedding"].([]byte)
		if !ok {
			continue
		}
		id, _ := ev["id"].(string)
		if id == "" {
			continue
		}
		scores[id] = vector.CosineSimilarity(emb, vector.BytesToFloat32Slice(embBytes))
	}
	return formatScoredMemories(query, scores, limit, gdb)
}

func formatScoredMemories(query string, scores map[string]float32, limit int, gdb *db.GraphDB) (string, error) {
	ranked := make([]scoredMemory, 0, len(scores))
	for id, score := range scores {
		if score >= memoryScoreThreshold {
			ranked = append(ranked, scoredMemory{id: id, score: score})
		}
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score == ranked[j].score {
			return ranked[i].id < ranked[j].id
		}
		return ranked[i].score > ranked[j].score
	})
	if limit > len(ranked) {
		limit = len(ranked)
	}
	ranked = ranked[:limit]
	if len(ranked) == 0 {
		return "No active memories found matching the query.", nil
	}

	ids := make([]string, len(ranked))
	for i, se := range ranked {
		ids[i] = se.id
	}
	if err := gdb.UpdateEventAccess(nil, ids, time.Now().Unix()); err != nil {
		fmt.Fprintf(os.Stderr, "[MCP] Warning: Failed to update event access times: %v\n", err)
	}
	texts, err := gdb.GetEventTexts(ids)
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d memories matching '%s':\n\n", len(ranked), query))
	for i, se := range ranked {
		ev := texts[se.id]
		sb.WriteString(fmt.Sprintf("%d. [Score: %.3f] (ID: %s, Type: %s)\n", i+1, se.score, se.id, ev.EventType))
		sb.WriteString(fmt.Sprintf("   Summary: %s\n", ev.Summary))
		if ev.Details != "" {
			details := ev.Details
			if len(details) > 300 {
				details = truncateString(details, 300) + "..."
			}
			sb.WriteString(fmt.Sprintf("   Details: %s\n", strings.ReplaceAll(details, "\n", "\n   ")))
		}
		sb.WriteString("\n")
	}
	return sb.String(), nil
}

// RunRemember saves a developer preference, compilation fix, or session log to the DB and generates vector embedding.
func RunRemember(summary, details, eventType string, importance int, projectID string, gdb *db.GraphDB, cfg *config.Config) error {
	if err := VerifyEmbeddingConfig(gdb, cfg); err != nil {
		return err
	}
	id := uuid.New().String()
	timestamp := time.Now().Unix()

	// Embed summary and details combined
	embedText := fmt.Sprintf("[%s] %s\n%s", eventType, summary, details)
	emb, src, err := vector.GetEmbedding(
		embedText,
		cfg.Embedding.Provider,
		cfg.Embedding.Model,
		cfg.Embedding.Endpoint,
		cfg.Embedding.APIKey,
		cfg.Embedding.Dimension,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[MCP] Warning: Failed to embed memory: %v. Falling back to pseudo-embedding.\n", err)
		emb = vector.GeneratePseudoEmbedding(embedText, cfg.Embedding.Dimension)
		src = vector.EmbeddingSourcePseudo
	}

	embBytes := vector.Float32SliceToBytes(emb)
	err = gdb.SaveEvent(nil, id, eventType, summary, details, timestamp, embBytes, "active", importance, projectID, string(src))
	if err != nil {
		return fmt.Errorf("failed to save memory to DB: %w", err)
	}

	// Extract and save abstract concepts
	concepts := concept.ExtractConcepts(summary + " " + details)
	if err := gdb.SaveConcepts(nil, id, concepts); err != nil {
		fmt.Fprintf(os.Stderr, "[MCP] Warning: Failed to save concepts for memory %s: %v\n", id, err)
	}

	fmt.Fprintf(os.Stderr, "[MCP] Successfully remembered event: %s\n", summary)
	return nil
}

// RunGetConceptHierarchy retrieves top frequency concepts from the database
func RunGetConceptHierarchy(limit int, gdb *db.GraphDB) (string, error) {
	concepts, err := gdb.GetTopConcepts(limit)
	if err != nil {
		return "", err
	}

	if len(concepts) == 0 {
		return "No extracted concepts found in the knowledge hierarchy.", nil
	}

	var sb strings.Builder
	sb.WriteString("High-Level Knowledge Hierarchy (Top Abstract Concepts):\n")
	sb.WriteString("=========================================================\n")

	for i, c := range concepts {
		name := c["name"].(string)
		frequency := c["frequency"].(int)
		sb.WriteString(fmt.Sprintf("%d. %s (Frequency: %d)\n", i+1, name, frequency))
	}

	return sb.String(), nil
}

// RunIngestConversation ingests a transcript chunk as an event (Cold Layer proxy)
func RunIngestConversation(gdb *db.GraphDB, cfg *config.Config, transcript, summary string, projectID string) (string, error) {
	if err := VerifyEmbeddingConfig(gdb, cfg); err != nil {
		return "", err
	}
	err := gdb.RunInTransaction(func(tx *sql.Tx) error {
		id := "conv_" + uuid.New().String()
		now := time.Now().Unix()
		details := fmt.Sprintf("Conversation Transcript:\n%s", transcript)

		// Create vector embedding for the transcript summary
		embText := summary + "\n" + transcript
		emb, src, err := vector.GetEmbedding(embText, cfg.Embedding.Provider, cfg.Embedding.Model, cfg.Embedding.Endpoint, cfg.Embedding.APIKey, cfg.Embedding.Dimension)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[MCP] Warning: Failed to generate embedding for conversation: %v. Falling back to pseudo-embedding.\n", err)
			emb = vector.GeneratePseudoEmbedding(embText, cfg.Embedding.Dimension)
			src = vector.EmbeddingSourcePseudo
		}
		var embBytes []byte
		if emb != nil {
			embBytes = vector.Float32SliceToBytes(emb)
		}

		// Save as an event with type 'conversation', importance 2 (default cold layer)
		err = gdb.SaveEvent(tx, id, "conversation", summary, details, now, embBytes, "archived", 2, projectID, string(src))
		if err != nil {
			return err
		}
		return nil
	})

	if err != nil {
		return "", err
	}

	return "Conversation successfully ingested into the Cold memory layer.", nil
}

// RunGetHotContext queries the most recent 5 active events and top 5 concepts to generate a dynamic hot context index
func RunGetHotContext(gdb *db.GraphDB, projectID string) (string, error) {
	events, err := gdb.GetRecentActiveEvents(5, projectID)
	if err != nil {
		return "", err
	}

	concepts, err := gdb.GetTopConcepts(5)
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	sb.WriteString("# AI Agent Hot Context (Dynamic Index)\n\n")

	sb.WriteString("## Active Entities / Concepts\n")
	if len(concepts) > 0 {
		for _, c := range concepts {
			name := c["name"].(string)
			frequency := c["frequency"].(int)
			sb.WriteString(fmt.Sprintf("- **%s** (Freq: %d)\n", name, frequency))
		}
	} else {
		sb.WriteString("No active concepts found.\n")
	}
	sb.WriteString("\n")

	sb.WriteString("## Current Tasks / Unresolved Issues\n")
	if len(events) > 0 {
		for i, ev := range events {
			t := time.Unix(ev["timestamp"].(int64), 0).Format("2006-01-02 15:04:05")

			// Render fire emoji for high importance events
			importanceLabel := ""
			if imp, ok := ev["importance"].(int); ok && imp >= 5 {
				importanceLabel = " 🔥"
			} else if imp, ok := ev["importance"].(int64); ok && imp >= 5 {
				importanceLabel = " 🔥"
			}

			sb.WriteString(fmt.Sprintf("%d. **[%s]** %s (%s)%s\n", i+1, ev["event_type"], ev["summary"], t, importanceLabel))
			if details := ev["details"].(string); details != "" {
				lines := strings.Split(details, "\n")
				for _, line := range lines {
					sb.WriteString(fmt.Sprintf("   > %s\n", line))
				}
			}
		}
	} else {
		sb.WriteString("No unresolved tasks found.\n")
	}

	return sb.String(), nil
}

// RunGetPreferences queries the most recent 10 events from memory
func RunGetPreferences(gdb *db.GraphDB, projectID string) (string, error) {
	events, err := gdb.GetRecentEvents(10, projectID)
	if err != nil {
		return "", err
	}

	if len(events) == 0 {
		return "No persistent preferences or debug event memories found in agent database.", nil
	}

	var sb strings.Builder
	sb.WriteString("Persistent Developer Preferences & Recent Fixes:\n")
	sb.WriteString("==================================================\n")

	for i, ev := range events {
		t := time.Unix(ev["timestamp"].(int64), 0).Format("2006-01-02 15:04:05")
		sb.WriteString(fmt.Sprintf("%d. [%s] %s (%s)\n", i+1, ev["event_type"], ev["summary"], t))
		if details := ev["details"].(string); details != "" {
			// Print details indented
			lines := strings.Split(details, "\n")
			for _, line := range lines {
				sb.WriteString(fmt.Sprintf("   %s\n", line))
			}
		}
		sb.WriteString("--------------------------------------------------\n")
	}

	return sb.String(), nil
}

// RunGenerateWiki generates memory_index.md and memory_log.md in the project's memory-bank directory
func RunGenerateWiki(projectPath string, projectID string, gdb *db.GraphDB) (string, error) {
	mbDir := filepath.Join(projectPath, "memory-bank")
	if err := os.MkdirAll(mbDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create memory-bank directory: %w", err)
	}

	// 1. Generate memory_index.md (Content-oriented)
	concepts, err := gdb.GetTopConcepts(50)
	if err != nil {
		return "", fmt.Errorf("failed to get top concepts: %w", err)
	}

	events, err := gdb.GetAllActiveEvents(projectID)
	if err != nil {
		return "", fmt.Errorf("failed to get events: %w", err)
	}

	indexFile := filepath.Join(mbDir, "memory_index.md")
	var idxSb strings.Builder
	idxSb.WriteString("# Memory Index\n\nThis is a content-oriented catalog of everything the AI agent has learned, organized by abstract concepts.\n\n")

	idxSb.WriteString("## High-Level Concepts\n\n")
	for _, c := range concepts {
		name := c["name"].(string)
		frequency := c["frequency"].(int)
		idxSb.WriteString(fmt.Sprintf("- **%s** (Mentions: %d)\n", name, frequency))
	}
	idxSb.WriteString("\n## Memory Catalog\n\n")

	// Group events by type for index
	eventsByType := make(map[string][]map[string]any)
	for _, ev := range events {
		eventType := ev["event_type"].(string)
		eventsByType[eventType] = append(eventsByType[eventType], ev)
	}

	for eType, evs := range eventsByType {
		idxSb.WriteString(fmt.Sprintf("### %s\n", strings.Title(eType)))
		for _, ev := range evs {
			summary := ev["summary"].(string)
			timestamp := time.Unix(ev["timestamp"].(int64), 0).Format("2006-01-02")
			impStr := ""
			if imp, ok := ev["importance"].(int); ok && imp >= 5 {
				impStr = " 🔥"
			} else if imp64, ok := ev["importance"].(int64); ok && imp64 >= 5 {
				impStr = " 🔥"
			}
			idxSb.WriteString(fmt.Sprintf("- [%s] %s%s\n", timestamp, summary, impStr))
		}
		idxSb.WriteString("\n")
	}

	if err := os.WriteFile(indexFile, []byte(idxSb.String()), 0644); err != nil {
		return "", fmt.Errorf("failed to write memory_index.md: %w", err)
	}

	// 2. Generate memory_log.md (Chronological)
	// Sort all events by timestamp descending
	sort.Slice(events, func(i, j int) bool {
		return events[i]["timestamp"].(int64) > events[j]["timestamp"].(int64)
	})

	logFile := filepath.Join(mbDir, "memory_log.md")
	var logSb strings.Builder
	logSb.WriteString("# Memory Log\n\nAn append-only chronological record of operations and memories.\n\n")

	for _, ev := range events {
		timestamp := time.Unix(ev["timestamp"].(int64), 0).Format("2006-01-02 15:04:05")
		eventType := ev["event_type"].(string)
		summary := ev["summary"].(string)
		details := ev["details"].(string)

		logSb.WriteString(fmt.Sprintf("## [%s] %s | %s\n", timestamp, eventType, summary))
		if details != "" {
			logSb.WriteString(fmt.Sprintf("%s\n\n", details))
		}
	}

	if err := os.WriteFile(logFile, []byte(logSb.String()), 0644); err != nil {
		return "", fmt.Errorf("failed to write memory_log.md: %w", err)
	}

	return fmt.Sprintf("Successfully generated memory_index.md and memory_log.md in %s", mbDir), nil
}

// RunSynthesizeMemories archives old fragmented events and saves a new high-importance synthesis event.
func RunSynthesizeMemories(eventIDs []string, summary, synthesis, projectID string, gdb *db.GraphDB, cfg *config.Config) (string, error) {
	if err := VerifyEmbeddingConfig(gdb, cfg); err != nil {
		return "", err
	}

	// Archive old events
	if len(eventIDs) > 0 {
		if err := gdb.ArchiveEvents(nil, eventIDs); err != nil {
			return "", fmt.Errorf("failed to archive old fragmented events: %w", err)
		}
	}

	// Create new synthesis event
	id := uuid.New().String()
	timestamp := time.Now().Unix()
	eventType := "synthesis"
	importance := 8 // High importance for synthesized knowledge

	embedText := fmt.Sprintf("[%s] %s\n%s", eventType, summary, synthesis)
	emb, src, err := vector.GetEmbedding(
		embedText,
		cfg.Embedding.Provider,
		cfg.Embedding.Model,
		cfg.Embedding.Endpoint,
		cfg.Embedding.APIKey,
		cfg.Embedding.Dimension,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[MCP] Warning: Failed to embed synthesis: %v. Falling back to pseudo-embedding.\n", err)
		emb = vector.GeneratePseudoEmbedding(embedText, cfg.Embedding.Dimension)
		src = vector.EmbeddingSourcePseudo
	}

	embBytes := vector.Float32SliceToBytes(emb)
	err = gdb.SaveEvent(nil, id, eventType, summary, synthesis, timestamp, embBytes, "active", importance, projectID, string(src))
	if err != nil {
		return "", fmt.Errorf("failed to save synthesized memory to DB: %w", err)
	}

	// Extract and save abstract concepts
	concepts := concept.ExtractConcepts(summary + " " + synthesis)
	if err := gdb.SaveConcepts(nil, id, concepts); err != nil {
		fmt.Fprintf(os.Stderr, "[MCP] Warning: Failed to save concepts for synthesis %s: %v\n", id, err)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Successfully saved synthesized Wiki memory '%s' (ID: %s).\n", summary, id))
	if len(eventIDs) > 0 {
		sb.WriteString(fmt.Sprintf("Archived %d deprecated fragmented events.\n", len(eventIDs)))
	}
	return sb.String(), nil
}
