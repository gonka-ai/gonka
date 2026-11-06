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

func TestModelGroupsAllowSharedPromptsAndPreserveResponseIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "samples.jsonl")
	body := `{"model":"a","temperature":0,"messages":[{"role":"user","content":"same"}],"response":{"role":"assistant","content":"answer-a"}}
{"model":"b","messages":[{"role":"user","content":"same"}],"response":{"role":"assistant","content":"answer-b"}}
{"model":"b","temperature":0.7,"messages":[{"role":"user","content":"unique"}],"response":{"role":"assistant","content":"unique-b"}}
`
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	dataset, err := LoadFile(path)
	require.NoError(t, err)
	first, second := NewSelector(dataset, 42), NewSelector(dataset, 42)
	seen := map[int]bool{}
	for i := uint64(1); i <= 100; i++ {
		for _, model := range []string{"a", "b"} {
			sample, index, ok := first.SelectModel(model, i)
			again, againIndex, againOK := second.SelectModel(model, i)
			require.True(t, ok)
			require.True(t, againOK)
			require.Equal(t, index, againIndex)
			require.Equal(t, sample, again)
			require.Equal(t, model, sample.Model)
			seen[index] = true
		}
	}
	require.Len(t, seen, 3)
	messages := []Message{{Role: "user", Content: "same"}}
	for _, model := range []string{"a", "b"} {
		sample, _, ok := dataset.Lookup(model, messages)
		require.True(t, ok)
		require.Equal(t, "answer-"+model, sample.Response.Content)
	}
	_, _, ok := dataset.Lookup("unknown", messages)
	require.False(t, ok, "an ambiguous prompt must never select another model's response")
	sample, _, ok := dataset.Lookup("model-alias", []Message{{Role: "user", Content: "unique"}})
	require.True(t, ok, "preserve the unique-prompt fallback for upstream aliases")
	require.Equal(t, "unique-b", sample.Response.Content)
	sample, ok = dataset.Sample(0)
	require.True(t, ok)
	require.NotNil(t, sample.Temperature)
	require.Zero(t, *sample.Temperature)
	*sample.Temperature = 2
	sample.Messages[0].Content = "mutated"
	again, _ := dataset.Sample(0)
	require.Zero(t, *again.Temperature)
	require.Equal(t, "same", again.Messages[0].Content)
	sample, _ = dataset.Sample(1)
	require.Nil(t, sample.Temperature, "an omitted temperature must stay omitted")
	_, _, ok = first.SelectModel("missing", 1)
	require.False(t, ok)
}
