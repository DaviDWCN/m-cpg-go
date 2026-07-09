package parser

import (
	"bufio"

	"fmt"
	"os"

	"github.com/go-python/gpython/ast"
	"github.com/go-python/gpython/parser"
	"regexp"
	"strings"
)

var (
	classRegex = regexp.MustCompile(`^\s*class\s+([a-zA-Z0-9_]+)`)
	funcRegex  = regexp.MustCompile(`^\s*def\s+([a-zA-Z0-9_]+)`)
)

type scope struct {
	id         string
	fqn        string
	indent     int
	entityType string // "Class" or "Method"
	startLine  int
}

type pythonASTResult struct {
	Entities  []CodeEntity
	Relations []CodeRelation
}

// ParsePythonFile extracts modules, classes, and functions structurally from a Python file
func ParsePythonFile(filePath, projectID, srcDir string) ([]CodeEntity, []CodeRelation, error) {
	fileContent, err := os.ReadFile(filePath)
	if err != nil {
		return nil, nil, err
	}

	moduleFqn := DeriveFQN(filePath, srcDir)
	moduleID := "module_" + moduleFqn

	// Try the python ast approach first
	entities, relations, err := parsePythonFileWithAST(string(fileContent), moduleFqn, moduleID)
	if err == nil {
		return entities, AggregateRelations(relations), nil
	}

	// Fallback to regex-based parsing if Python is not available or script fails
	entities, relations, err = parsePythonFileRegex(filePath, moduleFqn, moduleID)
	return entities, AggregateRelations(relations), err
}

func parsePythonFileWithAST(code, moduleFqn, moduleID string) ([]CodeEntity, []CodeRelation, error) {
	node, err := parser.ParseString(code, "exec")
	if err != nil {
		return nil, nil, fmt.Errorf("gpython syntax error: %w", err)
	}

	lines := strings.Split(code, "\n")
	var entities []CodeEntity
	var relations []CodeRelation

	moduleName := moduleFqn
	if parts := strings.Split(moduleFqn, "."); len(parts) > 0 {
		moduleName = parts[len(parts)-1]
	}

	docstring := ""
	if mod, ok := node.(*ast.Module); ok && len(mod.Body) > 0 {
		if exprStmt, ok := mod.Body[0].(*ast.ExprStmt); ok {
			if strExpr, ok := exprStmt.Value.(*ast.Str); ok {
				docstring = string(strExpr.S)
			}
		}
	}

	entities = append(entities, CodeEntity{
		ID:        moduleID,
		Type:      "Module",
		Name:      moduleName,
		FQN:       moduleFqn,
		Code:      code,
		Docstring: docstring,
		ParentID:  "",
	})

	var walk func(n ast.Ast, parentID, parentFqn string)
	walk = func(n ast.Ast, parentID, parentFqn string) {
		switch t := n.(type) {
		case *ast.Module:
			for _, stmt := range t.Body {
				walk(stmt, parentID, parentFqn)
			}
		case *ast.ClassDef:
			className := string(t.Name)
			classFqn := parentFqn + "." + className
			classID := "class_" + classFqn

			classDoc := ""
			if len(t.Body) > 0 {
				if exprStmt, ok := t.Body[0].(*ast.ExprStmt); ok {
					if strExpr, ok := exprStmt.Value.(*ast.Str); ok {
						classDoc = string(strExpr.S)
					}
				}
			}

			entities = append(entities, CodeEntity{
				ID:        classID,
				Type:      "Class",
				Name:      className,
				FQN:       classFqn,
				Code:      sliceCodePython(lines, t.Lineno, t.Lineno), // fallback
				Docstring: classDoc,
				ParentID:  parentID,
			})
			relations = append(relations, CodeRelation{
				Source: parentID,
				Target: classID,
				Label:  "CONTAINS",
			})

			for _, stmt := range t.Body {
				walk(stmt, classID, classFqn)
			}
		case *ast.FunctionDef:
			funcName := string(t.Name)
			funcFqn := parentFqn + "." + funcName
			funcID := "method_" + funcFqn

			funcDoc := ""
			if len(t.Body) > 0 {
				if exprStmt, ok := t.Body[0].(*ast.ExprStmt); ok {
					if strExpr, ok := exprStmt.Value.(*ast.Str); ok {
						funcDoc = string(strExpr.S)
					}
				}
			}

			entities = append(entities, CodeEntity{
				ID:        funcID,
				Type:      "Method",
				Name:      funcName,
				FQN:       funcFqn,
				Code:      sliceCodePython(lines, t.Lineno, t.Lineno),
				Docstring: funcDoc,
				ParentID:  parentID,
			})
			relations = append(relations, CodeRelation{
				Source: parentID,
				Target: funcID,
				Label:  "CONTAINS",
			})

			var inspect func(ast.Ast)
			inspect = func(cn ast.Ast) {
				if call, ok := cn.(*ast.Call); ok {
					if name, ok := call.Func.(*ast.Name); ok {
						relations = append(relations, CodeRelation{
							Source: funcID,
							Target: "call_" + string(name.Id),
							Label:  "CALLS",
						})
					} else if attr, ok := call.Func.(*ast.Attribute); ok {
						relations = append(relations, CodeRelation{
							Source: funcID,
							Target: "call_" + string(attr.Attr),
							Label:  "CALLS",
						})
					}
				}
				switch n := cn.(type) {
				case *ast.ExprStmt:
					inspect(n.Value)
				case *ast.Call:
					inspect(n.Func)
					for _, arg := range n.Args {
						inspect(arg)
					}
				case *ast.Assign:
					for _, target := range n.Targets {
						inspect(target)
					}
					inspect(n.Value)
				case *ast.Return:
					if n.Value != nil {
						inspect(n.Value)
					}
				case *ast.If:
					inspect(n.Test)
					for _, stmt := range n.Body {
						inspect(stmt)
					}
					for _, stmt := range n.Orelse {
						inspect(stmt)
					}
				case *ast.For:
					inspect(n.Target)
					inspect(n.Iter)
					for _, stmt := range n.Body {
						inspect(stmt)
					}
					for _, stmt := range n.Orelse {
						inspect(stmt)
					}
				case *ast.While:
					inspect(n.Test)
					for _, stmt := range n.Body {
						inspect(stmt)
					}
					for _, stmt := range n.Orelse {
						inspect(stmt)
					}
				case *ast.FunctionDef:
					// Don't recurse into inner functions to avoid polluting this method's calls
				case *ast.ClassDef:
					// Same for inner classes
				case *ast.Attribute:
					inspect(n.Value)
				}
			}
			for _, stmt := range t.Body {
				inspect(stmt)
			}
		}
	}

	walk(node, moduleID, moduleFqn)

	return entities, relations, nil
}

