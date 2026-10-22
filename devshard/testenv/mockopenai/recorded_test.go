package mockopenai_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"common/completionapi"
	"common/validation"
	"devshard/testenv/mockopenai"
	"devshard/testenv/replay"

	"github.com/stretchr/testify/require"
)

// The testdata/vllm_recorded_*.jsonl files hold answers recorded from vLLM serving code on
// CPU: the token sequence of a known assistant turn fed through vLLM's output processor and its
// chat stream and non-stream generators, with the model's chat template, reasoning and tool
// parsers. Logprob values and alternative ids are synthetic; token ids, bytes, chunk boundaries,
// deltas, finish reasons and usage are vLLM's. One file per build, since builds answer the same
// request differently, and each sample names its variant and vLLM commit in "build". The set
// here is mlnode310 (vLLM fork 71ee49c4d): GLM-5.3-Flash, and two DeepSeek-V4-Flash recordings
// of one prompt under different request fields.
const primaryRecordedFile = "testdata/vllm_recorded_mlnode310.jsonl"

func recordedFiles(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("testdata/vllm_recorded_*.jsonl")
	require.NoError(t, err)
	require.Contains(t, files, primaryRecordedFile)
	return files
}

func loadRecordedSamples(t *testing.T, file string) []replay.Sample {
	t.Helper()
	dataset, err := replay.LoadFile(file)
	require.NoError(t, err)
	samples := make([]replay.Sample, dataset.Len())
	for i := range samples {
		sample, ok := dataset.Sample(i)
		require.True(t, ok)
		require.NotNil(t, sample.Recorded, "%s: sample %d has no recording", file, i)
		samples[i] = sample
	}
	require.NotEmpty(t, samples)
	return samples
}

func newRecordedServer(t *testing.T, file string, faults mockopenai.FaultConfig) *httptest.Server {
	t.Helper()
	srv, err := mockopenai.NewServerWithError(mockopenai.Config{ReplayFile: file, Faults: faults})
	require.NoError(t, err)
	httpServer := httptest.NewServer(srv.Handler())
	t.Cleanup(httpServer.Close)
	return httpServer
}

// forEachRecording runs check on every sample of every recorded build, each build on its own mock.
func forEachRecording(t *testing.T, check func(t *testing.T, srv *httptest.Server, sample replay.Sample)) {
	t.Helper()
	for _, file := range recordedFiles(t) {
		srv := newRecordedServer(t, file, mockopenai.FaultConfig{})
		build := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(file), "vllm_recorded_"), ".jsonl")
		for _, sample := range loadRecordedSamples(t, file) {
			t.Run(build+"/"+sample.Model+"/"+sample.Name, func(t *testing.T) { check(t, srv, sample) })
		}
	}
}

