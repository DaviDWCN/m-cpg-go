package parser

import (
	"fmt"
	"path/filepath"
	"strings"
)

type CodeEntity struct {
	ID        string
	Type      string // "Module", "Class", "Method"
	Name      string
	FQN       string
	Code      string
	Docstring string
	ParentID  string
}

type CodeRelation struct {
	Source string
	Target string
	Label  string // "CONTAINS", "CALLS"
}

// DeriveFQN extracts the package/module name based on the file path relative to the source directory
func DeriveFQN(filePath, srcDir string) string {
	relPath, err := filepath.Rel(srcDir, filePath)
	if err != nil {
		relPath = filePath
	}

	// Normalize separators to "/"
	relPath = filepath.ToSlash(relPath)
	ext := filepath.Ext(relPath)
	modulePath := strings.TrimSuffix(relPath, ext)

	// Replace "/" with "."
	parts := strings.Split(modulePath, "/")
	if parts[len(parts)-1] == "__init__" {
		parts = parts[:len(parts)-1]
	}

	return strings.Join(parts, ".")
}

// ParseFile parses a file structurally depending on its extension
func ParseFile(filePath, projectID, srcDir string) ([]CodeEntity, []CodeRelation, error) {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".py":
		return ParsePythonFile(filePath, projectID, srcDir)
	case ".go":
		return ParseGoFile(filePath, projectID, srcDir)
	case ".md":
		return ParseMarkdownFile(filePath, projectID, srcDir)
	default:
		return nil, nil, fmt.Errorf("unsupported file extension: %s", ext)
	}
}
