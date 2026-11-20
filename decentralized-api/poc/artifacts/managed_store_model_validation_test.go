package artifacts

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestManagedArtifactStoreRejectsDotSegments(t *testing.T) {
	for _, operation := range []string{"create", "get", "serve"} {
		for _, modelID := range []string{".", ".."} {
			t.Run(operation+"/"+modelID, func(t *testing.T) {
				dir := t.TempDir()
				m := NewManagedArtifactStore(dir, 3)
				defer m.Close()
				m.ActivateStage(100)
				require.NoError(t, os.MkdirAll(filepath.Join(dir, "100"), 0755))
				sentinel := filepath.Join(dir, "sentinel")
				require.NoError(t, os.WriteFile(sentinel, []byte("unchanged"), 0644))
				var store ArtifactStore
				var err error
				switch operation {
				case "create":
					store, err = m.GetOrCreateStore(100, modelID)
				case "get":
					store, err = m.GetStore(100, modelID)
				case "serve":
					store, err = m.GetStoreForServing(100, modelID)
				}
				require.Error(t, err)
				require.Nil(t, store)
				require.Empty(t, m.stores)
				require.Empty(t, m.serving)
				entries, err := os.ReadDir(filepath.Join(dir, "100"))
				require.NoError(t, err)
				require.Empty(t, entries)
				entries, err = os.ReadDir(dir)
				require.NoError(t, err)
				require.Len(t, entries, 2)
				contents, err := os.ReadFile(sentinel)
				require.NoError(t, err)
				require.Equal(t, "unchanged", string(contents))
			})
		}
	}
}

func TestManagedArtifactStoreRejectsFakeHeightDotSegments(t *testing.T) {
	dir := t.TempDir()
	m := NewManagedArtifactStore(dir, 3)
	defer m.Close()
	for height := int64(1000); height < 1100; height++ {
		for _, modelID := range []string{".", ".."} {
			store, err := m.GetStoreForServing(height, modelID)
			require.Error(t, err)
			require.Nil(t, store)
		}
	}
	m.cleanup()
	require.Empty(t, m.serving)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestManagedArtifactStoreModelEncodingCompatibility(t *testing.T) {
	dir := t.TempDir()
	models := map[string]string{
		"org/model.v1": "org%2Fmodel.v1",
		"...":          "...",
		"%2E%2E":       "%252E%252E",
		"../model":     "..%2Fmodel",
		"org/../model": "org%2F..%2Fmodel",
		"model":        "model",
	}
	m := NewManagedArtifactStore(dir, 3)
	defer m.Close()
	m.ActivateStage(100)
	roots := make(map[string][]byte)
	for modelID, encoded := range models {
		require.Equal(t, encoded, encodeModelID(modelID))
		store, err := m.GetOrCreateStore(100, modelID)
		require.NoError(t, err)
		require.NoError(t, store.AddWithNode(1, []byte(fmt.Sprintf("artifact:%s", modelID)), "node-a"))
		require.NoError(t, store.Flush())
		_, roots[modelID] = store.GetFlushedRoot()
		info, err := os.Stat(filepath.Join(dir, "100", encoded))
		require.NoError(t, err)
		require.True(t, info.IsDir())
	}
	require.NoError(t, m.Close())
	reopened := NewManagedArtifactStore(dir, 3)
	defer reopened.Close()
	reopened.ActivateStage(200)
	stores, err := reopened.GetStoresForServing(100)
	require.NoError(t, err)
	require.Len(t, stores, len(models))
	for _, entry := range stores {
		expected, ok := roots[entry.ModelID]
		require.True(t, ok)
		count, root := entry.Store.GetFlushedRoot()
		require.EqualValues(t, 1, count)
		require.Equal(t, expected, root)
	}
	require.EqualValues(t, 200, reopened.ActiveStage())
	require.NoError(t, reopened.PruneStore(100))
	require.Empty(t, reopened.serving)
}
