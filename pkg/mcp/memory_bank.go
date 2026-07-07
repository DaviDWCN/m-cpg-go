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
		"projectbrief.md":   "# Project Brief\n\nCore requirements, scope and goals of the project.\n",
		"productContext.md": "# Product Context\n\nBusiness background, user pain points and UX considerations.\n",
		"systemPatterns.md": "# System Patterns\n\nArchitecture design, core design patterns, technical decisions, and component relationships.\n",
		"techContext.md":    "# Technical Context\n\nTech stack, dependencies, local environment constraints.\n",
		"activeContext.md":  "# Active Context\n\nCurrent development focus, recent changes, and next steps.\n",
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
