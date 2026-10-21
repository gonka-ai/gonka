package replay

import (
	"os"
	"path/filepath"
	"strings"
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

const recordedRequest = `{"model":"model-a","messages":[{"role":"user","content":"same"}],"max_tokens":32,"max_completion_tokens":32,"stream":true,"stream_options":{"include_usage":true},"logprobs":true,"top_logprobs":5,"seed":1234,"return_token_ids":true,"skip_special_tokens":false,"tools":[{"type":"function","function":{"name":"f"}}],"logit_bias":{"7":100},"stop":["END"],"chat_template_kwargs":{"thinking":true}}`

func recordedLine(request, content string) string {
	return `{"model":"model-a","messages":[{"role":"user","content":"same"}],"response":{"role":"assistant","content":"` + content + `"},` +
		`"request":` + request + `,"recorded":{"stream":[{"id":"` + content + `"}],"nonstream":{"id":"` + content + `"}}}`
}

// Test flow:
// 1. Rewrite a recorded request as the gateway, the executor and the validator replay do.
// 2. Require the same fingerprint for every rewrite.
// 3. Require a different fingerprint once a field that decides the answer changes.
func TestRequestFingerprintIgnoresForcedFieldsOnly(t *testing.T) {
	base, err := RequestFingerprint([]byte(recordedRequest))
	require.NoError(t, err)

	same := []string{
		// Gateway and executor: another key order, own seed, n clamped, tool_choice defaulted.
		`{"tool_choice":"auto","n":1,"seed":77,"top_logprobs":5,"logprobs":true,"stream":true,"stream_options":{"include_usage":true},"return_token_ids":true,"skip_special_tokens":false,"max_completion_tokens":32,"max_tokens":32,"model":"model-a","messages":[{"content":"same","role":"user"}],"tools":[{"function":{"name":"f"},"type":"function"}],"logit_bias":{"7":100.0},"stop":"END","chat_template_kwargs":{"thinking":true}}`,
		// Validator replay: non-stream, enforced tokens, logprobs mode, its own seed, max_completion_tokens only.
		`{"model":"model-a","messages":[{"role":"user","content":"same"}],"max_completion_tokens":32,"stream":false,"logprobs":true,"top_logprobs":5,"seed":41,"enforced_tokens":{"tokens":[{"token":"7"}]},"logprobs_mode":"processed_logprobs","tools":[{"type":"function","function":{"name":"f"}}],"logit_bias":{"7":100},"stop":["END"],"chat_template_kwargs":{"thinking":true},"min_tokens":0,"temperature":0.7}`,
	}
	for _, body := range same {
		fingerprint, err := RequestFingerprint([]byte(body))
		require.NoError(t, err)
		require.Equal(t, base, fingerprint, body)
	}

	changed := map[string]string{
		"max_tokens":           `"max_tokens":32,"max_completion_tokens":32`,
		"stop":                 `"stop":["END"]`,
		"logit_bias":           `"logit_bias":{"7":100}`,
		"chat_template_kwargs": `"chat_template_kwargs":{"thinking":true}`,
		"tools":                `"tools":[{"type":"function","function":{"name":"f"}}]`,
		"messages":             `"messages":[{"role":"user","content":"same"}]`,
	}
	replacements := map[string]string{
		"max_tokens":           `"max_tokens":33,"max_completion_tokens":33`,
		"stop":                 `"stop":["STOP"]`,
		"logit_bias":           `"logit_bias":{"8":100}`,
		"chat_template_kwargs": `"chat_template_kwargs":{"thinking":false}`,
		"tools":                `"tools":[{"type":"function","function":{"name":"g"}}]`,
		"messages":             `"messages":[{"role":"user","content":"other"}]`,
	}
	for field, original := range changed {
		body := strings.Replace(recordedRequest, original, replacements[field], 1)
		require.NotEqual(t, recordedRequest, body, field)
		fingerprint, err := RequestFingerprint([]byte(body))
		require.NoError(t, err)
		require.NotEqual(t, base, fingerprint, field)
	}
	withoutKwargs := strings.Replace(recordedRequest, `,"chat_template_kwargs":{"thinking":true}`, "", 1)
	fingerprint, err := RequestFingerprint([]byte(withoutKwargs))
	require.NoError(t, err)
	require.NotEqual(t, base, fingerprint, "an absent chat_template_kwargs is its own recording")
}

func TestLoadFileKeysRecordedSamplesByRequest(t *testing.T) {
	other := strings.Replace(recordedRequest, `"max_tokens":32,"max_completion_tokens":32`, `"max_tokens":64,"max_completion_tokens":64`, 1)
	path := filepath.Join(t.TempDir(), "samples.jsonl")
	body := `{"model":"model-a","messages":[{"role":"user","content":"same"}],"response":{"role":"assistant","content":"text only"}}` + "\n" +
		recordedLine(recordedRequest, "first") + "\n" +
		recordedLine(other, "second") + "\n"
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))

	dataset, err := LoadFile(path)
	require.NoError(t, err)
	require.Equal(t, 3, dataset.Len())
	require.Equal(t, []string{"model-a"}, dataset.Models())

	sample, index, ok := dataset.LookupRequest([]byte(recordedRequest))
	require.True(t, ok)
	require.Equal(t, 1, index)
	require.Equal(t, `{"id":"first"}`, string(sample.Recorded.NonStream))
	sample, index, ok = dataset.LookupRequest([]byte(other))
	require.True(t, ok)
	require.Equal(t, 2, index)
	require.Equal(t, `{"id":"second"}`, string(sample.Recorded.Stream[0]))
	_, _, ok = dataset.LookupRequest([]byte(strings.Replace(recordedRequest, `"stop":["END"]`, `"stop":["X"]`, 1)))
	require.False(t, ok)

	sample, index, ok = dataset.Lookup("model-a", []Message{{Role: "user", Content: "same"}})
	require.True(t, ok)
	require.Equal(t, 0, index)
	require.Nil(t, sample.Recorded)
	require.Equal(t, "text only", sample.Response.Content)
}

