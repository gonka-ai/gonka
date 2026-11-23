package gatewayprotocol_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const treeImportPath = "devshard/gatewayprotocol"

func layerImports() map[string][]string {
	return map[string][]string{
		"probe":          {"devshard/state", "devshard/user"},
		"settlement":     {"devshard/state", "devshard/types"},
		"settlementwire": {"devshard/gatewayprotocol/settlement", "github.com/productscience/inference/x/inference/types"},
		"streamevidence": {"common/completionapi"},
		"timeoutclaim":   {"devshard/host", "devshard/types", "devshard/user"},
	}
}

func forbiddenImports() []string {
	return []string{"sync", "sync/atomic", "net/http", "os", "log", "log/slog", "devshard/logging", "devshard/observability"}
}

func forbiddenImportPrefixes() []string {
	return []string{"devshard/cmd/", "github.com/prometheus/"}
}

func localModules() []string {
	return []string{"devshard", "common"}
}

func treeImporterPatterns() []string {
	return []string{"cmd/gateway/...", "cmd/devshardctl", "gatewayprotocol/..."}
}

func matchesPattern(directory, pattern string) bool {
	subtree, isSubtree := strings.CutSuffix(pattern, "/...")
	if !isSubtree {
		return directory == pattern
	}
	return directory == subtree || strings.HasPrefix(directory, subtree+"/")
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("reading go.mod above the tree: %v", err)
	}
	if !strings.HasPrefix(string(data), "module devshard\n") {
		t.Fatalf("%s is not the devshard module root", root)
	}
	return root
}

func isProductionGoFile(path string) bool {
	return strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go")
}

func isStandardLibrary(importPath string) bool {
	firstElement, _, _ := strings.Cut(importPath, "/")
	return !strings.Contains(firstElement, ".") && !slices.Contains(localModules(), firstElement)
}

func isForbidden(importPath string) bool {
	if slices.Contains(forbiddenImports(), importPath) {
		return true
	}
	for _, prefix := range forbiddenImportPrefixes() {
		if strings.HasPrefix(importPath, prefix) {
			return true
		}
	}
	return false
}

func fileImports(t *testing.T, set *token.FileSet, path string) []string {
	t.Helper()
	parsed, err := parser.ParseFile(set, path, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	imports := make([]string, 0, len(parsed.Imports))
	for _, imported := range parsed.Imports {
		importPath, err := strconv.Unquote(imported.Path.Value)
		if err != nil {
			t.Fatalf("%s: import %s: %v", path, imported.Path.Value, err)
		}
		imports = append(imports, importPath)
	}
	return imports
}

func productionFilesByDirectory(t *testing.T, base string) map[string][]string {
	t.Helper()
	filesByDirectory := map[string][]string{}
	err := filepath.WalkDir(base, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && entry.Name() == "testdata" {
			return filepath.SkipDir
		}
		if entry.IsDir() || !isProductionGoFile(path) {
			return nil
		}
		directory, err := filepath.Rel(base, filepath.Dir(path))
		if err != nil {
			return err
		}
		filesByDirectory[filepath.ToSlash(directory)] = append(filesByDirectory[filepath.ToSlash(directory)], path)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return filesByDirectory
}

// Test flow:
//  1. Collect the production Go files of every directory under the tree.
//  2. Assert every such directory has a row in the layer table and a `README.md`, and every row has such a directory.
//  3. Parse each file's imports and assert each one is in its row or the standard library, and none is forbidden.
func TestTreePackagesImportOnlyTheirLayer(t *testing.T) {
	filesByDirectory := productionFilesByDirectory(t, ".")
	layers := layerImports()
	for directory := range layers {
		if len(filesByDirectory[directory]) == 0 {
			t.Errorf("layer %s has no production Go files: the guard would pass without checking it", directory)
		}
	}

	set := token.NewFileSet()
	for directory, paths := range filesByDirectory {
		allowed, hasRow := layers[directory]
		if !hasRow {
			t.Errorf("%s has production Go files but no row in the layer table", directory)
			continue
		}
		if _, err := os.Stat(filepath.Join(directory, "README.md")); err != nil {
			t.Errorf("%s has no README.md: %v", directory, err)
		}
		for _, path := range paths {
			for _, importPath := range fileImports(t, set, path) {
				switch {
				case isForbidden(importPath):
					t.Errorf("%s imports %s, which is forbidden in the tree", path, importPath)
				case !isStandardLibrary(importPath) && !slices.Contains(allowed, importPath):
					t.Errorf("%s imports %s, which is not in the %s row of the layer table", path, importPath, directory)
				}
			}
		}
	}
}

// Test flow:
//  1. Collect the production Go files of every directory in the devshard module.
//  2. Parse each file's imports.
//  3. Assert an import of the tree comes only from `cmd/gateway/...`, `cmd/devshardctl` or the tree itself, and that at least one allowed import was seen.
func TestOnlyGatewaysImportTheTree(t *testing.T) {
	root := moduleRoot(t)
	set := token.NewFileSet()
	checked, allowedTreeImports := 0, 0
	for directory, paths := range productionFilesByDirectory(t, root) {
		isImporter := slices.ContainsFunc(treeImporterPatterns(), func(pattern string) bool {
			return matchesPattern(directory, pattern)
		})
		for _, path := range paths {
			checked++
			for _, importPath := range fileImports(t, set, path) {
				isTreeImport := importPath == treeImportPath || strings.HasPrefix(importPath, treeImportPath+"/")
				switch {
				case isTreeImport && !isImporter:
					t.Errorf("%s imports %s; only the gateways may import the tree", path, importPath)
				case isTreeImport:
					allowedTreeImports++
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no production Go files in the devshard module: the guard would pass without checking anything")
	}
	if allowedTreeImports == 0 {
		t.Fatalf("no production file imports %s: the guard would pass without checking anything", treeImportPath)
	}
}
