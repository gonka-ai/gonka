package replay

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadFileAndDeterministicSelector(t *testing.T) {
	path := filepath.Join(t.TempDir(), "samples.jsonl")
	body := `{"model":"model-a","stream":false,"temperature":0.7,"max_tokens":64,"messages":[{"role":"user","content":"one"}],"response":{"role":"assistant","content":"answer one"}}
{"model":"model-b","stream":true,"temperature":0.7,"max_tokens":128,"messages":[{"role":"user","content":"two"}],"response":{"role":"assistant","content":"answer two"}}
`
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))

	dataset, err := LoadFile(path)
	require.NoError(t, err)
	require.Equal(t, 2, dataset.Len())
	require.Equal(t, []string{"model-a", "model-b"}, dataset.Models())

	first := NewSelector(dataset, 42)
	second := NewSelector(dataset, 42)
	for index := uint64(1); index < 20; index++ {
		firstSample, firstIndex, firstOK := first.Select(index)
		secondSample, secondIndex, secondOK := second.Select(index)
		require.True(t, firstOK)
		require.True(t, secondOK)
		require.Equal(t, firstIndex, secondIndex)
		require.Equal(t, firstSample, secondSample)
	}
}

func TestLoadFileRejectsDuplicateFingerprint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "samples.jsonl")
	body := `{"model":"model-a","messages":[{"role":"user","content":"same"}],"response":{"role":"assistant","content":"one"}}
{"model":"model-a","messages":[{"role":"user","content":"same"}],"response":{"role":"assistant","content":"two"}}
`
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))

	_, err := LoadFile(path)
	require.ErrorContains(t, err, "duplicate request fingerprint")
}
