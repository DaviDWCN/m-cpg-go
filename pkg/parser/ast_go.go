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

	// Keep track of declared Structs to build relations
	structIDMap := make(map[string]string) // name -> ID

	// 2. First pass: Inspect Declarations to extract Structs (Classes)
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

			_, ok = typeSpec.Type.(*ast.StructType)
			if !ok {
				continue // Skip interfaces, type aliases, etc. for simple class mapping
			}

			structName := typeSpec.Name.Name
			structFqn := moduleFqn + "." + structName
			structID := "class_" + structFqn
			structIDMap[structName] = structID

			// Capture struct code block
			structCode := sliceCode(lines, fset.Position(genDecl.Pos()).Line, fset.Position(genDecl.End()).Line)
			docstring := getCommentGroupText(genDecl.Doc)
			if docstring == "" {
				docstring = getCommentGroupText(typeSpec.Doc)
			}

			entities = append(entities, CodeEntity{
				ID:        structID,
				Type:      "Class",
				Name:      structName,
				FQN:       structFqn,
				Code:      structCode,
				Docstring: docstring,
				ParentID:  moduleID,
			})

			relations = append(relations, CodeRelation{
				Source: moduleID,
				Target: structID,
				Label:  "CONTAINS",
			})
		}
	}

	// 3. Second pass: Extract Functions and Methods (Methods)
	for _, decl := range fileAST.Decls {
		funcDecl, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}

		funcName := funcDecl.Name.Name
		var parentID string
		var funcFqn string

		// Determine if it's a method on a struct
		if funcDecl.Recv != nil && len(funcDecl.Recv.List) > 0 {
			recvType := funcDecl.Recv.List[0].Type
			recvName := getReceiverTypeName(recvType)
			if structID, exists := structIDMap[recvName]; exists {
				parentID = structID
				funcFqn = moduleFqn + "." + recvName + "." + funcName
			} else {
				// Fallback to module parent if struct is not defined in this file
				parentID = moduleID
				funcFqn = moduleFqn + "." + funcName
			}
		} else {
			parentID = moduleID
			funcFqn = moduleFqn + "." + funcName
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
	}

	return entities, relations, nil
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