func postChat(t *testing.T, url string, body []byte) (*http.Response, []byte) {
	t.Helper()
	resp, err := http.Post(url+"/v1/chat/completions", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	return resp, raw
}

func sseBody(chunks []json.RawMessage, done bool) []byte {
	var out bytes.Buffer
	for _, chunk := range chunks {
		out.WriteString("data: ")
		out.Write(chunk)
		out.WriteString("\n\n")
	}
	if done {
		out.WriteString("data: [DONE]\n\n")
	}
	return out.Bytes()
}

func sseLines(raw []byte) []string {
	var lines []string
	for _, line := range strings.Split(string(raw), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func rewriteRequest(t *testing.T, body []byte, edit func(map[string]any)) []byte {
	t.Helper()
	var request map[string]any
	require.NoError(t, json.Unmarshal(body, &request))
	edit(request)
	out, err := json.Marshal(request)
	require.NoError(t, err)
	return out
}

// nonStreamRequest is the recorded request as a non-streaming call.
func nonStreamRequest(t *testing.T, body []byte) []byte {
	return rewriteRequest(t, body, func(request map[string]any) {
		request["stream"] = false
		delete(request, "stream_options")
	})
}

// gatewayForwardedRequest rebuilds the recorded request the way the gateway forwards it for a
// client that sent only what decides the answer: streaming with usage forced, max_tokens mirrored
// into max_completion_tokens, tool_choice defaulted and a client n clamped to 1.
func gatewayForwardedRequest(t *testing.T, body []byte) []byte {
	t.Helper()
	client := rewriteRequest(t, body, func(request map[string]any) {
		for _, forced := range []string{"stream", "stream_options", "logprobs", "top_logprobs", "skip_special_tokens", "return_token_ids", "seed", "max_completion_tokens"} {
			delete(request, forced)
		}
	})
	forwarded := rewriteRequest(t, client, func(request map[string]any) {
		request["stream"] = true
		request["stream_options"] = map[string]any{"include_usage": true}
		request["max_completion_tokens"] = request["max_tokens"]
		request["n"] = 1
		if _, ok := request["tools"]; ok {
			request["tool_choice"] = "auto"
		}
	})
	return forwarded
}

// executorRequest is what the executor sends the ML node for a forwarded request: logprobs,
// top_logprobs, skip_special_tokens and return_token_ids pinned, and its own seed.
func executorRequest(t *testing.T, forwarded []byte) []byte {
	t.Helper()
	modified, err := completionapi.ModifyRequestBody(forwarded, 77)
	require.NoError(t, err)
	return modified.NewBody
}

func recordedUsage(t *testing.T, chunks []json.RawMessage) completionapi.Usage {
	t.Helper()
	for i := len(chunks) - 1; i >= 0; i-- {
		var chunk struct {
			Usage *completionapi.Usage `json:"usage"`
		}
		require.NoError(t, json.Unmarshal(chunks[i], &chunk))
		if chunk.Usage != nil {
			return *chunk.Usage
		}
	}
	t.Fatal("recorded stream carries no usage")
	return completionapi.Usage{}
}

func recordedPositions(t *testing.T, chunks []json.RawMessage) int {
	t.Helper()
	positions := 0
	for _, raw := range chunks {
		var chunk struct {
			Choices []struct {
				Logprobs *struct {
					Content []json.RawMessage `json:"content"`
				} `json:"logprobs"`
			} `json:"choices"`
		}
		require.NoError(t, json.Unmarshal(raw, &chunk))
		for _, choice := range chunk.Choices {
			if choice.Logprobs != nil {
				positions += len(choice.Logprobs.Content)
			}
		}
	}
	return positions
}

// executeStreamed runs body through the executor's response processor, as devshardd does, and
// returns the stored response payload and the completion parsed from it.
func executeStreamed(t *testing.T, url string, body []byte) ([]byte, completionapi.CompletionResponse) {
	t.Helper()
	resp, err := http.Post(url+"/v1/chat/completions", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	processor := completionapi.NewExecutorResponseProcessor("inference-recorded", true)
	require.NoError(t, completionapi.ProcessHTTPResponse(resp, processor))
	stored, err := processor.GetResponseBytes()
	require.NoError(t, err)
	var serialized completionapi.SerializedStreamedResponse
	require.NoError(t, json.Unmarshal(stored, &serialized))
	completion, err := completionapi.NewCompletionResponseFromLines(serialized.Events)
	require.NoError(t, err)
	return stored, completion
}

func TestRecordedReplay_ServesRecordedBytes(t *testing.T) {
	forEachRecording(t, func(t *testing.T, srv *httptest.Server, sample replay.Sample) {
		resp, raw := postChat(t, srv.URL, sample.Request)
		require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))
		require.Contains(t, resp.Header.Get("Content-Type"), "text/event-stream")
		require.Equal(t, string(sseBody(sample.Recorded.Stream, true)), string(raw))

		resp, raw = postChat(t, srv.URL, nonStreamRequest(t, sample.Request))
		require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))
		require.Contains(t, resp.Header.Get("Content-Type"), "application/json")
		require.Equal(t, string(sample.Recorded.NonStream), string(raw))

		require.NotNil(t, sample.Build)
		require.Equal(t, sample.Model, sample.Build.Model)
		require.NotEmpty(t, sample.Build.Revision)
		require.NotEmpty(t, sample.Build.Variant)
		require.NotEmpty(t, sample.Build.Commit)
	})
}

