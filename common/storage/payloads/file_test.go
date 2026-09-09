package payloads

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFileStorage_StoreRetrieveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	fs := NewFileStorage(dir)
	ctx := context.Background()

	require.NoError(t, fs.Store(ctx, "42", 7, 3, []byte("prompt"), []byte("response")))
	prompt, response, err := fs.Retrieve(ctx, "42", 7, 3)
	require.NoError(t, err)
	require.Equal(t, []byte("prompt"), prompt)
	require.Equal(t, []byte("response"), response)

	_, err = os.Stat(filepath.Join(dir, "3", "42", "7.json"))
	require.NoError(t, err)
}

func TestFileStorage_RejectsPathTraversalEscrowID(t *testing.T) {
	dir := t.TempDir()
	fs := NewFileStorage(dir)
	ctx := context.Background()
	outside := filepath.Join(dir, "..", "escaped.json")

	for _, escrowID := range []string{
		"../escaped",
		"..",
		"foo/bar",
		"foo\\bar",
		"",
		" ",
		".",
	} {
		err := fs.Store(ctx, escrowID, 1, 1, []byte("p"), []byte("r"))
		require.Error(t, err, "escrowId=%q", escrowID)
		_, _, err = fs.Retrieve(ctx, escrowID, 1, 1)
		require.Error(t, err, "escrowId=%q", escrowID)
	}

	_, err := os.Stat(outside)
	require.Error(t, err)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestSanitizeEscrowPathSegment(t *testing.T) {
	got, err := sanitizeEscrowPathSegment("12345")
	require.NoError(t, err)
	require.Equal(t, "12345", got)

	_, err = sanitizeEscrowPathSegment("../x")
	require.Error(t, err)
}
