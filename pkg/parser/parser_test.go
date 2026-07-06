package parser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParsePythonFile(t *testing.T) {
	// Create a temp directory and python file
	tmpDir, err := os.MkdirTemp("", "m-cpg-parser-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	pyCode := `"""
This is a module docstring.
It spans multiple lines.
"""

class UserAccount:
    """Represents a user account."""
    def __init__(self, username: str):
        self.username = username
        
    func_in_class_with_doc = """
    This should not be treated as class docstring.
    """

    def get_display_name(self) -> str:
        """Returns username."""
        return self.username

def top_level_function():
    """Top level helper function."""
    pass
`

	pyFile := filepath.Join(tmpDir, "account.py")
	if err := os.WriteFile(pyFile, []byte(pyCode), 0644); err != nil {
		t.Fatalf("failed to write test python file: %v", err)
	}

	entities, relations, err := ParsePythonFile(pyFile, "test-proj", tmpDir)
	if err != nil {
		t.Fatalf("ParsePythonFile failed: %v", err)
	}

	// Verify structural contents
	foundModule := false
	foundClass := false
	foundMethod := false
	foundTopLevelFunc := false

	for _, ent := range entities {
		switch ent.Type {
		case "Module":
			foundModule = true
			if !strings.Contains(ent.Docstring, "This is a module docstring.") {
				t.Errorf("module docstring parsed incorrectly: '%s'", ent.Docstring)
			}
		case "Class":
			if ent.Name == "UserAccount" {
				foundClass = true
				if ent.Docstring != "Represents a user account." {
					t.Errorf("class docstring parsed incorrectly: '%s'", ent.Docstring)
				}
			}
		case "Method":
			if ent.Name == "get_display_name" {
				foundMethod = true
				if ent.Docstring != "Returns username." {
					t.Errorf("method docstring parsed incorrectly: '%s'", ent.Docstring)
				}
				if !strings.Contains(ent.Code, "def get_display_name") {
					t.Errorf("method code not sliced correctly: '%s'", ent.Code)
				}
			} else if ent.Name == "top_level_function" {
				foundTopLevelFunc = true
				if ent.Docstring != "Top level helper function." {
					t.Errorf("top level function docstring incorrect: '%s'", ent.Docstring)
				}
			}
		}
	}

	if !foundModule || !foundClass || !foundMethod || !foundTopLevelFunc {
		t.Errorf("failed to extract expected structure: module=%t, class=%t, method=%t, topLevel=%t",
			foundModule, foundClass, foundMethod, foundTopLevelFunc)
	}

	// Verify CONTAINS edges
	if len(relations) < 3 {
		t.Errorf("expected at least 3 containment relations, got %d", len(relations))
	}
}

func TestParseGoFile(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "m-cpg-parser-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	goCode := `// Package main is the entry point.
package main

import "fmt"

// Greeter structures messages.
type Greeter struct {
	Prefix string
}

// Greet prints the greeting message.
func (g *Greeter) Greet(name string) string {
	return fmt.Sprintf("%s, %s!", g.Prefix, name)
}

func SimpleHelper() {
	// helper
}
`

	goFile := filepath.Join(tmpDir, "main.go")
	if err := os.WriteFile(goFile, []byte(goCode), 0644); err != nil {
		t.Fatalf("failed to write test go file: %v", err)
	}

	entities, _, err := ParseGoFile(goFile, "test-proj", tmpDir)
	if err != nil {
		t.Fatalf("ParseGoFile failed: %v", err)
	}

	foundStruct := false
	foundMethod := false
	foundFunc := false

	for _, ent := range entities {
		switch ent.Type {
		case "Class":
			if ent.Name == "Greeter" {
				foundStruct = true
				if ent.Docstring != "Greeter structures messages." {
					t.Errorf("struct docstring parsed incorrectly: '%s'", ent.Docstring)
				}
			}
		case "Method":
			if ent.Name == "Greet" {
				foundMethod = true
				if ent.Docstring != "Greet prints the greeting message." {
					t.Errorf("method docstring parsed incorrectly: '%s'", ent.Docstring)
				}
				if ent.ParentID != "class_main.Greeter" {
					t.Errorf("expected parent to be 'class_main.Greeter', got '%s'", ent.ParentID)
				}
			} else if ent.Name == "SimpleHelper" {
				foundFunc = true
			}
		}
	}

	if !foundStruct || !foundMethod || !foundFunc {
		t.Errorf("failed to extract Go structures: struct=%t, method=%t, func=%t",
			foundStruct, foundMethod, foundFunc)
	}
}

func TestParseMarkdownFile(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "m-cpg-parser-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	mdCode := `# ProjectBrief

This is a brief of the project.

## ProductContext

These are details of product context.
`

	mdFile := filepath.Join(tmpDir, "README.md")
	if err := os.WriteFile(mdFile, []byte(mdCode), 0644); err != nil {
		t.Fatalf("failed to write test md file: %v", err)
	}

	entities, relations, err := ParseMarkdownFile(mdFile, "test-proj", tmpDir)
	if err != nil {
		t.Fatalf("ParseMarkdownFile failed: %v", err)
	}

	foundRoot := false
	foundSection := false

	for _, ent := range entities {
		if ent.ID == "doc_README" {
			foundRoot = true
		} else if ent.Name == "ProductContext" {
			foundSection = true
			if !strings.Contains(ent.Docstring, "These are details of product context.") {
				t.Errorf("section docstring parsed incorrectly: '%s'", ent.Docstring)
			}
		}
	}

	if !foundRoot || !foundSection {
		t.Errorf("failed to extract markdown structures: root=%t, section=%t", foundRoot, foundSection)
	}

	if len(relations) == 0 {
		t.Errorf("expected relations, got 0")
	}
}
