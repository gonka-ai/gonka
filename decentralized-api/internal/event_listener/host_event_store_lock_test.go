package event_listener

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHostEventStore_SQLiteWriterLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), hostEventDBFile)
	first, err := openSQLiteStore(path, "same-writer")
	require.NoError(t, err)
	t.Cleanup(func() { _ = first.Close() })
	_, err = openSQLiteStore(path, "same-writer")
	require.ErrorIs(t, err, ErrWriterLocked)

	alias := path + ".alias"
	require.NoError(t, os.Symlink(path, alias))
	_, err = openSQLiteStore(alias, "same-writer")
	require.ErrorIs(t, err, ErrWriterLocked)
	other, err := openSQLiteStore(path, "other-writer")
	require.NoError(t, err, "different participants may share a database")
	t.Cleanup(func() { _ = other.Close() })

	checkChild := func(want string) {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=^TestHostEventStore_SQLiteWriterLockChild$")
		cmd.Env = append(os.Environ(), "GONKA_SQLITE_LOCK_TEST_PATH="+path, "GONKA_SQLITE_LOCK_TEST_EXPECT="+want)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", out)
	}
	checkChild("locked")
	require.NoError(t, first.Close())
	checkChild("available")
	second, err := openSQLiteStore(path, "same-writer")
	require.NoError(t, err, "ownership is released when the previous store closes")
	require.NoError(t, second.Close())
}

func TestHostEventStore_SQLiteWriterLockDanglingSymlink(t *testing.T) {
	for _, tc := range []struct {
		name        string
		absolute    bool
		chain       bool
		parentAlias bool
	}{
		{name: "relative"},
		{name: "absolute", absolute: true},
		{name: "chain", chain: true},
		{name: "parent_alias", parentAlias: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "actual.sqlite")
			linkTarget := filepath.Base(target)
			if tc.absolute {
				linkTarget = target
			}
			if tc.chain {
				require.NoError(t, os.Symlink(linkTarget, filepath.Join(dir, "middle.sqlite")))
				linkTarget = "middle.sqlite"
			}
			path := filepath.Join(dir, hostEventDBFile)
			require.NoError(t, os.Symlink(linkTarget, path))
			if tc.parentAlias {
				parent := filepath.Join(t.TempDir(), "data")
				require.NoError(t, os.Symlink(dir, parent))
				path = filepath.Join(parent, hostEventDBFile)
			}
			_, err := os.Stat(target)
			require.True(t, os.IsNotExist(err), "target must not exist at the first open")
			first, err := openSQLiteStore(path, "same-writer")
			require.NoError(t, err)
			t.Cleanup(func() { _ = first.Close() })
			require.FileExists(t, target)
			for _, contender := range []string{path, target} {
				second, err := openSQLiteStore(contender, "same-writer")
				if second != nil {
					_ = second.Close()
				}
				require.ErrorIs(t, err, ErrWriterLocked, "same database via %s", contender)
			}
			assertSQLiteWriterChild(t, path, "locked")
			require.NoError(t, first.Close())
			assertSQLiteWriterChild(t, target, "available")
		})
	}
}

func TestHostEventStore_SQLiteWriterLockSymlinkParentTraversal(t *testing.T) {
	for _, tc := range []struct {
		name     string
		absolute bool
		existing bool
		direct   bool
	}{
		{name: "relative_existing", existing: true},
		{name: "relative_missing"},
		{name: "absolute_existing", absolute: true, existing: true},
		{name: "absolute_missing", absolute: true},
		{name: "direct_existing", direct: true, existing: true},
		{name: "direct_missing", direct: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(dir, "real", "sub"), 0o700))
			require.NoError(t, os.Symlink("real/sub", filepath.Join(dir, "link")))
			target := filepath.Join(dir, "real", "events.sqlite")
			if tc.existing {
				seed, err := openSQLiteStore(target, "same-writer")
				require.NoError(t, err)
				require.NoError(t, seed.Reset(t.Context(), 77))
				require.NoError(t, seed.Close())
			}
			linkTarget := "link/../events.sqlite"
			if tc.absolute || tc.direct {
				linkTarget = dir + "/" + linkTarget
			}
			path := linkTarget
			if !tc.direct {
				path = filepath.Join(dir, hostEventDBFile)
				require.NoError(t, os.Symlink(linkTarget, path))
			}
			first, err := openSQLiteStore(path, "same-writer")
			require.NoError(t, err)
			t.Cleanup(func() { _ = first.Close() })
			require.FileExists(t, target)
			require.NoFileExists(t, filepath.Join(dir, "events.sqlite"), "do not create a different journal")
			if tc.existing {
				head, err := first.Head(t.Context())
				require.NoError(t, err)
				require.Equal(t, uint64(77), head.Generation, "preserve the existing journal")
			}
			second, err := openSQLiteStore(target, "same-writer")
			if second != nil {
				_ = second.Close()
			}
			require.ErrorIs(t, err, ErrWriterLocked)
		})
	}
}

func TestHostEventStore_SQLiteWriterLockSymlinkLoop(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, hostEventDBFile)
	require.NoError(t, os.Symlink("other.sqlite", path))
	require.NoError(t, os.Symlink(hostEventDBFile, filepath.Join(dir, "other.sqlite")))
	_, err := openSQLiteStore(path, "same-writer")
	require.ErrorIs(t, err, syscall.ELOOP)
}

func assertSQLiteWriterChild(t *testing.T, path, want string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHostEventStore_SQLiteWriterLockChild$")
	cmd.Env = append(os.Environ(), "GONKA_SQLITE_LOCK_TEST_PATH="+path, "GONKA_SQLITE_LOCK_TEST_EXPECT="+want)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
}

func TestHostEventStore_SQLiteWriterLockChild(t *testing.T) {
	path := os.Getenv("GONKA_SQLITE_LOCK_TEST_PATH")
	if path == "" {
		return
	}
	s, err := openSQLiteStore(path, "same-writer")
	if os.Getenv("GONKA_SQLITE_LOCK_TEST_EXPECT") == "locked" {
		require.ErrorIs(t, err, ErrWriterLocked)
		return
	}
	require.NoError(t, err)
	require.NoError(t, s.Close())
}