// Test flow:
// 1. Stream each recorded request through the executor's response processor.
// 2. Require its usage to be the recorded usage and the non-stream body to report the same.
// 3. Require one logprobs position per recorded position, equal between stream and non-stream.
func TestRecordedReplay_UsageAndLogprobPositions(t *testing.T) {
	forEachRecording(t, func(t *testing.T, srv *httptest.Server, sample replay.Sample) {
		want := recordedUsage(t, sample.Recorded.Stream)
		_, streamed := executeStreamed(t, srv.URL, sample.Request)
		usage, err := streamed.GetUsage()
		require.NoError(t, err)
		require.Equal(t, want, *usage)

		_, raw := postChat(t, srv.URL, nonStreamRequest(t, sample.Request))
		whole, err := completionapi.NewCompletionResponseFromBytes(raw)
		require.NoError(t, err)
		usage, err = whole.GetUsage()
		require.NoError(t, err)
		require.Equal(t, want, *usage)

		positions := recordedPositions(t, sample.Recorded.Stream)
		require.Positive(t, positions)
		require.Len(t, streamed.ExtractLogits(), positions)
		require.Equal(t, streamed.ExtractLogits(), whole.ExtractLogits())
	})
}

func TestRecordedReplay_MatchesGatewayForwardedRequest(t *testing.T) {
	forEachRecording(t, func(t *testing.T, srv *httptest.Server, sample replay.Sample) {
		resp, raw := postChat(t, srv.URL, executorRequest(t, gatewayForwardedRequest(t, sample.Request)))
		require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))
		require.Equal(t, string(sseBody(sample.Recorded.Stream, true)), string(raw))
	})
}

// Test flow:
// 1. Execute the gateway-forwarded request as the executor does and store the response.
// 2. Validate it with the validator's own replay: enforced tokens, its own seed, streamed.
// 3. Require the replay to reach the same recording and return the logprobs the executor streamed.
func TestRecordedReplay_HonestValidatorReplayGetsRecordedLogprobs(t *testing.T) {
	forEachRecording(t, func(t *testing.T, srv *httptest.Server, sample replay.Sample) {
		prompt := gatewayForwardedRequest(t, sample.Request)
		stored, streamed := executeStreamed(t, srv.URL, executorRequest(t, prompt))
		usage, err := streamed.GetUsage()
		require.NoError(t, err)

		var replayRequest, replayResponse []byte
		result, err := validation.ExecuteValidation(
			context.Background(),
			"41",
			prompt,
			stored,
			func(_ context.Context, body []byte) (*http.Response, error) {
				replayRequest = body
				resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
				if err != nil {
					return nil, err
				}
				replayResponse, err = io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				resp.Body = io.NopCloser(bytes.NewReader(replayResponse))
				return resp, err
			},
			usage.PromptTokens,
			usage.CompletionTokens,
			"",
			0,
		)
		require.NoError(t, err)
		require.True(t, result.IsSuccessful(), "honest replay of a recording must validate: %#v", result)

		var sent map[string]any
		require.NoError(t, json.Unmarshal(replayRequest, &sent))
		require.Equal(t, true, sent["stream"])
		require.Contains(t, sent, "enforced_tokens")
		require.Equal(t, string(sseBody(sample.Recorded.Stream, true)), string(replayResponse))
		replayed, err := completionapi.NewCompletionResponseFromLines(sseLines(replayResponse))
		require.NoError(t, err)
		require.Equal(t, streamed.ExtractLogits(), replayed.ExtractLogits())
	})
}

// Recordings of one prompt differ only in fields that change the answer; each must stay apart
// and a change to any of them must miss rather than reach a neighbour.
func TestRecordedReplay_KeySeparatesRequestsOfOnePrompt(t *testing.T) {
	samples := loadRecordedSamples(t, primaryRecordedFile)
	byPrompt := map[string][]replay.Sample{}
	for _, sample := range samples {
		prompt := replay.Fingerprint(sample.Model, sample.Messages)
		byPrompt[prompt] = append(byPrompt[prompt], sample)
	}
	shared := 0
	for _, group := range byPrompt {
		if len(group) > 1 {
			shared++
		}
	}
	require.Positive(t, shared, "testdata must hold recordings that share a prompt")

	srv := newRecordedServer(t, primaryRecordedFile, mockopenai.FaultConfig{})
	for _, sample := range samples {
		_, raw := postChat(t, srv.URL, sample.Request)
		require.Equal(t, string(sseBody(sample.Recorded.Stream, true)), string(raw), sample.Name)
	}
	edits := map[string]func(map[string]any){
		"max_tokens": func(request map[string]any) {
			request["max_tokens"] = request["max_tokens"].(float64) + 1
			request["max_completion_tokens"] = request["max_tokens"]
		},
		"stop":                 func(request map[string]any) { request["stop"] = []any{"never-recorded"} },
		"chat_template_kwargs": func(request map[string]any) { request["chat_template_kwargs"] = map[string]any{"never": true} },
		"logit_bias":           func(request map[string]any) { request["logit_bias"] = map[string]any{"7": 1} },
		"tools": func(request map[string]any) {
			request["tools"] = []any{map[string]any{"type": "function", "function": map[string]any{"name": "never_recorded"}}}
		},
	}
	forEachRecording(t, func(t *testing.T, srv *httptest.Server, sample replay.Sample) {
		for field, edit := range edits {
			resp, raw := postChat(t, srv.URL, rewriteRequest(t, sample.Request, edit))
			require.Equal(t, http.StatusNotFound, resp.StatusCode, "changed %s: %s", field, raw)
		}
	})
}

