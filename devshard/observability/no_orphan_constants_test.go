package observability

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoOrphanReasonsAndWheres asserts that every declared Reason* and Where*
// constant is referenced somewhere across the gonka-fork repo (devshard +
// decentralized-api modules) outside its declaration. This protects against
// the kind of dead-constant accumulation that Phase 0 item 1 had to clean up
// (21 unused Reason*, 6 unused Where*).
//
// New constants must either be wired into a call site or removed; the test
// fails fast so the maintainer notices at PR time.
func TestNoOrphanReasonsAndWheres(t *testing.T) {
	repoRoot := findRepoRootForOrphanScan(t)
	devshardDir := filepath.Join(repoRoot, "devshard")
	dapiDir := filepath.Join(repoRoot, "decentralized-api")

	declared := collectReasonAndWhereDecls(t, devshardDir)
	if len(declared) == 0 {
		t.Fatalf("no Reason*/Where* declarations found — walk root may be wrong")
	}

	used := collectIdentifierReferences(t, devshardDir, declared)
	if _, err := os.Stat(dapiDir); err == nil {
		dapiUsed := collectIdentifierReferences(t, dapiDir, declared)
		for name, n := range dapiUsed {
			used[name] += n
		}
	}

	// Anything declared but never referenced outside its own decl is orphan.
	// (Each declaration counts as one occurrence of the identifier; we accept
	// >=2 as "used somewhere".)
	var orphans []string
	for name, count := range used {
		if count < 2 {
			orphans = append(orphans, name)
		}
	}
	if len(orphans) > 0 {
		t.Fatalf("unused observability constants (declare or remove): %v", orphans)
	}
}

// findRepoRootForOrphanScan walks up from cwd until it finds a directory that
// contains both `devshard/` and `decentralized-api/`.
func findRepoRootForOrphanScan(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 8; i++ {
		_, e1 := os.Stat(filepath.Join(dir, "devshard"))
		_, e2 := os.Stat(filepath.Join(dir, "decentralized-api"))
		if e1 == nil && e2 == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("could not locate gonka-fork repo root")
	return ""
}

// collectReasonAndWhereDecls parses every .go file under root and returns the
// set of identifier names declared as `Reason* Reason = "..."` or
// `Where* Where = "..."`.
func collectReasonAndWhereDecls(t *testing.T, root string) map[string]struct{} {
	t.Helper()
	out := make(map[string]struct{})
	fset := token.NewFileSet()
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			return nil // skip non-parseable
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || vs.Type == nil {
					continue
				}
				typeName, _ := vs.Type.(*ast.Ident)
				if typeName == nil {
					continue
				}
				if typeName.Name != "Reason" && typeName.Name != "Where" {
					continue
				}
				for _, name := range vs.Names {
					if !strings.HasPrefix(name.Name, "Reason") && !strings.HasPrefix(name.Name, "Where") {
						continue
					}
					out[name.Name] = struct{}{}
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	return out
}

// collectIdentifierReferences counts occurrences of each declared identifier
// across all .go files in the module (including tests). Returns a map
// name -> count. A declaration itself counts as one reference.
func collectIdentifierReferences(t *testing.T, root string, names map[string]struct{}) map[string]int {
	t.Helper()
	counts := make(map[string]int, len(names))
	for n := range names {
		counts[n] = 0
	}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.Contains(path, "/vendor/") {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		// Cheap word-boundary count via strings.Count + boundary check; we
		// avoid go/parser here to also catch references in test files.
		text := string(data)
		for name := range names {
			counts[name] += countWordOccurrences(text, name)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	return counts
}

// countWordOccurrences returns the number of times `word` appears in `text`
// surrounded by non-identifier characters (so ReasonOK does not match
// ReasonOKExtended).
func countWordOccurrences(text, word string) int {
	var n int
	for i := 0; ; {
		j := strings.Index(text[i:], word)
		if j < 0 {
			break
		}
		start := i + j
		end := start + len(word)
		leftOK := start == 0 || !isIdentByte(text[start-1])
		rightOK := end == len(text) || !isIdentByte(text[end])
		if leftOK && rightOK {
			n++
		}
		i = end
	}
	return n
}

func isIdentByte(b byte) bool {
	return b == '_' ||
		(b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9')
}
