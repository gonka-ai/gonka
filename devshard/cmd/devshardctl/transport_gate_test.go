package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Production gateway paths must not call REST chain clients (Track C/D/E).
var restChainForbiddenCalls = []string{
	"NewRESTBridge",
	"NewRESTChainTxClient",
	"NewRESTChainFetcher",
}

var restChainForbiddenLiterals = []string{
	"DEVSHARD_CHAIN_REST",
	"DEVSHARD_TX_QUERY_REST",
}

var restChainExcludedFiles = map[string]bool{
	"transport_gate_test.go": true,
}

func TestG4_NoRESTChainClientsInGatewayProduction(t *testing.T) {
	dir := "."
	fset := token.NewFileSet()
	var violations []string

	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		base := filepath.Base(path)
		if strings.HasSuffix(base, "_test.go") || restChainExcludedFiles[base] {
			return nil
		}

		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(raw)
		for _, forbidden := range restChainForbiddenLiterals {
			if strings.Contains(text, forbidden) {
				violations = append(violations, path+": contains "+forbidden)
			}
		}

		f, err := parser.ParseFile(fset, path, raw, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := callName(call.Fun)
			for _, forbidden := range restChainForbiddenCalls {
				if name == forbidden {
					pos := fset.Position(call.Pos())
					violations = append(violations, pos.String()+": "+name)
				}
			}
			return true
		})
		return nil
	})
	require.NoError(t, err)
	require.Empty(t, violations, "REST chain transport must not be used in production gateway code")
}

func callName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return e.Sel.Name
	default:
		return ""
	}
}
