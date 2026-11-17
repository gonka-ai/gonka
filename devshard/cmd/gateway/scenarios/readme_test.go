package scenarios

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var readmeTestRow = regexp.MustCompile("^\\| `((?:Test|Fuzz)[A-Za-z0-9]+)` \\|")

// Test flow:
//  1. List every Test and Fuzz function declared in this directory's test files, except TestMain.
//  2. Read the rows of README.md's test tables, whose first cell is a test name.
//  3. Assert every function has exactly one row and every row names a declared function.
func TestEveryTestHasAReadmeRow(t *testing.T) {
	paths, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatalf("filepath.Glob(*_test.go) = %v, want nil", err)
	}
	declared := map[string]bool{}
	fileSet := token.NewFileSet()
	for _, path := range paths {
		parsed, err := parser.ParseFile(fileSet, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parser.ParseFile(%s) = %v, want nil", path, err)
		}
		for _, declaration := range parsed.Decls {
			function, isFunction := declaration.(*ast.FuncDecl)
			if !isFunction || function.Recv != nil || function.Name.Name == "TestMain" {
				continue
			}
			if strings.HasPrefix(function.Name.Name, "Test") || strings.HasPrefix(function.Name.Name, "Fuzz") {
				declared[function.Name.Name] = true
			}
		}
	}
	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatalf("os.ReadFile(README.md) = %v, want nil", err)
	}
	rows := map[string]int{}
	for line := range strings.SplitSeq(string(readme), "\n") {
		if match := readmeTestRow.FindStringSubmatch(line); match != nil {
			rows[match[1]]++
		}
	}
	for name := range declared {
		if rows[name] != 1 {
			t.Errorf("README.md rows for %s = %d, want 1", name, rows[name])
		}
	}
	for name := range rows {
		if !declared[name] {
			t.Errorf("README.md has a row for %s, want a test declared in this directory", name)
		}
	}
}
