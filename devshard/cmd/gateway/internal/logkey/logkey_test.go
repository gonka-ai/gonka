package logkey

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// declared reads this package's own constants, so the vocabulary has exactly one definition.
func declared(t *testing.T) map[string]string {
	t.Helper()
	set := token.NewFileSet()
	parsed, err := parser.ParseFile(set, "logkey.go", nil, 0)
	if err != nil {
		t.Fatalf("parse logkey.go: %v", err)
	}
	names := map[string]string{}
	for _, decl := range parsed.Decls {
		general, isGeneral := decl.(*ast.GenDecl)
		if !isGeneral || general.Tok != token.CONST {
			continue
		}
		for _, spec := range general.Specs {
			value := spec.(*ast.ValueSpec)
			if literal, ok := value.Values[0].(*ast.BasicLit); ok && literal.Kind == token.STRING {
				unquoted, _ := strconv.Unquote(literal.Value)
				names[unquoted] = value.Names[0].Name
			}
		}
	}
	return names
}

func gatewayFiles(t *testing.T) []string {
	t.Helper()
	seen := map[string]bool{}
	for _, pattern := range []string{"../../*.go", "../../*/*.go"} {
		found, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range found {
			if !strings.HasSuffix(path, "_test.go") && !strings.Contains(path, "logkey") {
				seen[path] = true
			}
		}
	}
	paths := make([]string, 0, len(seen))
	for path := range seen {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

// keyLiterals returns the string literals sitting in key position of a key/value run: keys and
// values alternate, so only the even offsets from start are keys.
func keyLiterals(args []ast.Expr, start int) []*ast.BasicLit {
	var keys []*ast.BasicLit
	for i := start; i < len(args); i += 2 {
		if literal, ok := args[i].(*ast.BasicLit); ok && literal.Kind == token.STRING {
			keys = append(keys, literal)
		}
	}
	return keys
}

// anyElement reports []any in both spellings the parser hands back: as the alias or as the written-out empty interface.
func anyElement(element ast.Expr) bool {
	if name, isIdent := element.(*ast.Ident); isIdent {
		return name.Name == "any"
	}
	iface, isInterface := element.(*ast.InterfaceType)
	return isInterface && iface.Methods.NumFields() == 0
}

// loggedKeys finds every key the gateway puts on a log line, in each of the three shapes it builds them in.
func loggedKeys(t *testing.T) map[string]map[string]bool {
	t.Helper()
	set := token.NewFileSet()
	keys := map[string]map[string]bool{}
	note := func(literal *ast.BasicLit) {
		name, _ := strconv.Unquote(literal.Value)
		if keys[name] == nil {
			keys[name] = map[string]bool{}
		}
		keys[name][set.Position(literal.Pos()).String()] = true
	}
	for _, path := range gatewayFiles(t) {
		parsed, err := parser.ParseFile(set, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			switch typed := node.(type) {
			case *ast.CompositeLit:
				if array, isArray := typed.Type.(*ast.ArrayType); isArray && anyElement(array.Elt) {
					for _, literal := range keyLiterals(typed.Elts, 0) {
						note(literal)
					}
				}
			case *ast.CallExpr:
				if selector, isSelector := typed.Fun.(*ast.SelectorExpr); isSelector {
					if pkg, isIdent := selector.X.(*ast.Ident); isIdent && pkg.Name == "logging" {
						switch selector.Sel.Name {
						case "Info", "Warn", "Error", "Debug":
							for _, literal := range keyLiterals(typed.Args, 1) {
								note(literal)
							}
						}
					}
				}
				if name, isIdent := typed.Fun.(*ast.Ident); isIdent && name.Name == "append" && len(typed.Args) > 2 {
					for _, literal := range keyLiterals(typed.Args, 1) {
						note(literal)
					}
				}
			}
			return true
		})
	}
	return keys
}

// Test flow:
//  1. Parse logkey.go for its declared string constants.
//  2. Scan every non-test gateway source file for the log keys it puts on a line.
//  3. Assert every logged key found is among the declared constants, reporting each undeclared key's location.
func TestEveryLoggedKeyIsDeclared(t *testing.T) {
	t.Parallel()
	vocabulary := declared(t)

	for name, where := range loggedKeys(t) {
		if _, known := vocabulary[name]; known {
			continue
		}
		places := make([]string, 0, len(where))
		for place := range where {
			places = append(places, place)
		}
		sort.Strings(places)
		t.Errorf("key %q is not declared in logkey: %s", name, strings.Join(places, ", "))
	}
}

// Test flow:
//  1. Parse logkey.go for its declared string constants.
//  2. Assert every declared value is lowercase and non-empty.
func TestKeysAreLowercase(t *testing.T) {
	t.Parallel()
	for value, name := range declared(t) {
		if value != strings.ToLower(value) || value == "" {
			t.Errorf("%s = %q: a key must be lowercase", name, value)
		}
	}
}
