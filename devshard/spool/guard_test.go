package spool_test

import (
	"bufio"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// allowlistedScratchCreate are call sites not yet migrated onto spool.
// Empty list = plan complete: every production spill goes through spool.
var allowlistedScratchCreate = map[string]bool{}

// TestScratchCreateOnlyInSpool fails if production code outside spool creates
// scratch files via os.CreateTemp / os.MkdirTemp / os.OpenFile(O_CREATE).
func TestScratchCreateOnlyInSpool(t *testing.T) {
	root := findDevshardRoot(t)
	// Scope: gateway + host spill call sites only (not durable storage, testenv, …).
	roots := []string{
		filepath.Join(root, "cmd", "devshardctl"),
		filepath.Join(root, "host"),
	}
	var violations []string
	for _, base := range roots {
		err := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if allowlistedScratchCreate[rel] {
				return nil
			}
			hits := findScratchCreates(t, path)
			for _, h := range hits {
				violations = append(violations, rel+": "+h)
			}
			return nil
		})
		require.NoError(t, err)
	}
	if len(violations) > 0 {
		t.Fatalf("scratch file creation outside spool (migrate or allow-list):\n  %s", strings.Join(violations, "\n  "))
	}
}

func findDevshardRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
			require.NoError(t, err)
			if strings.Contains(string(data), "module devshard") {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("devshard module root not found")
		}
		dir = parent
	}
}

func findScratchCreates(t *testing.T, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		// Fall back to line scan if parse fails.
		return scanScratchCreates(path)
	}
	var hits []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "os" {
			return true
		}
		switch sel.Sel.Name {
		case "CreateTemp", "MkdirTemp":
			hits = append(hits, sel.Sel.Name+"()")
		case "OpenFile":
			if callUsesOCreate(call) {
				hits = append(hits, "OpenFile(O_CREATE)")
			}
		}
		return true
	})
	return hits
}

func callUsesOCreate(call *ast.CallExpr) bool {
	if len(call.Args) < 2 {
		return false
	}
	return exprMentionsOCreate(call.Args[1])
}

func exprMentionsOCreate(e ast.Expr) bool {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name == "O_CREATE"
	case *ast.SelectorExpr:
		if id, ok := t.X.(*ast.Ident); ok && id.Name == "os" && t.Sel.Name == "O_CREATE" {
			return true
		}
		return exprMentionsOCreate(t.X)
	case *ast.BinaryExpr:
		return exprMentionsOCreate(t.X) || exprMentionsOCreate(t.Y)
	case *ast.ParenExpr:
		return exprMentionsOCreate(t.X)
	default:
		return false
	}
}

func scanScratchCreates(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var hits []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.Contains(line, "os.CreateTemp(") || strings.Contains(line, "os.MkdirTemp(") {
			hits = append(hits, "line-scan create")
		}
		if strings.Contains(line, "os.OpenFile(") && strings.Contains(line, "O_CREATE") {
			hits = append(hits, "line-scan OpenFile")
		}
	}
	return hits
}
