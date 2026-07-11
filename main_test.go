package main

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

func TestE2ECLI(t *testing.T) {
	// Build the binary for testing
	binaryName := "m-cpg-go-e2e-test"
	if runtime.GOOS == "windows" {
		binaryName += ".exe"
	}

	cmd := exec.Command("go", "build", "-o", binaryName, ".")
	err := cmd.Run()
	if err != nil {
		t.Fatalf("Failed to build binary for E2E test: %v", err)
	}
	// Clean up before starting to ensure clean DB environment
	os.Remove("m_cpg.db")

	// Clean up after tests
	defer func() {
		os.Remove(binaryName)
		os.Remove("m_cpg.db") // Default database created by app
		os.RemoveAll("e2e-test-bank") // Cleanup any generated bank
	}()

	binPath := "./" + binaryName
	if runtime.GOOS == "windows" {
		binPath = ".\\" + binaryName
	}

	t.Run("Index", func(t *testing.T) {
		cmd := exec.Command(binPath, "index", ".")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to run index command: %v\nOutput: %s", err, string(output))
		}
		if !strings.Contains(string(output), "SUCCESS: Indexing finished!") {
			t.Errorf("Index output missing expected success message. Output: %s", string(output))
		}

		// Run again to test incremental indexing
		cmd2 := exec.Command(binPath, "index", ".")
		output2, err := cmd2.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to run incremental index: %v\nOutput: %s", err, string(output2))
		}
		if !strings.Contains(string(output2), "Graph Nodes Created: 0") || !strings.Contains(string(output2), "Relationships Created: 0") {
			t.Errorf("Incremental index failed to skip unchanged files. Output: %s", string(output2))
		}
	})

	t.Run("Search", func(t *testing.T) {
		// Needs to be run after indexing has populated DB
		cmd := exec.Command(binPath, "search", "SQLite")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to run search command: %v\nOutput: %s", err, string(output))
		}
		if !strings.Contains(string(output), "Hybrid Search Results") {
			t.Errorf("Search output missing expected results header. Output: %s", string(output))
		}
	})

	t.Run("InitBank", func(t *testing.T) {
		cmd := exec.Command(binPath, "init-bank", "e2e-test-bank")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to run init-bank command: %v\nOutput: %s", err, string(output))
		}
		if !strings.Contains(string(output), "Successfully initialized Memory Bank") {
			t.Errorf("Init-bank output missing expected success message. Output: %s", string(output))
		}
	})

	t.Run("Help", func(t *testing.T) {
		cmd := exec.Command(binPath, "help")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to run help command: %v\nOutput: %s", err, string(output))
		}
		if !strings.Contains(string(output), "Usage:") {
			t.Errorf("Help output missing expected 'Usage:' string. Output: %s", string(output))
		}
	})
}
