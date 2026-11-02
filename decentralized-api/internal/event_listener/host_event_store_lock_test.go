package event_listener

import (
	"os"
	"os/exec"
	"path/filepath"
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
