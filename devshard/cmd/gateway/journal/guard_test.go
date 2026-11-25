package journal

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// lifecyclePackages write their lines through the journal. See README.md, "Who may log directly".
var lifecyclePackages = []string{"api", "chain", "engine", "escrow", "limits", "perf", "registry", "scheduler", "warmup"}

// narratingPackages reach the journal through interfaces they declare; api alone imports it, for RequestLine.
var narratingPackages = []string{"chain", "engine", "escrow", "limits", "nonces", "perf", "registry", "scheduler", "warmup"}

// plainLoggingFiles write an operator's own action or a refused admin call, which belong to no lifecycle.
func plainLoggingFiles() []string {
	return []string{filepath.Join("..", "api", "admin.go"), filepath.Join("..", "api", "errors.go")}
}

func sourceFiles(t *testing.T, packageName string) []string {
	t.Helper()
	found, err := filepath.Glob(filepath.Join("..", packageName, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	sources := make([]string, 0, len(found))
	for _, path := range found {
		if !strings.HasSuffix(path, "_test.go") {
			sources = append(sources, path)
		}
	}
	if len(sources) == 0 {
		t.Fatalf("no Go files under %s: the guard would pass without checking anything", packageName)
	}
	return sources
}

// loggingIdentifier is the name a file calls devshard/logging by, so an import alias cannot hide a call.
func loggingIdentifier(parsed *ast.File) string {
	for _, imported := range parsed.Imports {
		if imported.Path.Value == `"devshard/logging"` && imported.Name != nil {
			return imported.Name.Name
		}
	}
	return "logging"
}

// Test flow:
//  1. Collect every non-test source file in `lifecyclePackages` plus `app/observers.go`, skipping the files `plainLoggingFiles` allows to log directly.
//  2. Parse each file's AST.
//  3. Walk the AST for calls to `logging.Info`, `Warn`, `Error`, or `Debug`, resolving the file's own import alias via `loggingIdentifier`.
//  4. Assert no such call exists outside the journal.
func TestLifecyclePackagesWriteNoLineAroundTheJournal(t *testing.T) {
	paths := []string{filepath.Join("..", "app", "observers.go")}
	for _, packageName := range lifecyclePackages {
		for _, path := range sourceFiles(t, packageName) {
			if !slices.Contains(plainLoggingFiles(), path) {
				paths = append(paths, path)
			}
		}
	}

	set := token.NewFileSet()
	for _, path := range paths {
		parsed, err := parser.ParseFile(set, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		loggingName := loggingIdentifier(parsed)
		ast.Inspect(parsed, func(node ast.Node) bool {
			call, isCall := node.(*ast.CallExpr)
			if !isCall {
				return true
			}
			selector, isSelector := call.Fun.(*ast.SelectorExpr)
			if !isSelector {
				return true
			}
			receiver, isIdentifier := selector.X.(*ast.Ident)
			if !isIdentifier || receiver.Name != loggingName {
				return true
			}
			switch selector.Sel.Name {
			case "Info", "Warn", "Error", "Debug":
				t.Errorf("%s: logging.%s writes a lifecycle line around the journal", set.Position(call.Pos()), selector.Sel.Name)
			}
			return true
		})
	}
}

// Test flow:
//  1. Collect every non-test source file in `narratingPackages`.
//  2. Parse each file's imports only.
//  3. Assert none of them imports `devshard/cmd/gateway/journal` directly.
func TestProducersNeverImportTheJournal(t *testing.T) {
	set := token.NewFileSet()
	for _, packageName := range narratingPackages {
		for _, path := range sourceFiles(t, packageName) {
			parsed, err := parser.ParseFile(set, path, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			for _, imported := range parsed.Imports {
				if importPath, _ := strconv.Unquote(imported.Path.Value); importPath == "devshard/cmd/gateway/journal" {
					t.Errorf("%s imports the journal; declare a narrator interface there instead", path)
				}
			}
		}
	}
}