func firstRecordedWithTokens(t *testing.T, minTokenChunks int) replay.Sample {
	t.Helper()
	for _, sample := range loadRecordedSamples(t, primaryRecordedFile) {
		if recordedPositions(t, sample.Recorded.Stream) >= minTokenChunks {
			return sample
		}
	}
	t.Fatalf("no recording with %d positions", minTokenChunks)
	return replay.Sample{}
}

func chunkHasToken(t *testing.T, raw json.RawMessage) bool {
	t.Helper()
	var chunk struct {
		Choices []struct {
			TokenIDs []int64 `json:"token_ids"`
		} `json:"choices"`
	}
	require.NoError(t, json.Unmarshal(raw, &chunk))
	for _, choice := range chunk.Choices {
		if len(choice.TokenIDs) > 0 {
			return true
		}
	}
	return false
}

// chunkPromptFields returns the top-level prompt fields the chunk carries; vLLM sets them on the
// first chunk of a stream only.
func chunkPromptFields(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &fields))
	var present []string
	for _, name := range []string{"prompt_text", "prompt_token_ids", "prompt_logprobs"} {
		if _, ok := fields[name]; ok {
			present = append(present, name)
		}
	}
	return present
}

func chunkFinishes(t *testing.T, raw json.RawMessage) bool {
	t.Helper()
	var chunk struct {
		Choices []struct {
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
		Usage json.RawMessage `json:"usage"`
	}
	require.NoError(t, json.Unmarshal(raw, &chunk))
	for _, choice := range chunk.Choices {
		if choice.FinishReason != nil {
			return true
		}
	}
	return len(chunk.Usage) > 0
}

func mockStats(t *testing.T, url string) mockopenai.Stats {
	t.Helper()
	resp, err := http.Get(url + "/testenv/stats")
	require.NoError(t, err)
	defer resp.Body.Close()
	var stats mockopenai.Stats
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&stats))
	return stats
}

func TestRecordedReplay_DropFirstChunkDropsFirstToken(t *testing.T) {
	sample := firstRecordedWithTokens(t, 4)
	srv := newRecordedServer(t, primaryRecordedFile, mockopenai.FaultConfig{DropFirstChunk: true})
	resp, raw := postChat(t, srv.URL, sample.Request)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	chunks := sample.Recorded.Stream
	first := 0
	for !chunkHasToken(t, chunks[first]) {
		first++
	}
	want := append(append([]json.RawMessage(nil), chunks[:first]...), chunks[first+1:]...)
	require.Equal(t, string(sseBody(want, true)), string(raw))
	require.Equal(t, uint64(1), mockStats(t, srv.URL).FailedResponses)
}

func TestRecordedReplay_PartialStreamStopsBeforeFinish(t *testing.T) {
	sample := firstRecordedWithTokens(t, 4)
	srv := newRecordedServer(t, primaryRecordedFile, mockopenai.FaultConfig{PartialStream: true})
	resp, raw := postChat(t, srv.URL, sample.Request)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body := string(raw)
	require.NotContains(t, body, "data: [DONE]")
	require.True(t, strings.HasPrefix(string(sseBody(sample.Recorded.Stream, true)), body), "partial stream must be a prefix of the recording")
	lines := sseLines(raw)
	chunks := sample.Recorded.Stream
	tokens := 0
	for _, chunk := range chunks {
		if chunkFinishes(t, chunk) {
			break
		}
		if chunkHasToken(t, chunk) {
			tokens++
		}
	}
	written := 0
	for _, chunk := range chunks[:len(lines)] {
		require.False(t, chunkFinishes(t, chunk), "partial stream must not finish the choice")
		if chunkHasToken(t, chunk) {
			written++
		}
	}
	require.Equal(t, tokens/2+1, written)
	require.Equal(t, uint64(1), mockStats(t, srv.URL).FailedResponses)
}

