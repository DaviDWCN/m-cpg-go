package parser

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
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
	pythonScript := `
import ast
import json
import sys

def get_code_slice(lines, start_line, end_line):
    if end_line is None:
        return ""
    # ast lineno is 1-indexed, inclusive
    # end_lineno is also 1-indexed, inclusive
    return "\n".join(lines[start_line-1:end_line])

def parse(code, module_fqn, module_id):
    lines = code.split("\n")
    try:
        tree = ast.parse(code)
    except SyntaxError as e:
        print(json.dumps({"error": str(e)}))
        sys.exit(1)

    entities = []
    relations = []

    entities.append({
        "ID": module_id,
        "Type": "Module",
        "Name": module_fqn.split('.')[-1] if '.' in module_fqn else module_fqn,
        "FQN": module_fqn,
        "Code": code,
        "Docstring": ast.get_docstring(tree) or "",
        "ParentID": ""
    })

    class Visitor(ast.NodeVisitor):
        def __init__(self):
            self.scope_stack = [(module_id, module_fqn)]

        def visit_ClassDef(self, node):
            parent_id, parent_fqn = self.scope_stack[-1]
            class_name = node.name
            class_fqn = f"{parent_fqn}.{class_name}"
            class_id = f"class_{class_fqn}"

            entities.append({
                "ID": class_id,
                "Type": "Class",
                "Name": class_name,
                "FQN": class_fqn,
                "Code": get_code_slice(lines, node.lineno, getattr(node, 'end_lineno', node.lineno)),
                "Docstring": ast.get_docstring(node) or "",
                "ParentID": parent_id
            })

            relations.append({
                "Source": parent_id,
                "Target": class_id,
                "Label": "CONTAINS"
            })

            self.scope_stack.append((class_id, class_fqn))
            self.generic_visit(node)
            self.scope_stack.pop()

        def visit_FunctionDef(self, node):
            self.handle_function(node)

        def visit_AsyncFunctionDef(self, node):
            self.handle_function(node)

        def handle_function(self, node):
            parent_id, parent_fqn = self.scope_stack[-1]
            func_name = node.name
            func_fqn = f"{parent_fqn}.{func_name}"
            func_id = f"method_{func_fqn}"

            entities.append({
                "ID": func_id,
                "Type": "Method",
                "Name": func_name,
                "FQN": func_fqn,
                "Code": get_code_slice(lines, node.lineno, getattr(node, 'end_lineno', node.lineno)),
                "Docstring": ast.get_docstring(node) or "",
                "ParentID": parent_id
            })

            relations.append({
                "Source": parent_id,
                "Target": func_id,
                "Label": "CONTAINS"
            })

            class CallVisitor(ast.NodeVisitor):
                def visit_Call(self, call_node):
                    target_name = None
                    if isinstance(call_node.func, ast.Name):
                        target_name = call_node.func.id
                    elif isinstance(call_node.func, ast.Attribute):
                        target_name = call_node.func.attr

                    if target_name:
                        relations.append({
                            "Source": func_id,
                            "Target": f"call_{target_name}",
                            "Label": "CALLS"
                        })
                    self.generic_visit(call_node)

            cv = CallVisitor()
            for stmt in node.body:
                cv.visit(stmt)

            self.scope_stack.append((func_id, func_fqn))
            self.generic_visit(node)
            self.scope_stack.pop()

    Visitor().visit(tree)
    return {"Entities": entities, "Relations": relations}

if __name__ == "__main__":
    code = sys.stdin.read()
    module_fqn = sys.argv[1]
    module_id = sys.argv[2]
    res = parse(code, module_fqn, module_id)
    print(json.dumps(res))
`

	cmd := exec.Command("python3", "-c", pythonScript, moduleFqn, moduleID)
	cmd.Stdin = strings.NewReader(code)
	var out bytes.Buffer
	cmd.Stdout = &out

	if err := cmd.Run(); err != nil {
		return nil, nil, fmt.Errorf("python script failed: %w", err)
	}

	var result pythonASTResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		return nil, nil, fmt.Errorf("failed to unmarshal python script output: %w", err)
	}

	return result.Entities, result.Relations, nil
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
