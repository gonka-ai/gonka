package app

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"devshard/cmd/gateway/api"
)

func guardedTree(t *testing.T) (base, guarded string) {
	t.Helper()
	root := t.TempDir()
	base = filepath.Join(root, "gateway")
	guarded = filepath.Join(root, "victim")
	for _, dir := range []string{filepath.Join(base, "escrow-7"), guarded} {
		if err := os.MkdirAll(dir, storageDirectoryMode); err != nil {
			t.Fatalf("preparing %s: %v", dir, err)
		}
	}
	return base, guarded
}

// Test flow:
//  1. Build a base storage tree and a sibling `guarded` directory outside it.
//  2. Define a table of target paths, varying across an in-base escrow path, parent traversal, an absolute path outside the base, a URL-encoded traversal, and a sibling directory sharing the base's prefix.
//  3. For each case, call `removeDevshardStorage` on the target and the base.
//  4. Assert the in-base case succeeds and its directory is gone, and every other case fails with the guarded directory left untouched.
func TestRemoveDevshardStorageRefusesAPathOutsideItsBase(t *testing.T) {
	testCases := []struct {
		name       string
		target     func(base, guarded string) string
		wantRemove bool
	}{
		{
			name:       "a path inside the base is removed",
			target:     func(base, _ string) string { return filepath.Join(base, "escrow-7") },
			wantRemove: true,
		},
		{
			name:   "parent traversal",
			target: func(base, _ string) string { return base + "/../victim" },
		},
		{
			name:   "absolute path",
			target: func(_, guarded string) string { return guarded },
		},
		{
			name:   "url encoded traversal",
			target: func(base, _ string) string { return base + "/" + mustUnescape(t, "%2e%2e%2fvictim") },
		},
		{
			name:   "a sibling sharing the base's prefix",
			target: func(base, _ string) string { return base + "-other" },
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			base, guarded := guardedTree(t)
			err := removeDevshardStorage(testCase.target(base, guarded), base)

			if testCase.wantRemove {
				if err != nil {
					t.Fatalf("removing an in-base path: %v", err)
				}
				if _, statErr := os.Stat(filepath.Join(base, "escrow-7")); !os.IsNotExist(statErr) {
					t.Fatalf("the in-base directory survived: %v", statErr)
				}
				return
			}
			if err == nil {
				t.Fatalf("removeDevshardStorage accepted a path outside %q", base)
			}
			if _, statErr := os.Stat(guarded); statErr != nil {
				t.Fatalf("the guarded directory was deleted: %v", statErr)
			}
		})
	}
}

// Test flow:
//  1. Build a base storage tree and a sibling `guarded` directory outside it.
//  2. Define a table of escrow IDs, varying across parent segments after a real one, repeated parent segments, and URL-encoded parent segments.
//  3. For each case, resolve the escrow ID through `api.DevshardStoragePath` and call `removeDevshardStorage`.
//  4. Assert every case fails and the guarded directory survives.
func TestAnEscrowIdCannotEscapeTheBaseStorageDir(t *testing.T) {
	testCases := []struct {
		name     string
		escrowID string
	}{
		{name: "parent segments after a real one", escrowID: "7/../../victim"},
		{name: "repeated parent segments", escrowID: "a/../../.."},
		{name: "url encoded parent segments", escrowID: mustUnescape(t, "7%2f%2e%2e%2f%2e%2e%2fvictim")},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			base, guarded := guardedTree(t)
			err := removeDevshardStorage(api.DevshardStoragePath(base, testCase.escrowID), base)
			if err == nil {
				t.Fatalf("escrow id %q reached %q", testCase.escrowID, filepath.Clean(api.DevshardStoragePath(base, testCase.escrowID)))
			}
			if _, statErr := os.Stat(guarded); statErr != nil {
				t.Fatalf("the guarded directory was deleted: %v", statErr)
			}
		})
	}
}

// Test flow:
//  1. Call `removeDevshardStorage` with the base directory as both its own target and base.
//  2. Assert it fails.
//  3. Assert the base directory still exists.
func TestRemoveDevshardStorageRefusesTheBaseDirectoryItself(t *testing.T) {
	base := t.TempDir()
	if err := removeDevshardStorage(base, base); err == nil {
		t.Fatal("removeDevshardStorage deleted its own base directory")
	}
	if _, err := os.Stat(base); err != nil {
		t.Fatalf("the base directory was deleted: %v", err)
	}
}

func mustUnescape(t *testing.T, encoded string) string {
	t.Helper()
	decoded, err := url.PathUnescape(encoded)
	if err != nil {
		t.Fatalf("unescaping %q: %v", encoded, err)
	}
	return decoded
}