func TestRecordedReplay_StreamErrorEnvelopeMatchesGeneratedStream(t *testing.T) {
	sample := firstRecordedWithTokens(t, 1)
	faults := mockopenai.FaultConfig{StreamErrorEnvelope: true}
	recordedSrv := newRecordedServer(t, primaryRecordedFile, faults)
	generatedSrv := httptest.NewServer(mockopenai.NewServer(mockopenai.Config{Faults: faults}).Handler())
	defer generatedSrv.Close()

	resp, recorded := postChat(t, recordedSrv.URL, sample.Request)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Content-Type"), "text/event-stream")
	_, generated := postChat(t, generatedSrv.URL, sample.Request)
	require.Equal(t, string(generated), string(recorded))
	require.Contains(t, string(recorded), `"error"`)
	require.True(t, strings.HasSuffix(string(recorded), "data: [DONE]\n\n"))
}

func TestRecordedReplay_SSEErrorKeepsFourTokenChunks(t *testing.T) {
	sample := firstRecordedWithTokens(t, 6)
	srv := newRecordedServer(t, primaryRecordedFile, mockopenai.FaultConfig{SSEErrorMessage: "TimeoutError"})
	resp, raw := postChat(t, srv.URL, sample.Request)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	lines := sseLines(raw)
	require.Equal(t, "data: [DONE]", lines[len(lines)-1])
	require.Contains(t, lines[len(lines)-2], "TimeoutError")
	prefix := lines[:len(lines)-2]
	tokens := 0
	for i, line := range prefix {
		require.Equal(t, "data: "+string(sample.Recorded.Stream[i]), line)
		if chunkHasToken(t, sample.Recorded.Stream[i]) {
			tokens++
		}
	}
	require.Equal(t, 4, tokens)
}

func TestRecordedReplay_PauseHoldsAfterFirstTokenChunk(t *testing.T) {
	sample := firstRecordedWithTokens(t, 2)
	srv := newRecordedServer(t, primaryRecordedFile, mockopenai.FaultConfig{PauseStream: true})
	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", bytes.NewReader(sample.Request))
	require.NoError(t, err)
	defer resp.Body.Close()

	chunks := sample.Recorded.Stream
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for i := 0; ; i++ {
		require.True(t, scanner.Scan(), "stream ended before its first token chunk")
		require.Equal(t, "data: "+string(chunks[i]), scanner.Text())
		require.True(t, scanner.Scan())
		require.Empty(t, scanner.Text())
		if chunkHasToken(t, chunks[i]) {
			break
		}
	}
	done := make(chan error, 1)
	go func() {
		for scanner.Scan() {
		}
		done <- scanner.Err()
	}()
	select {
	case err := <-done:
		t.Fatalf("paused stream completed before release: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	release, err := http.Post(srv.URL+"/testenv/stream/release", "application/json", nil)
	require.NoError(t, err)
	_ = release.Body.Close()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("stream did not complete after release")
	}
}

func recordedContent(t *testing.T, chunks []json.RawMessage) string {
	t.Helper()
	var content strings.Builder
	for _, raw := range chunks {
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		require.NoError(t, json.Unmarshal(raw, &chunk))
		for _, choice := range chunk.Choices {
			content.WriteString(choice.Delta.Content)
		}
	}
	return content.String()
}

func servedChunks(t *testing.T, raw []byte) []json.RawMessage {
	t.Helper()
	var chunks []json.RawMessage
	for _, line := range sseLines(raw) {
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			continue
		}
		chunks = append(chunks, json.RawMessage(payload))
	}
	return chunks
}

