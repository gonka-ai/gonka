package artifacts

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestModelDirectoryDotSegmentsAreIsolated(t *testing.T) {
	store := NewManagedArtifactStore(t.TempDir(), 3)
	defer store.Close()
	for _, modelID := range []string{".", "..", "%2E", "%2E%2E", "org/model"} {
		t.Run(modelID, func(t *testing.T) {
			require.Equal(t, store.stageDir(100), filepath.Dir(store.modelDir(100, modelID)))
			require.NotEqual(t, store.modelDir(100, modelID), store.modelDir(200, modelID))
			decoded, err := decodeModelID(encodeModelID(modelID))
			require.NoError(t, err)
			require.Equal(t, modelID, decoded)
		})
	}
	require.NotEqual(t, store.modelDir(100, "."), store.modelDir(100, "%2E"))
	require.NotEqual(t, store.modelDir(100, ".."), store.modelDir(100, "%2E%2E"))
}
