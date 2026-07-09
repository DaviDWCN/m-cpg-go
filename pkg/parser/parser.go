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
	Source     string
	Target     string
	Label      string         // "CONTAINS", "CALLS"
	Properties map[string]any // Used for things like weights
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
	case ".ts", ".tsx", ".js", ".jsx":
		return ParseTSFile(filePath, projectID, srcDir)
	default:
		return nil, nil, fmt.Errorf("unsupported file extension: %s", ext)
	}
}

// AggregateRelations groups relations by Source, Target, and Label, summing their weights
func AggregateRelations(relations []CodeRelation) []CodeRelation {
	type key struct {
		Source string
		Target string
		Label  string
	}

	agg := make(map[key]*CodeRelation)
	var orderedKeys []key

	for _, rel := range relations {
		k := key{Source: rel.Source, Target: rel.Target, Label: rel.Label}
		if existing, found := agg[k]; found {
			// Increment weight if both are CALLS or if it already has a weight
			if rel.Label == "CALLS" {
				var w int = 1
				if existing.Properties != nil && existing.Properties["weight"] != nil {
					if currentW, ok := existing.Properties["weight"].(int); ok {
						w = currentW + 1
					}
				}
				if existing.Properties == nil {
					existing.Properties = make(map[string]any)
				}
				existing.Properties["weight"] = w
			}
		} else {
			orderedKeys = append(orderedKeys, k)
			newRel := rel
			if newRel.Label == "CALLS" {
				if newRel.Properties == nil {
					newRel.Properties = make(map[string]any)
				}
				newRel.Properties["weight"] = 1
			}
			agg[k] = &newRel
		}
	}

	var result []CodeRelation
	for _, k := range orderedKeys {
		result = append(result, *agg[k])
	}

	return result
}