// Test flow:
// 1. Execute a recording with the usage knobs set, as an engine that overstates its counts.
// 2. Require the stored usage to carry the overstated counts and every other chunk to keep its bytes.
// 3. Replay it on the same mock as a validator does; the honest recording comes back and fails the claim.
func TestRecordedReplay_UsageKnobsOverstateExecutionOnly(t *testing.T) {
	sample := firstRecordedWithTokens(t, 4)
	srv := newRecordedServer(t, primaryRecordedFile, mockopenai.FaultConfig{UsageCompletionTokens: 4096, UsagePromptTokens: 700})
	honest := recordedUsage(t, sample.Recorded.Stream)

	_, raw := postChat(t, srv.URL, sample.Request)
	served := servedChunks(t, raw)
	require.Len(t, served, len(sample.Recorded.Stream))
	for i, chunk := range served {
		if bytes.Contains(sample.Recorded.Stream[i], []byte(`"usage"`)) {
			continue
		}
		require.Equal(t, string(sample.Recorded.Stream[i]), string(chunk), "chunk %d", i)
	}
	require.Equal(t, completionapi.Usage{PromptTokens: 700, CompletionTokens: 4096}, recordedUsage(t, served))
	var last struct {
		Usage struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	}
	require.NoError(t, json.Unmarshal(served[len(served)-1], &last))
	require.Equal(t, 4796, last.Usage.TotalTokens)

	prompt := gatewayForwardedRequest(t, sample.Request)
	stored, streamed := executeStreamed(t, srv.URL, executorRequest(t, prompt))
	usage, err := streamed.GetUsage()
	require.NoError(t, err)
	require.Equal(t, uint64(4096), usage.CompletionTokens)
	require.Equal(t, uint64(700), usage.PromptTokens)
	require.Len(t, streamed.ExtractLogits(), recordedPositions(t, sample.Recorded.Stream))

	var replayResponse []byte
	result, err := validation.ExecuteValidation(
		context.Background(),
		"41",
		prompt,
		stored,
		func(_ context.Context, body []byte) (*http.Response, error) {
			resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
			if err != nil {
				return nil, err
			}
			replayResponse, err = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			resp.Body = io.NopCloser(bytes.NewReader(replayResponse))
			return resp, err
		},
		usage.PromptTokens,
		usage.CompletionTokens,
		"",
		0,
	)
	require.NoError(t, err)
	require.Equal(t, string(sseBody(sample.Recorded.Stream, true)), string(replayResponse))
	require.False(t, result.IsSuccessful())
	invalid, ok := result.(*validation.InvalidInferenceResult)
	require.True(t, ok, "%#v", result)
	require.Equal(t, "Inflated token counts.", invalid.Reason)
	require.Equal(t, honest.CompletionTokens, recordedUsage(t, servedChunks(t, replayResponse)).CompletionTokens)
}

// Padding adds lines but no text, no positions, no usage and no prompt fields: the claim the
// executor signs and the content a client assembles stay the recording's, and the pads look like
// the chunks vLLM streams after the first, which alone reports the prompt.
func TestRecordedReplay_PadEmptyChunksAddsLinesOnly(t *testing.T) {
	sample := firstRecordedWithTokens(t, 4)
	require.NotEmpty(t, chunkPromptFields(t, sample.Recorded.Stream[0]), "the recording reports the prompt on its first chunk")
	srv := newRecordedServer(t, primaryRecordedFile, mockopenai.FaultConfig{PadEmptyChunks: 50})

	_, raw := postChat(t, srv.URL, sample.Request)
	served := servedChunks(t, raw)
	require.Len(t, served, len(sample.Recorded.Stream)+50)
	require.Equal(t, recordedContent(t, sample.Recorded.Stream), recordedContent(t, served))
	require.Equal(t, recordedPositions(t, sample.Recorded.Stream), recordedPositions(t, served))
	require.Equal(t, recordedUsage(t, sample.Recorded.Stream), recordedUsage(t, served))
	require.True(t, strings.HasSuffix(string(raw), "data: [DONE]\n\n"))

	final := 0
	for !chunkFinishes(t, sample.Recorded.Stream[final]) {
		final++
	}
	for i := final; i < final+50; i++ {
		require.False(t, chunkHasToken(t, served[i]))
		require.False(t, chunkFinishes(t, served[i]))
		require.Empty(t, chunkPromptFields(t, served[i]), "pad chunk %d repeats the prompt fields of the first chunk", i)
	}
	require.Equal(t, string(sample.Recorded.Stream[final]), string(served[final+50]))

	_, streamed := executeStreamed(t, srv.URL, sample.Request)
	usage, err := streamed.GetUsage()
	require.NoError(t, err)
	require.Equal(t, recordedUsage(t, sample.Recorded.Stream), *usage)
}

