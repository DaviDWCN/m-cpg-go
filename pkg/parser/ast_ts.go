package parser

import (
	"bufio"
	"os"
	"regexp"
	"strings"
)

var (
	// Very simple regexes for TS/JS structural parsing
	tsClassRegex = regexp.MustCompile(`^\s*(?:export\s+)?(?:default\s+)?class\s+([a-zA-Z0-9_]+)`)
	tsFuncRegex  = regexp.MustCompile(`^\s*(?:export\s+)?(?:default\s+)?(?:async\s+)?function\s+([a-zA-Z0-9_]+)`)
	tsArrowFuncRegex = regexp.MustCompile(`^\s*(?:export\s+)?const\s+([a-zA-Z0-9_]+)\s*=\s*(?:async\s+)?(?:\([^)]*\)|[a-zA-Z0-9_]+)\s*=>`)
	tsMethodRegex = regexp.MustCompile(`^\s*(?:public\s+|private\s+|protected\s+)?(?:async\s+)?([a-zA-Z0-9_]+)\s*\([^)]*\)\s*(?::\s*[^{]+)?\{`)
)

type tsScope struct {
	id         string
	fqn        string
	indent     int
	entityType string // "Class" or "Method"
}

// ParseTSFile extracts modules, classes, and functions structurally from a TS/JS file
func ParseTSFile(filePath, projectID, srcDir string) ([]CodeEntity, []CodeRelation, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()

	moduleFqn := DeriveFQN(filePath, srcDir)
	moduleID := "module_" + moduleFqn

	var entities []CodeEntity
	var relations []CodeRelation

	// Read all lines to easily slice code blocks
	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, err
	}

	// Insert module node
	entities = append(entities, CodeEntity{
		ID:        moduleID,
		Type:      "Module",
		Name:      filepathBase(moduleFqn),
		FQN:       moduleFqn,
		Code:      strings.Join(lines, "\n"),
		Docstring: "", // Simplified docstring parsing for TS
		ParentID:  "",
	})

	scopeStack := []tsScope{
		{id: moduleID, fqn: moduleFqn, indent: -1, entityType: "Module"},
	}

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		// Skip empty lines or pure comment lines
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}

		indent := countIndentation(line)

		// Pop scopes that are no longer active based on indentation
		for len(scopeStack) > 1 && scopeStack[len(scopeStack)-1].indent >= indent {
			scopeStack = scopeStack[:len(scopeStack)-1]
		}

		parentScope := scopeStack[len(scopeStack)-1]

		if matches := tsClassRegex.FindStringSubmatch(line); len(matches) > 1 {
			className := matches[1]
			classFqn := parentScope.fqn + "." + className
			classID := "class_" + classFqn

			classCode := extractBlockCodeTS(lines, i, indent)

			entities = append(entities, CodeEntity{
				ID:        classID,
				Type:      "Class",
				Name:      className,
				FQN:       classFqn,
				Code:      classCode,
				Docstring: "",
				ParentID:  parentScope.id,
			})

			relations = append(relations, CodeRelation{
				Source: parentScope.id,
				Target: classID,
				Label:  "CONTAINS",
			})

			scopeStack = append(scopeStack, tsScope{
				id:         classID,
				fqn:        classFqn,
				indent:     indent,
				entityType: "Class",
			})

		} else if matches := findFunctionMatch(line); len(matches) > 1 {
			funcName := matches[1]
			// Skip keywords that might match method regex
			if isTSKeyword(funcName) {
				continue
			}

			funcFqn := parentScope.fqn + "." + funcName
			funcID := "method_" + funcFqn

			funcCode := extractBlockCodeTS(lines, i, indent)

			entities = append(entities, CodeEntity{
				ID:        funcID,
				Type:      "Method", // "Method" is used universally in M-Flow CPG schema
				Name:      funcName,
				FQN:       funcFqn,
				Code:      funcCode,
				Docstring: "",
				ParentID:  parentScope.id,
			})

			relations = append(relations, CodeRelation{
				Source: parentScope.id,
				Target: funcID,
				Label:  "CONTAINS",
			})

			// Extract simple function calls using regex within the method block
			callRegex := regexp.MustCompile(`(?:this\.)?([a-zA-Z_][a-zA-Z0-9_]*)\s*\(.*\)`)
			codeLines := strings.Split(funcCode, "\n")
			if len(codeLines) > 0 {
				for _, codeLine := range codeLines {
					callMatches := callRegex.FindAllStringSubmatch(codeLine, -1)
					for _, m := range callMatches {
						if len(m) > 1 {
							targetName := m[1]
							if !isTSKeyword(targetName) && targetName != funcName {
								relations = append(relations, CodeRelation{
									Source: funcID,
									Target: "call_" + targetName,
									Label:  "CALLS",
								})
							}
						}
					}
				}
			}

			scopeStack = append(scopeStack, tsScope{
				id:         funcID,
				fqn:        funcFqn,
				indent:     indent,
				entityType: "Method",
			})
		}
	}

	return entities, AggregateRelations(relations), nil
}

func findFunctionMatch(line string) []string {
	if m := tsFuncRegex.FindStringSubmatch(line); len(m) > 0 {
		return m
	}
	if m := tsArrowFuncRegex.FindStringSubmatch(line); len(m) > 0 {
		return m
	}
	if m := tsMethodRegex.FindStringSubmatch(line); len(m) > 0 {
		return m
	}
	return nil
}

func isTSKeyword(name string) bool {
	keywords := map[string]bool{
		"if": true, "else": true, "for": true, "while": true, "switch": true,
		"catch": true, "return": true, "function": true, "class": true, "constructor": true,
	}
	return keywords[name]
}

// extractBlockCodeTS slices lines belonging to a block based on brace counting
// Fallback to indentation if braces aren't clear
func extractBlockCodeTS(lines []string, startLine, baseIndent int) string {
	var blockLines []string

	openBraces := 0
	started := false

	for i := startLine; i < len(lines); i++ {
		line := lines[i]
		blockLines = append(blockLines, line)

		openBraces += strings.Count(line, "{")
		openBraces -= strings.Count(line, "}")

		if strings.Contains(line, "{") {
			started = true
		}

		if started && openBraces <= 0 {
			break
		}

		// Fallback for one-liners or if brace parsing gets weird (simplified)
		if !started && i > startLine {
			lineIndent := countIndentation(line)
			if lineIndent <= baseIndent && strings.TrimSpace(line) != "" {
				// We likely exited the block
				blockLines = blockLines[:len(blockLines)-1]
				break
			}
		}
	}

	return strings.Join(blockLines, "\n")
}
