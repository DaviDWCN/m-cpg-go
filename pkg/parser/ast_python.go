package parser

import (
	"bufio"
	"os"
	"regexp"
	"strings"
)

var (
	classRegex = regexp.MustCompile(`^\s*class\s+([a-zA-Z0-9_]+)`)
	funcRegex  = regexp.MustCompile(`^\s*def\s+([a-zA-Z0-9_]+)`)
)

type scope struct {
	id          string
	fqn         string
	indent      int
	entityType  string // "Class" or "Method"
	startLine   int
}

// ParsePythonFile extracts modules, classes, and functions structurally from a Python file
func ParsePythonFile(filePath, projectID, srcDir string) ([]CodeEntity, []CodeRelation, error) {
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
		Docstring: parseModuleDocstring(lines),
	})

	scopeStack := []scope{
		{id: moduleID, fqn: moduleFqn, indent: -1, entityType: "Module", startLine: 0},
	}

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		// Skip empty lines or pure comment lines
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		indent := countIndentation(line)

		// Pop scopes that are no longer active based on indentation
		for len(scopeStack) > 1 && scopeStack[len(scopeStack)-1].indent >= indent {
			scopeStack = scopeStack[:len(scopeStack)-1]
		}

		parentScope := scopeStack[len(scopeStack)-1]

		if matches := classRegex.FindStringSubmatch(line); len(matches) > 1 {
			className := matches[1]
			classFqn := parentScope.fqn + "." + className
			classID := "class_" + classFqn

			// Capture class docstring
			docstring, docLinesCount := parseDocstring(lines, i+1, indent+4)

			// Capture class code body (lines with higher indentation)
			classCode := extractBlockCode(lines, i, indent)

			entities = append(entities, CodeEntity{
				ID:        classID,
				Type:      "Class",
				Name:      className,
				FQN:       classFqn,
				Code:      classCode,
				Docstring: docstring,
				ParentID:  parentScope.id,
			})

			relations = append(relations, CodeRelation{
				Source: parentScope.id,
				Target: classID,
				Label:  "CONTAINS",
			})

			scopeStack = append(scopeStack, scope{
				id:         classID,
				fqn:        classFqn,
				indent:     indent,
				entityType: "Class",
				startLine:  i,
			})

			i += docLinesCount // Skip docstring lines in subsequent parsing
		} else if matches := funcRegex.FindStringSubmatch(line); len(matches) > 1 {
			funcName := matches[1]
			funcFqn := parentScope.fqn + "." + funcName
			funcID := "method_" + funcFqn

			docstring, docLinesCount := parseDocstring(lines, i+1, indent+4)
			funcCode := extractBlockCode(lines, i, indent)

			entities = append(entities, CodeEntity{
				ID:        funcID,
				Type:      "Method", // "Method" is used universally in M-Flow CPG schema
				Name:      funcName,
				FQN:       funcFqn,
				Code:      funcCode,
				Docstring: docstring,
				ParentID:  parentScope.id,
			})

			relations = append(relations, CodeRelation{
				Source: parentScope.id,
				Target: funcID,
				Label:  "CONTAINS",
			})

			scopeStack = append(scopeStack, scope{
				id:         funcID,
				fqn:        funcFqn,
				indent:     indent,
				entityType: "Method",
				startLine:  i,
			})

			i += docLinesCount
		}
	}

	return entities, relations, nil
}

func countIndentation(line string) int {
	count := 0
	for _, char := range line {
		if char == ' ' {
			count++
		} else if char == '\t' {
			count += 4 // Convert tabs to standard 4 spaces
		} else {
			break
		}
	}
	return count
}

func filepathBase(fqn string) string {
	parts := strings.Split(fqn, ".")
	return parts[len(parts)-1]
}

func parseModuleDocstring(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	doc, _ := parseDocstring(lines, 0, 0)
	return doc
}

// parseDocstring looks for triple quotes (""" or ''') right after a def/class statement
func parseDocstring(lines []string, startLine, expectedIndent int) (string, int) {
	if startLine >= len(lines) {
		return "", 0
	}

	// Skip empty lines first
	curr := startLine
	for curr < len(lines) && strings.TrimSpace(lines[curr]) == "" {
		curr++
	}

	if curr >= len(lines) {
		return "", 0
	}

	line := strings.TrimSpace(lines[curr])
	var quote string
	if strings.HasPrefix(line, `"""`) {
		quote = `"""`
	} else if strings.HasPrefix(line, `'''`) {
		quote = `'''`
	} else {
		return "", 0
	}

	// Single-line docstring: e.g. """doc"""
	if strings.HasSuffix(line, quote) && len(line) > len(quote)*2 {
		content := strings.TrimSuffix(strings.TrimPrefix(line, quote), quote)
		return strings.TrimSpace(content), (curr - startLine) + 1
	}

	// Multi-line docstring
	var docLines []string
	docLines = append(docLines, strings.TrimPrefix(line, quote))

	endFound := false
	i := curr + 1
	for ; i < len(lines); i++ {
		l := lines[i]
		if strings.Contains(l, quote) {
			parts := strings.Split(l, quote)
			docLines = append(docLines, parts[0])
			endFound = true
			break
		}
		docLines = append(docLines, l)
	}

	if !endFound {
		return "", 0
	}

	// Standardize indentation stripping
	var cleanLines []string
	for _, dl := range docLines {
		cleanLines = append(cleanLines, strings.TrimSpace(dl))
	}

	return strings.Join(cleanLines, "\n"), (i - startLine) + 1
}

// extractBlockCode slices lines belonging to a block based on indentation
func extractBlockCode(lines []string, startLine, blockIndent int) string {
	var blockLines []string
	blockLines = append(blockLines, lines[startLine])

	for i := startLine + 1; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		// Include empty lines in the code block
		if trimmed == "" {
			blockLines = append(blockLines, "")
			continue
		}

		lineIndent := countIndentation(line)
		if lineIndent <= blockIndent && !strings.HasPrefix(trimmed, "#") {
			break
		}
		blockLines = append(blockLines, line)
	}

	// Trim empty trailing lines
	for len(blockLines) > 0 && blockLines[len(blockLines)-1] == "" {
		blockLines = blockLines[:len(blockLines)-1]
	}

	return strings.Join(blockLines, "\n")
}