// Dropping the tokens leaves the lines, the usage and the finish: a claim with nothing generated.
func TestRecordedReplay_DropTokensKeepsUsageOnly(t *testing.T) {
	sample := firstRecordedWithTokens(t, 4)
	srv := newRecordedServer(t, primaryRecordedFile, mockopenai.FaultConfig{DropTokens: true})

	_, raw := postChat(t, srv.URL, sample.Request)
	served := servedChunks(t, raw)
	require.Len(t, served, len(sample.Recorded.Stream))
	require.Empty(t, recordedContent(t, served))
	require.Empty(t, recordedPositions(t, served))
	for _, chunk := range served {
		require.False(t, chunkHasToken(t, chunk))
	}
	require.Equal(t, recordedUsage(t, sample.Recorded.Stream), recordedUsage(t, served))
	finishes := false
	for _, chunk := range served {
		finishes = finishes || chunkFinishes(t, chunk)
	}
	require.True(t, finishes)
	require.True(t, strings.HasSuffix(string(raw), "data: [DONE]\n\n"))
}

// A seed patch changes only the requests with that seed, so rows sent at once each get their own.
func TestRecordedReplay_SeedScopesUsageKnobs(t *testing.T) {
	sample := firstRecordedWithTokens(t, 4)
	srv := newRecordedServer(t, primaryRecordedFile, mockopenai.FaultConfig{})
	patch := func(body map[string]any) {
		data, err := json.Marshal(body)
		require.NoError(t, err)
		resp, err := http.Post(srv.URL+"/testenv/fault", "application/json", bytes.NewReader(data))
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		_ = resp.Body.Close()
	}
	withSeed := func(seed int) []byte {
		var body map[string]any
		require.NoError(t, json.Unmarshal(sample.Request, &body))
		body["seed"] = seed
		data, err := json.Marshal(body)
		require.NoError(t, err)
		return data
	}
	honest := recordedUsage(t, sample.Recorded.Stream)

	patch(map[string]any{"seed": 7, "usage_completion_tokens": 1000})
	_, raw := postChat(t, srv.URL, withSeed(7))
	require.Equal(t, uint64(1000), recordedUsage(t, servedChunks(t, raw)).CompletionTokens)
	_, raw = postChat(t, srv.URL, withSeed(8))
	require.Equal(t, honest, recordedUsage(t, servedChunks(t, raw)))

	patch(map[string]any{"seed": 7, "usage_completion_tokens": 0})
	_, raw = postChat(t, srv.URL, withSeed(7))
	require.Equal(t, honest, recordedUsage(t, servedChunks(t, raw)))
}

// The knobs are runtime faults like the others: POST /testenv/fault sets them and zero clears them.
func TestRecordedReplay_UsageKnobsPatchAndClear(t *testing.T) {
	sample := firstRecordedWithTokens(t, 4)
	srv := newRecordedServer(t, primaryRecordedFile, mockopenai.FaultConfig{})
	patch := func(body map[string]int) {
		data, err := json.Marshal(body)
		require.NoError(t, err)
		resp, err := http.Post(srv.URL+"/testenv/fault", "application/json", bytes.NewReader(data))
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		_ = resp.Body.Close()
	}

	patch(map[string]int{"usage_completion_tokens": 1000, "usage_prompt_tokens": 300, "pad_empty_chunks": 2})
	_, raw := postChat(t, srv.URL, sample.Request)
	served := servedChunks(t, raw)
	require.Len(t, served, len(sample.Recorded.Stream)+2)
	usage := recordedUsage(t, served)
	require.Equal(t, uint64(1000), usage.CompletionTokens)
	require.Equal(t, uint64(300), usage.PromptTokens)

	patch(map[string]int{"usage_completion_tokens": 0, "usage_prompt_tokens": 0, "pad_empty_chunks": 0})
	_, raw = postChat(t, srv.URL, sample.Request)
	require.Equal(t, string(sseBody(sample.Recorded.Stream, true)), string(raw))
}
