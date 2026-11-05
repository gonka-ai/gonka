package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"devshard/testenv/replay"

	"github.com/stretchr/testify/require"
)

const recordingFixture = `{
 "case": "tool_one@chat",
 "request": {"model": "org/model", "messages": [{"role": "user", "content": "a < b & c"}],
  "stream": true, "max_tokens": 12, "max_completion_tokens": 12, "chat_template_kwargs": {"thinking": false}},
 "stream": [
  {"id": "chatcmpl-1", "object": "chat.completion.chunk", "created": 0, "model": "org/model",
   "choices": [{"index": 0, "delta": {"role": "assistant", "content": ""}, "logprobs": null, "finish_reason": null}],
   "prompt_token_ids": [1, 2]},
  {"id": "chatcmpl-1", "choices": [{"index": 0, "delta": {"content": "<é>"},
   "logprobs": {"content": [{"token": "40", "logprob": -0.10000000149011612, "bytes": [60]}]}, "finish_reason": "stop", "token_ids": [40]}]},
  {"id": "chatcmpl-1", "choices": [], "usage": {"prompt_tokens": 2, "total_tokens": 3, "completion_tokens": 1}}
 ],
 "response_nonstream": {"id": "chatcmpl-1", "choices": [{"message": {"content": "<é>"}}], "usage": {"prompt_tokens": 2, "completion_tokens": 1}},
 "served": {"reasoning": "", "content": "<é>", "tool_calls": []},
 "tokens": [{"id": 40}],
 "notes": {"logprob_values": "synthetic", "request": {"form": "forwarded", "client_request": {"model": "org/model", "max_tokens": 50}}},
 "host": {"positions": 1},
 "build": {"model": "org/model", "revision": "abc", "variant": "v1 (fork 1234567)", "vllm_version": "0.30.0",
  "vllm_overlay": {"sha": "1234567890"}, "tokenizer_files_sha256": {"tokenizer.json": "ff"}, "image_id": "sha256:00"}
}`

func compactJSON(t *testing.T, raw string) string {
	t.Helper()
	var out bytes.Buffer
	require.NoError(t, json.Compact(&out, []byte(raw)))
	return out.String()
}

// Test flow:
// 1. Convert a recording with a key order, number spelling and characters Go would escape.
// 2. Require the request and payloads compact but otherwise as recorded, and the rest dropped.
// 3. Require the JSONL line to load as a recorded replay sample.
func TestConvertKeepsRecordedBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tool_one@chat.json")
	require.NoError(t, os.WriteFile(path, []byte(recordingFixture), 0o644))
	var out bytes.Buffer
	require.NoError(t, convertFiles(&out, []string{path}, "unused"))
	line := out.String()
	require.Equal(t, 1, strings.Count(line, "\n"))
	for _, dropped := range []string{"tokenizer_files_sha256", "image_id", `"tokens"`, `"host"`, `"served"`, "vllm_overlay", "logprob_values", `"form"`} {
		require.NotContains(t, line, dropped)
	}

	var fixture struct {
		Request  json.RawMessage   `json:"request"`
		Stream   []json.RawMessage `json:"stream"`
		Response json.RawMessage   `json:"response_nonstream"`
	}
	require.NoError(t, json.Unmarshal([]byte(recordingFixture), &fixture))
	require.Contains(t, line, `"request":`+compactJSON(t, string(fixture.Request)))
	require.Contains(t, line, `"nonstream":`+compactJSON(t, string(fixture.Response)))
	for _, chunk := range fixture.Stream {
		require.Contains(t, line, compactJSON(t, string(chunk)))
	}
	require.Contains(t, line, `"logprob":-0.10000000149011612`)
	require.Contains(t, line, `"content":"<é>"`)
	require.Contains(t, line, `"client_request":{"model":"org/model","max_tokens":50}`)

	replayFile := filepath.Join(t.TempDir(), "recorded.jsonl")
	require.NoError(t, os.WriteFile(replayFile, out.Bytes(), 0o644))
	dataset, err := replay.LoadFile(replayFile)
	require.NoError(t, err)
	sample, _, ok := dataset.LookupRequest(fixture.Request)
	require.True(t, ok)
	require.Equal(t, "tool_one@chat", sample.Name)
	require.Equal(t, "org/model", sample.Model)
	require.True(t, sample.Stream)
	require.Equal(t, int64(12), sample.MaxTokens)
	require.Equal(t, []replay.Message{{Role: "user", Content: "a < b & c"}}, sample.Messages)
	require.Equal(t, replay.Response{Role: "assistant", Content: "<é>"}, sample.Response)
	require.Equal(t, &replay.Build{Model: "org/model", Revision: "abc", Variant: "v1 (fork 1234567)", Commit: "1234567890"}, sample.Build)
	require.Len(t, sample.Recorded.Stream, 3)
}

func TestConvertRejectsMessagesWithoutPlainForm(t *testing.T) {
	recording := strings.Replace(recordingFixture,
		`[{"role": "user", "content": "a < b & c"}]`,
		`[{"role": "user", "content": [{"type": "text", "text": "hi"}]}]`, 1)
	_, err := convert([]byte(recording))
	require.ErrorContains(t, err, "messages[0]")
}

