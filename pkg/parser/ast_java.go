package parser

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ParseJavaFile parses Java files to extract modules (packages), classes/interfaces, and methods.
func ParseJavaFile(filePath, projectID, srcDir string) ([]CodeEntity, []CodeRelation, error) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, nil, err
	}

	codeStr := string(content)

	// 1. Extract Package FQN
	packageRegex := regexp.MustCompile(`package\s+([a-zA-Z0-9_\.]+);`)
	packageMatch := packageRegex.FindStringSubmatch(codeStr)
	pkg := ""
	if len(packageMatch) > 1 {
		pkg = packageMatch[1]
	}

	fileName := filepath.Base(filePath)
	baseName := strings.TrimSuffix(fileName, filepath.Ext(fileName))

	var moduleFqn string
	if pkg != "" {
		moduleFqn = pkg + "." + baseName
	} else {
		moduleFqn = baseName
	}

	moduleID := "module_" + moduleFqn

	var entities []CodeEntity
	var relations []CodeRelation

	// Insert Module (File) Entity
	entities = append(entities, CodeEntity{
		ID:        moduleID,
		Type:      "Module",
		Name:      baseName,
		FQN:       moduleFqn,
		Code:      codeStr,
		Docstring: fmt.Sprintf("Java Package: %s, File: %s", pkg, fileName),
		FilePath:  filePath,
	})

	// 2. Extract Classes/Interfaces/Enums
	classRegex := regexp.MustCompile(`(?:(?:public|protected|private|static|abstract|final|sealed|non-sealed|strictfp)\s+)*(class|interface|enum|record)\s+([a-zA-Z0-9_]+)`)
	classMatches := classRegex.FindAllStringSubmatchIndex(codeStr, -1)

	classIDMap := make(map[string]string) // name -> ID

	for _, matchIdx := range classMatches {
		kwStart, kwEnd := matchIdx[2], matchIdx[3]
		nameStart, nameEnd := matchIdx[4], matchIdx[5]

		if nameStart == -1 || nameEnd == -1 {
			continue
		}

		className := codeStr[nameStart:nameEnd]
		keyword := codeStr[kwStart:kwEnd]
		classFqn := moduleFqn
		if pkg != "" {
			classFqn = pkg + "." + className
		} else {
			classFqn = className
		}
		classID := "class_" + classFqn
		classIDMap[className] = classID

		// Extract docstring preceding the class
		preceding := strings.TrimSpace(codeStr[:matchIdx[0]])
		docstring := ""
		if strings.HasSuffix(preceding, "*/") {
			commentStart := strings.LastIndex(preceding, "/**")
			if commentStart != -1 {
				docstring = cleanJavaDoc(preceding[commentStart:])
			}
		}

		classDeclPos := matchIdx[0]
		classCode := codeStr[classDeclPos:]
		if len(classCode) > 1000 {
			classCode = classCode[:1000] + "\n... [omitted]"
		}

		entities = append(entities, CodeEntity{
			ID:        classID,
			Type:      "Class",
			Name:      className,
			FQN:       classFqn,
			Code:      classCode,
			Docstring: fmt.Sprintf("[%s] %s", keyword, docstring),
			ParentID:  moduleID,
			FilePath:  filePath,
		})

		relations = append(relations, CodeRelation{
			Source: moduleID,
			Target: classID,
			Label:  "CONTAINS",
		})
	}

	// 3. Extract Methods
	// Enforce start of line (possibly with whitespace) to avoid matching method calls as method declarations.
	methodRegex := regexp.MustCompile(`(?m)^[ \t]*(?:(?:public|protected|private|static|final|synchronized|abstract|default|native|strictfp)\s+)*(?:[\w\<\>\[\]\,\?\.\s]+\s+)?([a-zA-Z0-9_]+)\s*\(([^\)]*)\)\s*(?:throws\s+[\w\s,\.]+)?\s*(?:\{|;)`)
	methodMatchesRaw := methodRegex.FindAllStringSubmatchIndex(codeStr, -1)

	var methodMatches [][]int
	for _, matchIdx := range methodMatchesRaw {
		if matchIdx[2] == -1 || matchIdx[3] == -1 {
			continue
		}
		methodName := codeStr[matchIdx[2]:matchIdx[3]]
		if methodName == "if" || methodName == "for" || methodName == "while" || methodName == "switch" || methodName == "catch" {
			continue
		}

		fullMatch := codeStr[matchIdx[0]:matchIdx[1]]

		// Filter out statement calls mistakenly captured as method declarations (e.g., return compute();, throw new Exception();)
		statementRegex := regexp.MustCompile(`(?m)^[ \t]*(return|throw|new)\b`)
		if statementRegex.MatchString(fullMatch) {
			continue
		}

		isCallRegex := regexp.MustCompile(`(?m)^[ \t]*[a-zA-Z0-9_]+\s*\([^)]*\)\s*;`)
		if isCallRegex.MatchString(fullMatch) {
			continue
		}
		methodMatches = append(methodMatches, matchIdx)
	}

	for i, matchIdx := range methodMatches {
		nameStart, nameEnd := matchIdx[2], matchIdx[3]
		paramsStart, paramsEnd := matchIdx[4], matchIdx[5]

		methodName := codeStr[nameStart:nameEnd]

		params := ""
		if paramsStart != -1 && paramsEnd != -1 {
			params = codeStr[paramsStart:paramsEnd]
		}

		// Extract docstring preceding the method
		preceding := strings.TrimSpace(codeStr[:matchIdx[0]])
		docstring := ""
		if strings.HasSuffix(preceding, "*/") {
			commentStart := strings.LastIndex(preceding, "/**")
			if commentStart != -1 {
				docstring = cleanJavaDoc(preceding[commentStart:])
			}
		}

		parentID := moduleID
		parentFqn := moduleFqn
		for className, cID := range classIDMap {
			classIndex := strings.Index(codeStr, className)
			if classIndex != -1 && classIndex < matchIdx[0] {
				parentID = cID
				if pkg != "" {
					parentFqn = pkg + "." + className
				} else {
					parentFqn = className
				}
			}
		}

		methodFqn := parentFqn + "." + methodName
		methodID := "method_" + methodFqn

		methodDeclPos := matchIdx[0]
		methodCode := codeStr[methodDeclPos:]

		nextPos := len(codeStr)
		if i+1 < len(methodMatches) {
			nextPos = methodMatches[i+1][0]
		}

		methodChunk := codeStr[methodDeclPos:nextPos]
		if len(methodChunk) > 1500 {
			methodChunk = methodChunk[:1500]
		}

		// Extract Calls
		callRegex := regexp.MustCompile(`([a-zA-Z0-9_]+)\s*\(`)
		callMatches := callRegex.FindAllStringSubmatch(methodChunk, -1)
		for _, m := range callMatches {
			if len(m) > 1 {
				targetName := m[1]
				if !isJavaKeyword(targetName) && targetName != methodName {
					relations = append(relations, CodeRelation{
						Source: methodID,
						Target: "call_" + targetName,
						Label:  "CALLS",
					})
				}
			}
		}

		if len(methodCode) > 800 {
			methodCode = methodCode[:800] + "\n... [omitted]"
		}

		entities = append(entities, CodeEntity{
			ID:        methodID,
			Type:      "Method",
			Name:      methodName,
			FQN:       methodFqn,
			Code:      methodCode,
			Docstring: fmt.Sprintf("Params: (%s)\n%s", params, docstring),
			ParentID:  parentID,
			FilePath:  filePath,
		})

		relations = append(relations, CodeRelation{
			Source: parentID,
			Target: methodID,
			Label:  "CONTAINS",
		})
	}

	return entities, AggregateRelations(relations), nil
}

func isJavaKeyword(word string) bool {
	keywords := map[string]bool{
		"if": true, "for": true, "while": true, "switch": true, "catch": true,
		"super": true, "this": true, "return": true, "new": true, "synchronized": true,
	}
	return keywords[word]
}

func cleanJavaDoc(doc string) string {
	lines := strings.Split(doc, "\n")
	var cleaned []string
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		trimmed = strings.TrimPrefix(trimmed, "/**")
		trimmed = strings.TrimSuffix(trimmed, "*/")
		trimmed = strings.TrimPrefix(trimmed, "*")
		trimmed = strings.TrimSpace(trimmed)
		if trimmed != "" {
			cleaned = append(cleaned, trimmed)
		}
	}
	return strings.Join(cleaned, " ")
}
