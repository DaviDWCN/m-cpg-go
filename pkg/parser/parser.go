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
	FilePath  string
}

type CodeRelation struct {
	Source     string
	Target     string
	Label      string         // "CONTAINS", "CALLS"
	Properties map[string]any // Used for things like weights
	// Callee is the unresolved call-site name. Target is empty until a unique
	// symbol in this file (or a later project pass) can be bound.
	Callee string
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
	var (
		entities  []CodeEntity
		relations []CodeRelation
		err       error
	)
	switch ext {
	case ".py":
		entities, relations, err = ParsePythonFile(filePath, projectID, srcDir)
	case ".go":
		entities, relations, err = ParseGoFile(filePath, projectID, srcDir)
	case ".md":
		entities, relations, err = ParseMarkdownFile(filePath, projectID, srcDir)
	case ".ts", ".tsx", ".js", ".jsx":
		entities, relations, err = ParseTSFile(filePath, projectID, srcDir)
	case ".java":
		entities, relations, err = ParseJavaFile(filePath, projectID, srcDir)
	default:
		return nil, nil, fmt.Errorf("unsupported file extension: %s", ext)
	}
	if err != nil {
		return nil, nil, err
	}
	entities, relations = ResolveCallTargets(entities, relations)
	return entities, relations, nil
}

// ResolveCallTargets binds CALLS edges to a method or class in the same file
// when the callee name matches exactly one symbol. Unresolved calls keep the
// callee name and an empty target so the indexer can retry across the project
// without inventing placeholder nodes.
func ResolveCallTargets(entities []CodeEntity, relations []CodeRelation) ([]CodeEntity, []CodeRelation) {
	byName := make(map[string][]string)
	for _, ent := range entities {
		if ent.Type != "Method" && ent.Type != "Class" {
			continue
		}
		byName[ent.Name] = append(byName[ent.Name], ent.ID)
	}

	resolved := make([]CodeRelation, 0, len(relations))
	for _, rel := range relations {
		if rel.Label != "CALLS" {
			resolved = append(resolved, rel)
			continue
		}
		callee := rel.Callee
		if callee == "" && strings.HasPrefix(rel.Target, "call_") {
			callee = strings.TrimPrefix(rel.Target, "call_")
		}
		if callee == "" && rel.Target != "" && !strings.HasPrefix(rel.Target, "call_") {
			resolved = append(resolved, rel)
			continue
		}

		simple := callee
		if i := strings.LastIndex(callee, "."); i >= 0 {
			simple = callee[i+1:]
		}

		var ids []string
		if strings.Contains(callee, ".") {
			for _, ent := range entities {
				if ent.Type != "Method" && ent.Type != "Class" {
					continue
				}
				if ent.FQN == callee || strings.HasSuffix(ent.FQN, "."+callee) {
					ids = append(ids, ent.ID)
				}
			}
		}
		if len(ids) == 0 {
			ids = byName[simple]
		}
		ids = uniqueIDs(ids)
		if len(ids) == 1 && ids[0] != rel.Source {
			rel.Target = ids[0]
			rel.Callee = ""
		} else {
			rel.Target = ""
			rel.Callee = callee
		}
		resolved = append(resolved, rel)
	}
	return entities, resolved
}

func uniqueIDs(ids []string) []string {
	if len(ids) < 2 {
		return ids
	}
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
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
