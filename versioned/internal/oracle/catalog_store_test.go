package oracle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCatalogStorePersistsMonotonicCatalog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "catalog.json")
	store, err := OpenCatalogStore(path)
	if err != nil {
		t.Fatalf("OpenCatalogStore: %v", err)
	}
	if _, ok := store.Current(); ok {
		t.Fatal("new store unexpectedly has a current catalog")
	}

	first := catalogForTest(7,
		Version{Name: "v4", Binary: "https://example.test/v4", SHA256: testSHA256A},
	)
	got, changed, err := store.Accept(first)
	if err != nil {
		t.Fatalf("Accept first catalog: %v", err)
	}
	if !changed || got.Revision != 7 {
		t.Fatalf("first acceptance = (%+v, %v), want revision 7 changed", got, changed)
	}

	reordered := catalogForTest(8,
		Version{Name: "v5", Binary: "https://example.test/v5", SHA256: testSHA256B},
		Version{Name: "v4", Binary: "https://example.test/v4-r2", SHA256: testSHA256B},
	)
	got, changed, err = store.Accept(reordered)
	if err != nil {
		t.Fatalf("Accept append and artifact update: %v", err)
	}
	if !changed || len(got.Versions) != 2 {
		t.Fatalf("second acceptance = (%+v, %v), want two versions changed", got, changed)
	}

	reopened, err := OpenCatalogStore(path)
	if err != nil {
		t.Fatalf("reopen catalog store: %v", err)
	}
	persisted, ok := reopened.Current()
	if !ok || !catalogContentEqual(reordered, persisted) || persisted.Revision != 8 {
		t.Fatalf("persisted catalog = (%+v, %v), want revision 8", persisted, ok)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat catalog snapshot: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("catalog mode = %o, want 600", info.Mode().Perm())
	}
}

func TestCatalogStoreAcceptsIdenticalSameRevisionInAnyOrder(t *testing.T) {
	store, err := OpenCatalogStore(filepath.Join(t.TempDir(), "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	first := catalogForTest(4,
		Version{Name: "v4", Binary: "https://example.test/v4", SHA256: testSHA256A},
		Version{Name: "v5", Binary: "https://example.test/v5", SHA256: testSHA256B},
	)
	if _, _, err := store.Accept(first); err != nil {
		t.Fatal(err)
	}
	replay := catalogForTest(4, first.Versions[1], first.Versions[0])
	_, changed, err := store.Accept(replay)
	if err != nil {
		t.Fatalf("Accept same-revision reorder: %v", err)
	}
	if changed {
		t.Fatal("identical same-revision replay reported a change")
	}
}

func TestCatalogStoreRejectsNonMonotonicProgression(t *testing.T) {
	base := catalogForTest(10,
		Version{Name: "v4", Binary: "https://example.test/v4", SHA256: testSHA256A},
		Version{Name: "v5", Binary: "https://example.test/v5", SHA256: testSHA256B},
	)
	tests := []struct {
		name      string
		candidate VersionConfig
		want      string
	}{
		{name: "revision regression", candidate: catalogForTest(9, base.Versions...), want: "regressed"},
		{name: "same revision mutation", candidate: catalogForTest(10,
			Version{Name: "v4", Binary: "https://example.test/changed", SHA256: testSHA256A},
			base.Versions[1],
		), want: "changed in place"},
		{name: "removed version", candidate: catalogForTest(11, base.Versions[1]), want: "removed version"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, err := OpenCatalogStore(filepath.Join(t.TempDir(), "catalog.json"))
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := store.Accept(base); err != nil {
				t.Fatal(err)
			}
			if _, _, err := store.Accept(tt.candidate); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Accept error = %v, want containing %q", err, tt.want)
			}
			current, ok := store.Current()
			if !ok || current.Revision != base.Revision || !catalogContentEqual(base, current) {
				t.Fatalf("rejected candidate changed current catalog: %+v", current)
			}
		})
	}
}

func TestOpenCatalogStoreFailsClosedOnCorruptSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.json")
	if err := os.WriteFile(path, []byte(`{"schema":1,"initialized":true,"revision":3,"versions":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenCatalogStore(path); err == nil {
		t.Fatal("expected corrupt durable catalog to fail startup")
	}
}

func TestCatalogStoreDoesNotAdvanceMemoryWhenPersistenceFails(t *testing.T) {
	dir := t.TempDir()
	parent := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(parent, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenCatalogStore(filepath.Join(parent, "catalog.json"))
	if err == nil {
		t.Fatal("opening below a non-directory unexpectedly succeeded")
	}

	store = &CatalogStore{path: filepath.Join(parent, "catalog.json")}
	if _, _, err := store.Accept(catalogForTest(1)); err == nil {
		t.Fatal("expected persistence failure")
	}
	if _, ok := store.Current(); ok {
		t.Fatal("failed persistence advanced in-memory authority")
	}
}

func catalogForTest(revision int64, versions ...Version) VersionConfig {
	return VersionConfig{
		Schema:      1,
		Initialized: true,
		Revision:    revision,
		Versions:    append([]Version(nil), versions...),
	}
}
