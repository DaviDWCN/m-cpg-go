package parser

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type mdScope struct {
	id    string
	fqn   string
	level int
	text  []string
}

// ParseMarkdownFile parses a Markdown file, dividing it into sections by headings.
func ParseMarkdownFile(filePath, projectID, srcDir string) ([]CodeEntity, []CodeRelation, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()

	docFqn := DeriveFQN(filePath, srcDir)
	docID := "doc_" + docFqn

	var entities []CodeEntity
	var relations []CodeRelation

	// Read lines
	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, err
	}

	fileName := filepath.Base(filePath)

	// Create root document entity
	rootEntity := CodeEntity{
		ID:        docID,
		Type:      "Document",
		Name:      fileName,
		FQN:       docFqn,
		Code:      strings.Join(lines, "\n"),
		Docstring: "Markdown document: " + fileName,
	}
	entities = append(entities, rootEntity)

	// Regexp to match Markdown headings: e.g. "### Heading Text"
	headingRegex := regexp.MustCompile(`^(#{1,6})\s+(.*)$`)

	// Stack to track document sections hierarchically
	scopeStack := []*mdScope{
		{id: docID, fqn: docFqn, level: 0, text: make([]string, 0)},
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if matches := headingRegex.FindStringSubmatch(trimmed); len(matches) > 1 {
			hashes := matches[1]
			headingText := strings.TrimSpace(matches[2])
			level := len(hashes)

			// Clean heading text to use as safe identifier
			safeName := cleanHeadingName(headingText)

			// Pop from stack until the parent heading has a lower level (closer to root)
			for len(scopeStack) > 1 && scopeStack[len(scopeStack)-1].level >= level {
				// Finalize text for the popped scope
				finalizeScopeText(scopeStack[len(scopeStack)-1], &entities)
				scopeStack = scopeStack[:len(scopeStack)-1]
			}

			parent := scopeStack[len(scopeStack)-1]
			secFqn := parent.fqn + "." + safeName
			secID := "doc_sec_" + secFqn

			newEntity := CodeEntity{
				ID:       secID,
				Type:     "Document",
				Name:     headingText,
				FQN:      secFqn,
				ParentID: parent.id,
			}
			entities = append(entities, newEntity)

			relations = append(relations, CodeRelation{
				Source: parent.id,
				Target: secID,
				Label:  "CONTAINS",
			})

			scopeStack = append(scopeStack, &mdScope{
				id:    secID,
				fqn:   secFqn,
				level: level,
				text:  []string{line}, // Start with the heading line
			})
		} else {
			// Append normal line to the current active scope
			scopeStack[len(scopeStack)-1].text = append(scopeStack[len(scopeStack)-1].text, line)
		}
	}

	// Finalize all remaining scopes in stack
	for len(scopeStack) > 0 {
		finalizeScopeText(scopeStack[len(scopeStack)-1], &entities)
		scopeStack = scopeStack[:len(scopeStack)-1]
	}

	for i := range entities {
		entities[i].FilePath = filePath
	}

	return entities, AggregateRelations(relations), nil
}

func cleanHeadingName(heading string) string {
	// Keep alphanumeric characters, underscores, and convert spaces to underscores
	reg := regexp.MustCompile(`[^a-zA-Z0-9_\s]`)
	clean := reg.ReplaceAllString(heading, "")
	clean = strings.TrimSpace(clean)
	clean = strings.ReplaceAll(clean, " ", "_")
	if len(clean) > 30 {
		clean = clean[:30]
	}
	return clean
}

func finalizeScopeText(s *mdScope, entities *[]CodeEntity) {
	fullText := strings.Join(s.text, "\n")
	for i, ent := range *entities {
		if ent.ID == s.id {
			(*entities)[i].Code = fullText
			(*entities)[i].Docstring = strings.TrimSpace(fullText)
			break
		}
	}
}
