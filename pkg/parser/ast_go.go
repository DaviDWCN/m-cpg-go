package parser

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
)

// ParseGoFile extracts modules (packages), structs (classes), and functions/methods from a Go source file
func ParseGoFile(filePath, projectID, srcDir string) ([]CodeEntity, []CodeRelation, error) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, nil, err
	}

	fset := token.NewFileSet()
	fileAST, err := parser.ParseFile(fset, filePath, content, parser.ParseComments)
	if err != nil {
		return nil, nil, err
	}

	moduleFqn := DeriveFQN(filePath, srcDir)
	moduleID := "module_" + moduleFqn

	var entities []CodeEntity
	var relations []CodeRelation

	// Read all file lines for code slicing
	lines := strings.Split(string(content), "\n")

	// 1. Insert Module Node
	entities = append(entities, CodeEntity{
		ID:        moduleID,
		Type:      "Module",
		Name:      fileAST.Name.Name,
		FQN:       moduleFqn,
		Code:      string(content),
		Docstring: getCommentGroupText(fileAST.Doc),
	})

	// Keep track of declared Structs/Interfaces to build relations
	typeIDMap := make(map[string]string) // name -> ID
	// Keep track of fields for basic type inference
	typeFieldMap := make(map[string]map[string]string) // StructName -> map[fieldName]fieldTypeName

	// 2. First pass: Inspect Declarations to extract Structs, Interfaces, and Fields
	for _, decl := range fileAST.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.TYPE {
			continue
		}

		for _, spec := range genDecl.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}

			typeName := typeSpec.Name.Name
			typeFqn := moduleFqn + "." + typeName
			typeID := ""

			switch t := typeSpec.Type.(type) {
			case *ast.StructType:
				typeID = "class_" + typeFqn
				typeIDMap[typeName] = typeID
				typeFieldMap[typeName] = make(map[string]string)

				structCode := sliceCode(lines, fset.Position(genDecl.Pos()).Line, fset.Position(genDecl.End()).Line)
				docstring := getCommentGroupText(genDecl.Doc)
				if docstring == "" {
					docstring = getCommentGroupText(typeSpec.Doc)
				}

				entities = append(entities, CodeEntity{
					ID:        typeID,
					Type:      "Class",
					Name:      typeName,
					FQN:       typeFqn,
					Code:      structCode,
					Docstring: docstring,
					ParentID:  moduleID,
				})

				relations = append(relations, CodeRelation{
					Source: moduleID,
					Target: typeID,
					Label:  "CONTAINS",
				})

				// Extract Struct Fields
				if t.Fields != nil {
					for _, field := range t.Fields.List {
						fieldTypeName := getReceiverTypeName(field.Type)
						for _, fieldNameIdent := range field.Names {
							fieldName := fieldNameIdent.Name
							fieldFqn := typeFqn + "." + fieldName
							fieldID := "field_" + fieldFqn

							entities = append(entities, CodeEntity{
								ID:        fieldID,
								Type:      "Field",
								Name:      fieldName,
								FQN:       fieldFqn,
								Code:      sliceCode(lines, fset.Position(field.Pos()).Line, fset.Position(field.End()).Line),
								Docstring: getCommentGroupText(field.Doc),
								ParentID:  typeID,
							})

							relations = append(relations, CodeRelation{
								Source: typeID,
								Target: fieldID,
								Label:  "CONTAINS",
							})

							// Very basic type inference for fields
							if fieldTypeName != "" {
								typeFieldMap[typeName][fieldName] = fieldTypeName
							}
						}
					}
				}

			case *ast.InterfaceType:
				typeID = "interface_" + typeFqn
				typeIDMap[typeName] = typeID

				interfaceCode := sliceCode(lines, fset.Position(genDecl.Pos()).Line, fset.Position(genDecl.End()).Line)
				docstring := getCommentGroupText(genDecl.Doc)
				if docstring == "" {
					docstring = getCommentGroupText(typeSpec.Doc)
				}

				entities = append(entities, CodeEntity{
					ID:        typeID,
					Type:      "Interface",
					Name:      typeName,
					FQN:       typeFqn,
					Code:      interfaceCode,
					Docstring: docstring,
					ParentID:  moduleID,
				})

				relations = append(relations, CodeRelation{
					Source: moduleID,
					Target: typeID,
					Label:  "CONTAINS",
				})
			}
		}
	}

	// 3. Second pass: Extract Functions and Methods
	for _, decl := range fileAST.Decls {
		funcDecl, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}

		funcName := funcDecl.Name.Name
		var parentID string
		var funcFqn string
		var recvName string

		// Function-scoped variable type map
		funcVarTypeMap := make(map[string]string)

		// Determine if it's a method on a struct
		if funcDecl.Recv != nil && len(funcDecl.Recv.List) > 0 {
			recvType := funcDecl.Recv.List[0].Type
			recvName = getReceiverTypeName(recvType)
			if typeID, exists := typeIDMap[recvName]; exists {
				parentID = typeID
				funcFqn = moduleFqn + "." + recvName + "." + funcName
			} else {
				parentID = moduleID
				funcFqn = moduleFqn + "." + funcName
			}

			// Map receiver variable name to struct type for type inference
			if len(funcDecl.Recv.List[0].Names) > 0 {
				funcVarTypeMap[funcDecl.Recv.List[0].Names[0].Name] = recvName
			}
		} else {
			parentID = moduleID
			funcFqn = moduleFqn + "." + funcName
		}

		// If the function returns a known struct type, let's map its results for calls (simplistic, just captures return types)
		if funcDecl.Type.Results != nil && len(funcDecl.Type.Results.List) > 0 {
			// This isn't immediately mapped to local vars unless called, but we could do more complex static analysis later
		}

		funcID := "method_" + funcFqn
		funcCode := sliceCode(lines, fset.Position(funcDecl.Pos()).Line, fset.Position(funcDecl.End()).Line)
		docstring := getCommentGroupText(funcDecl.Doc)

		entities = append(entities, CodeEntity{
			ID:        funcID,
			Type:      "Method",
			Name:      funcName,
			FQN:       funcFqn,
			Code:      funcCode,
			Docstring: docstring,
			ParentID:  parentID,
		})

		relations = append(relations, CodeRelation{
			Source: parentID,
			Target: funcID,
			Label:  "CONTAINS",
		})

		// 4. Extract function calls within this function body
		ast.Inspect(funcDecl.Body, func(n ast.Node) bool {
			// Record local assignments for simple type inference
			if assignStmt, ok := n.(*ast.AssignStmt); ok {
				if len(assignStmt.Lhs) == 1 && len(assignStmt.Rhs) == 1 {
					if ident, ok := assignStmt.Lhs[0].(*ast.Ident); ok {
						if callExpr, ok := assignStmt.Rhs[0].(*ast.CallExpr); ok {
							// E.g., svc := NewService() -> map "svc" to "Service" (approximate)
							if funcIdent, ok := callExpr.Fun.(*ast.Ident); ok && strings.HasPrefix(funcIdent.Name, "New") {
								inferredType := strings.TrimPrefix(funcIdent.Name, "New")
								funcVarTypeMap[ident.Name] = inferredType
							}
						}
					}
				}
			}

			callExpr, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}

			var targetName string
			switch fun := callExpr.Fun.(type) {
			case *ast.Ident:
				targetName = fun.Name
			case *ast.SelectorExpr:
				// E.g., svc.DoSomething()
				if ident, ok := fun.X.(*ast.Ident); ok {
					// Check if we know the type of 'ident.Name' (the receiver instance)
					if typeName, exists := funcVarTypeMap[ident.Name]; exists {
						targetName = typeName + "." + fun.Sel.Name
					} else if recvName != "" && (ident.Name == "s" || ident.Name == "m") { // very naive heuristic fallback
						// we already map receiver accurately in funcVarTypeMap for 'this'
					} else {
						// See if it refers to a field of the receiver
						if recvName != "" {
							if fieldMap, ok := typeFieldMap[recvName]; ok {
								if fieldType, ok := fieldMap[ident.Name]; ok {
									targetName = fieldType + "." + fun.Sel.Name
								}
							}
						}
						if targetName == "" {
							targetName = ident.Name + "." + fun.Sel.Name
						}
					}
				} else if selExpr, ok := fun.X.(*ast.SelectorExpr); ok { // e.g. s.FieldA.Do()
					if ident, ok := selExpr.X.(*ast.Ident); ok {
						if typeName, exists := funcVarTypeMap[ident.Name]; exists {
							if fieldMap, ok := typeFieldMap[typeName]; ok {
								if fieldType, ok := fieldMap[selExpr.Sel.Name]; ok {
									targetName = fieldType + "." + fun.Sel.Name
								}
							}
						}
					}
					if targetName == "" {
						targetName = selExpr.Sel.Name + "." + fun.Sel.Name
					}
				} else {
					targetName = fun.Sel.Name
				}
			}

			if targetName != "" {
				relations = append(relations, CodeRelation{
					Source: funcID,
					Target: "call_" + targetName,
					Label:  "CALLS",
				})
			}
			return true
		})
	}

	return entities, AggregateRelations(relations), nil
}

func getCommentGroupText(cg *ast.CommentGroup) string {
	if cg == nil {
		return ""
	}
	return strings.TrimSpace(cg.Text())
}

func getReceiverTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return getReceiverTypeName(t.X)
	default:
		return fmt.Sprintf("%v", expr)
	}
}

func sliceCode(lines []string, startLine, endLine int) string {
	if startLine < 1 || startLine > len(lines) {
		return ""
	}
	if endLine < startLine {
		endLine = startLine
	}
	if endLine > len(lines) {
		endLine = len(lines)
	}

	// 1-indexed to 0-indexed slice
	return strings.Join(lines[startLine-1:endLine], "\n")
}
