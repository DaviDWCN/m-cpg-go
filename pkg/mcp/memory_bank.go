package mcp

import (
	"fmt"
	"os"
	"path/filepath"
)

// RunInitMemoryBank creates the standard Memory Bank directory and markdown files.
func RunInitMemoryBank(projectPath string) (string, error) {
	mbDir := filepath.Join(projectPath, "memory-bank")

	if err := os.MkdirAll(mbDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create memory-bank directory: %w", err)
	}

	filesToCreate := map[string]string{
		"projectbrief.md":   "# Project Brief\n\nCore requirements, scope and goals of the project.\n\n> **[AI Agent Trigger]**\n> Before finalizing any new feature planning, you MUST execute `m_cpg_search` to verify if similar concepts or requirements have already been documented or implemented in the past.\n",
		"productContext.md": "# Product Context\n\nBusiness background, user pain points and UX considerations.\n",
		"systemPatterns.md": "# System Patterns\n\nArchitecture design, core design patterns, technical decisions, and component relationships.\n\n> **[AI Agent Trigger]**\n> Whenever you propose a change to the core architecture, you MUST first use `m_cpg_get_node_dependencies` to assess the structural blast radius.\n> If the architecture evolves significantly, you MUST use `m_cpg_synthesize_memories` to curate these changes into a unified wiki-style memory and `m_cpg_lint_memory` to clean up stale knowledge.\n",
		"techContext.md":    "# Technical Context\n\nTech stack, dependencies, local environment constraints.\n",
		"activeContext.md":  "# Active Context\n\nCurrent development focus, recent changes, and next steps.\n\n> **[AI Agent Trigger]**\n> At the end of your session or upon task completion, you MUST invoke `m_cpg_ingest_conversation` to store this context into episodic memory.\n> When making a significant decision during this task, use `m_cpg_remember` to record it.\n> Once in a while, run `m_cpg_generate_wiki` to update the visual `memory_index.md` and `memory_log.md`.\n",
		"progress.md":       "# Progress\n\nCompleted, in-progress, and known defects/todo lists.\n",
	}

	createdCount := 0
	for filename, defaultContent := range filesToCreate {
		fullPath := filepath.Join(mbDir, filename)
		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			if err := os.WriteFile(fullPath, []byte(defaultContent), 0644); err != nil {
				return "", fmt.Errorf("failed to create file %s: %w", filename, err)
			}
			createdCount++
		}
	}

	msg := fmt.Sprintf("Successfully initialized Memory Bank at %s\nCreated %d new files.", mbDir, createdCount)
	return msg, nil
}