func sliceCodePython(lines []string, startLine, endLine int) string {
	if startLine < 1 || startLine > len(lines) {
		return ""
	}
	if endLine < startLine {
		endLine = startLine
	}

	// Try to find the end of the block logically based on indentation
	startIdx := startLine - 1
	baseIndent := countIndentation(lines[startIdx])

	endIdx := startIdx
	for i := startIdx + 1; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			endIdx = i
			continue
		}

		indent := countIndentation(line)
		if indent <= baseIndent && !strings.HasPrefix(trimmed, "#") {
			break
		}
		endIdx = i
	}

	return strings.Join(lines[startIdx:endIdx+1], "\n")
}

func parsePythonFileRegex(filePath, moduleFqn, moduleID string) ([]CodeEntity, []CodeRelation, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()

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

			// Extract simple function calls using regex within the method block
			callRegex := regexp.MustCompile(`([a-zA-Z_][a-zA-Z0-9_]*(?:\.[a-zA-Z_][a-zA-Z0-9_]*)*)\s*\(.*\)`)
			codeLines := strings.Split(funcCode, "\n")
			// Skip the def line itself to avoid self-reference for the definition
			if len(codeLines) > 1 {
				for _, codeLine := range codeLines[1:] {
					callMatches := callRegex.FindAllStringSubmatch(codeLine, -1)
					for _, m := range callMatches {
						if len(m) > 1 {
							targetName := m[1]
							// Exclude common keywords that look like function calls
							if targetName != "if" && targetName != "elif" && targetName != "while" && targetName != "for" && targetName != "with" {
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

// parseDocstring looks for triple quotes (""" or ”') right after a def/class statement
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