func TestLoadFileRejectsDuplicateRecordedRequest(t *testing.T) {
	reseeded := strings.Replace(recordedRequest, `"seed":1234`, `"seed":5`, 1)
	path := filepath.Join(t.TempDir(), "samples.jsonl")
	body := recordedLine(recordedRequest, "one") + "\n" + recordedLine(reseeded, "two") + "\n"
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))

	_, err := LoadFile(path)
	require.ErrorContains(t, err, "duplicate recorded request fingerprint")
}

func TestLoadFileRejectsIncompleteRecording(t *testing.T) {
	cases := map[string]string{
		"recorded sample needs its request": `{"model":"model-a","messages":[{"role":"user","content":"x"}],"response":{"role":"assistant"},"recorded":{"stream":[{}],"nonstream":{}}}`,
		"differs from sample model":         `{"model":"model-b","messages":[{"role":"user","content":"x"}],"response":{"role":"assistant"},"request":{"model":"model-a"},"recorded":{"stream":[{}],"nonstream":{}}}`,
		"recorded.stream is required":       `{"model":"model-a","messages":[{"role":"user","content":"x"}],"response":{"role":"assistant"},"request":{"model":"model-a"},"recorded":{"nonstream":{}}}`,
		"recorded.nonstream must be":        `{"model":"model-a","messages":[{"role":"user","content":"x"}],"response":{"role":"assistant"},"request":{"model":"model-a"},"recorded":{"stream":[{}]}}`,
	}
	for want, line := range cases {
		path := filepath.Join(t.TempDir(), "samples.jsonl")
		require.NoError(t, os.WriteFile(path, []byte(line+"\n"), 0o644))
		_, err := LoadFile(path)
		require.ErrorContains(t, err, want)
	}
}
