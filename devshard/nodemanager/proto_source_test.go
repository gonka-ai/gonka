package nodemanager

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProtoSource_SingleCanonicalLocation(t *testing.T) {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	root := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))

	_, err := os.Stat(filepath.Join(root, "devshard", "nodemanager", "nodemanager.proto"))
	require.NoError(t, err, "canonical proto must exist")

	_, err = os.Stat(filepath.Join(root, "devshard", "nodemanager", "gen", "nodemanager.pb.go"))
	require.NoError(t, err, "canonical gen must exist")

	_, err = os.Stat(filepath.Join(root, "decentralized-api", "nodemanager", "nodemanager.proto"))
	require.True(t, os.IsNotExist(err), "duplicate dapi proto must be removed")

	_, err = os.Stat(filepath.Join(root, "decentralized-api", "nodemanager", "gen"))
	require.True(t, os.IsNotExist(err), "duplicate dapi gen must be removed")

	_, err = os.Stat(filepath.Join(root, "devshard", "mlnode", "gen"))
	require.True(t, os.IsNotExist(err), "duplicate mlnode gen must be removed")
}