// Test flow:
// 1. Convert a recording whose history has reasoning, a tool call with null content and a tool result.
// 2. Require the plain form to keep each role and string content, and the request to keep the rest.
// 3. Require the sample to load and to be found by its full request.
func TestConvertKeepsHistoryFieldsInRequest(t *testing.T) {
	history := `[{"role": "user", "content": "weather?"},
		{"role": "assistant", "content": null, "reasoning": "look it up",
		 "tool_calls": [{"id": "call_1", "type": "function", "function": {"name": "get_weather", "arguments": "{}"}}]},
		{"role": "tool", "content": "sunny", "tool_call_id": "call_1"}]`
	recording := strings.Replace(recordingFixture, `[{"role": "user", "content": "a < b & c"}]`, history, 1)
	sample, err := convert([]byte(recording))
	require.NoError(t, err)
	require.Equal(t, []replay.Message{{Role: "user", Content: "weather?"}, {Role: "assistant"}, {Role: "tool", Content: "sunny"}}, sample.Messages)
	for _, kept := range []string{`"reasoning":"look it up"`, `"tool_call_id":"call_1"`, `"content":null`} {
		require.Contains(t, compactJSON(t, string(sample.Request)), kept)
	}

	var out bytes.Buffer
	path := filepath.Join(t.TempDir(), "hist.json")
	require.NoError(t, os.WriteFile(path, []byte(recording), 0o644))
	require.NoError(t, convertFiles(&out, []string{path}, ""))
	replayFile := filepath.Join(t.TempDir(), "recorded.jsonl")
	require.NoError(t, os.WriteFile(replayFile, out.Bytes(), 0o644))
	dataset, err := replay.LoadFile(replayFile)
	require.NoError(t, err)
	_, _, ok := dataset.LookupRequest(sample.Request)
	require.True(t, ok)
}

func TestConvertNamesVariantOnlyWhenMissing(t *testing.T) {
	dir := t.TempDir()
	named := filepath.Join(dir, "named.json")
	unnamed := filepath.Join(dir, "unnamed.json")
	require.NoError(t, os.WriteFile(named, []byte(recordingFixture), 0o644))
	require.NoError(t, os.WriteFile(unnamed, []byte(strings.Replace(recordingFixture, `"variant": "v1 (fork 1234567)", `, "", 1)), 0o644))
	var out bytes.Buffer
	require.NoError(t, convertFiles(&out, []string{named, unnamed}, "v2 (fork 7654321)"))
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	require.Len(t, lines, 2)
	require.Contains(t, lines[0], `"variant":"v1 (fork 1234567)"`)
	require.Contains(t, lines[1], `"variant":"v2 (fork 7654321)"`)
}

// Test flow:
// 1. Write one recording to a .jsonl and a .jsonl.gz output.
// 2. Require the .gz file to be gzip that decompresses to the plain file byte for byte.
func TestWriteFileCompressesGzName(t *testing.T) {
	dir := t.TempDir()
	recording := filepath.Join(dir, "tool_one@chat.json")
	require.NoError(t, os.WriteFile(recording, []byte(recordingFixture), 0o644))
	plain := filepath.Join(dir, "out.jsonl")
	compressed := filepath.Join(dir, "out.jsonl.gz")
	require.NoError(t, writeFile(plain, []string{recording}, ""))
	require.NoError(t, writeFile(compressed, []string{recording}, ""))

	want, err := os.ReadFile(plain)
	require.NoError(t, err)
	file, err := os.Open(compressed)
	require.NoError(t, err)
	defer file.Close()
	reader, err := gzip.NewReader(file)
	require.NoError(t, err)
	got, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Equal(t, string(want), string(got))
}

func TestVLLMCommitFromDevVersion(t *testing.T) {
	require.Equal(t, "71ee49c4d", vllmCommit("0.1.dev1+g71ee49c4d", nil))
	require.Equal(t, "", vllmCommit("0.30.0", nil))
	require.Equal(t, "be6059424", vllmCommit("0.25.1", &vllmOverlay{SHA: "be6059424"}))
}

// Test flow:
// 1. Convert a recording whose host section holds the build's render counts, and one without them.
// 2. Require the counts and the served text bytes in the sample, and no counts for the other.
func TestConvertKeepsRenderCounts(t *testing.T) {
	recording := strings.Replace(recordingFixture, `"host": {"positions": 1}`,
		`"host": {"positions": 1, "rendered_input": 24, "rendered_output": 7}, "gateway_bytes": {"total": 3}`, 1)
	sample, err := convert([]byte(recording))
	require.NoError(t, err)
	require.Equal(t, &replay.RenderCounts{Input: 24, Output: 7, TurnBytes: 3}, sample.Render)

	sample, err = convert([]byte(recordingFixture))
	require.NoError(t, err)
	require.Nil(t, sample.Render)
}
