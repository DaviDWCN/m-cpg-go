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

func TestParseTSFile(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "m-cpg-parser-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	tsCode := `// Helper TS file
export class Greeter {
    private prefix: string;

    constructor(prefix: string) {
        this.prefix = prefix;
    }

    public greet(name: string): string {
        console.log("Greeting...");
        return this.prefix + name;
    }
}

export const helperFunction = (val: number) => {
    return val * 2;
}

async function doSomething() {
    helperFunction(10);
}
`

	tsFile := filepath.Join(tmpDir, "helper.ts")
	if err := os.WriteFile(tsFile, []byte(tsCode), 0644); err != nil {
		t.Fatalf("failed to write test ts file: %v", err)
	}

	entities, relations, err := ParseTSFile(tsFile, "test-proj", tmpDir)
	if err != nil {
		t.Fatalf("ParseTSFile failed: %v", err)
	}

	foundClass := false
	foundMethod := false
	foundArrowFunc := false
	foundAsyncFunc := false
	foundCalls := false

	for _, ent := range entities {
		switch ent.Type {
		case "Class":
			if ent.Name == "Greeter" {
				foundClass = true
			}
		case "Method":
			if ent.Name == "greet" {
				foundMethod = true
				if ent.ParentID != "class_helper.Greeter" {
					t.Errorf("expected parent to be 'class_helper.Greeter', got '%s'", ent.ParentID)
				}
			} else if ent.Name == "helperFunction" {
				foundArrowFunc = true
			} else if ent.Name == "doSomething" {
				foundAsyncFunc = true
			}
		}
	}

	if !foundClass || !foundMethod || !foundArrowFunc || !foundAsyncFunc {
		t.Errorf("failed to extract TS structures: class=%t, method=%t, arrow=%t, async=%t",
			foundClass, foundMethod, foundArrowFunc, foundAsyncFunc)
	}

	for _, rel := range relations {
		if rel.Source == "method_helper.doSomething" && rel.Target == "call_helperFunction" && rel.Label == "CALLS" {
			foundCalls = true
		}
	}

	if !foundCalls {
		t.Errorf("failed to extract TS CALLS relation")
	}
}

func TestParseJavaFile(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "m-cpg-parser-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	javaCode := `
package com.example.service;

import java.io.IOException;

/**
 * Service to manage customer operations.
 */
public class CustomerService implements Service {
    
    private String version = "1.0";

    /**
     * Creates a new customer.
     */
    public void createCustomer(String name, int age) throws IOException {
        System.out.println("Customer created");
    }
}
`
	filePath := filepath.Join(tmpDir, "CustomerService.java")
	err = os.WriteFile(filePath, []byte(javaCode), 0644)
	if err != nil {
		t.Fatalf("failed to write java file: %v", err)
	}

	entities, relations, err := ParseFile(filePath, "test-proj", tmpDir)
	if err != nil {
		t.Fatalf("ParseFile failed: %v", err)
	}

	foundModule := false
	foundClass := false
	foundMethod := false

	for _, ent := range entities {
		switch ent.Type {
		case "Module":
			if ent.Name == "CustomerService" && ent.FQN == "com.example.service.CustomerService" {
				foundModule = true
			}
		case "Class":
			if ent.Name == "CustomerService" && ent.FQN == "com.example.service.CustomerService" {
				foundClass = true
			}
		case "Method":
			if ent.Name == "createCustomer" && ent.FQN == "com.example.service.CustomerService.createCustomer" {
				foundMethod = true
				if !strings.Contains(ent.Docstring, "Creates a new customer.") {
					t.Errorf("expected docstring to contain description, got '%s'", ent.Docstring)
				}
			}
		}
	}

	if !foundModule || !foundClass || !foundMethod {
		t.Fatalf("failed to extract Java structures: module=%t, class=%t, method=%t",
			foundModule, foundClass, foundMethod)
	}

	foundContains := false
	for _, rel := range relations {
		if rel.Source == "class_com.example.service.CustomerService" && rel.Target == "method_com.example.service.CustomerService.createCustomer" && rel.Label == "CONTAINS" {
			foundContains = true
		}
	}
	if !foundContains {
		t.Errorf("failed to extract Java CONTAINS relation")
	}
}
