package mockopenai_test

// Checks of the mock's request journal and of the helpers the chaos tests share. Every expectation is
// a literal or is decoded with encoding/json: a known request must give a known record, so that a
// result later read from a load run can be traced to what the mock was given.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"devshard/testenv/mockopenai"

	"github.com/stretchr/testify/require"
)

func journalServer(t *testing.T, tune func(*mockopenai.Config)) (*mockopenai.Server, *httptest.Server) {
	t.Helper()
	cfg := mockopenai.DefaultConfig()
	if tune != nil {
		tune(&cfg)
	}
	srv, err := mockopenai.NewServerWithError(cfg)
	require.NoError(t, err)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, ts
}

func post(t *testing.T, ts *httptest.Server, body string) (int, []byte) {
	t.Helper()
	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, raw
}

func postMap(t *testing.T, ts *httptest.Server, body string) map[string]any {
	t.Helper()
	status, raw := post(t, ts, body)
	require.Equal(t, http.StatusOK, status, string(raw))
	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

func firstChoice(t *testing.T, resp map[string]any) map[string]any {
	t.Helper()
	choices, ok := resp["choices"].([]any)
	require.True(t, ok)
	require.Len(t, choices, 1)
	return choices[0].(map[string]any)
}

func logprobEntries(t *testing.T, choice map[string]any) []any {
	t.Helper()
	lp, ok := choice["logprobs"].(map[string]any)
	require.True(t, ok, "choice has no logprobs object: %#v", choice["logprobs"])
	content, ok := lp["content"].([]any)
	require.True(t, ok)
	return content
}

func usageOf(t *testing.T, resp map[string]any) (prompt, completion int) {
	t.Helper()
	u := resp["usage"].(map[string]any)
	return int(u["prompt_tokens"].(float64)), int(u["completion_tokens"].(float64))
}

// replayBody builds a validator-shaped replay request around the given enforced tokens.
func replayBody(prompt string, seed int, tokens string) string {
	return fmt.Sprintf(`{"model":"test-model","stream":false,"logprobs":true,"top_logprobs":5,"seed":%d,`+
		`"messages":[{"role":"user","content":%q}],"enforced_tokens":{"tokens":%s}}`, seed, prompt, tokens)
}

const eightTokens = `[
 {"token":"101","top_tokens":["101","7","8","9","10"]},
 {"token":"102","top_tokens":["102","7","8","9","10"]},
 {"token":"103","top_tokens":["103","7","8","9","10"]},
 {"token":"104","top_tokens":["104","7","8","9","10"]},
 {"token":"105","top_tokens":["105","7","8","9","10"]},
 {"token":"106","top_tokens":["106","7","8","9","10"]},
 {"token":"107","top_tokens":["107","7","8","9","10"]},
 {"token":"108","top_tokens":["108","7","8","9","10"]}]`

const fourTokens = `[
 {"token":"101","top_tokens":["101","7","8","9","10"]},
 {"token":"102","top_tokens":["102","7","8","9","10"]},
 {"token":"103","top_tokens":["103","7","8","9","10"]},
 {"token":"104","top_tokens":["104","7","8","9","10"]}]`

// sseEvents decodes the data lines of a text/event-stream body; [DONE] is returned as nil.
func sseEvents(t *testing.T, body io.Reader) []map[string]any {
	t.Helper()
	var out []map[string]any
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			out = append(out, nil)
			continue
		}
		var ev map[string]any
		require.NoError(t, json.Unmarshal([]byte(payload), &ev))
		out = append(out, ev)
	}
	require.NoError(t, sc.Err())
	return out
}

func streamRequest(t *testing.T, ts *httptest.Server, body string) []map[string]any {
	t.Helper()
	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	return sseEvents(t, resp.Body)
}

const streamBody = `{"model":"test-model","stream":true,"logprobs":true,"top_logprobs":5,"messages":[{"role":"user","content":"stream me"}]}`

func getStats(t *testing.T, ts *httptest.Server) mockopenai.Stats {
	t.Helper()
	resp, err := http.Get(ts.URL + "/testenv/stats")
	require.NoError(t, err)
	defer resp.Body.Close()
	var st mockopenai.Stats
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&st))
	return st
}

type logExport struct {
	Complete bool                       `json:"complete"`
	Dropped  int                        `json:"dropped"`
	Records  []mockopenai.RequestRecord `json:"records"`
}

func getLog(t *testing.T, ts *httptest.Server) logExport {
	t.Helper()
	resp, err := http.Get(ts.URL + "/testenv/log")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var out logExport
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return out
}

func execBody(nonce string, seed int) string {
	return fmt.Sprintf(`{"model":"test-model","seed":%d,"messages":[{"role":"user","content":"req %s"}]}`, seed, nonce)
}

func TestRequestLogCarriesWhatWasSeenAndAnswered(t *testing.T) {
	_, ts := journalServer(t, nil)
	execJSON := execBody("logged", 4242)
	postMap(t, ts, execJSON)
	postMap(t, ts, replayBody("req logged", 4243, eightTokens))

	export := getLog(t, ts)
	require.True(t, export.Complete)
	require.Len(t, export.Records, 2)
	exec, replay := export.Records[0], export.Records[1]

	require.Equal(t, mockopenai.PhaseExecute, exec.Phase)
	require.NotNil(t, exec.Seed)
	require.Equal(t, 4242, *exec.Seed)
	require.Equal(t, mockopenai.PromptKey([]byte(execJSON)), exec.PromptKey)
	require.Equal(t, 28, exec.TokensOut)
	require.Equal(t, 7, exec.UsageCompletion, "len(text)/4 for the 28-rune answer")
	require.Equal(t, http.StatusOK, exec.Status)
	require.Equal(t, "ok", exec.Outcome)
	require.False(t, exec.Stream)

	require.Equal(t, mockopenai.PhaseReplay, replay.Phase)
	require.Equal(t, 8, replay.EnforcedTokens)
	require.Equal(t, 40, replay.EnforcedTops, "8 tokens with 5 alternatives each")
	// The mock answers a replay from the prompt, as it answers an execution of it.
	require.Equal(t, 28, replay.TokensOut)
	require.Equal(t, exec.PromptKey, replay.PromptKey, "an execution and its replay share the prompt key")
	require.Equal(t, 1, exec.ActiveAtStart)
	require.Greater(t, replay.Seq, exec.Seq)
}

func TestRequestLogKeepsTheWireShapeAndAbsentIsNotFalse(t *testing.T) {
	_, ts := journalServer(t, nil)
	postMap(t, ts, `{"model":"test-model","stream":false,"logprobs":true,"top_logprobs":5,"n":1,"min_tokens":64,"max_tokens":80,`+
		`"return_token_ids":true,"skip_special_tokens":false,"stream_options":{"include_usage":true},`+
		`"messages":[{"role":"user","content":"wire"}]}`)
	postMap(t, ts, `{"model":"test-model","messages":[{"role":"user","content":"bare"}]}`)
	postMap(t, ts, replayBody("wire", 3, fourTokens))

	recs := getLog(t, ts).Records
	require.Len(t, recs, 3)
	full, bare, replay := recs[0].Wire, recs[1].Wire, recs[2].Wire
	require.NotNil(t, full)
	require.Equal(t, false, *full.Stream)
	require.Equal(t, true, *full.Logprobs)
	require.Equal(t, 5, *full.TopLogprobs)
	require.Equal(t, 1, *full.N)
	require.Equal(t, 64, *full.MinTokens)
	require.Equal(t, 80, *full.MaxTokens)
	require.Equal(t, true, *full.ReturnTokenIDs)
	require.Equal(t, false, *full.SkipSpecialTokens, "a present false is kept as false")
	require.True(t, full.HasStreamOptions)
	require.Equal(t, true, *full.IncludeUsage)
	require.False(t, full.HasEnforced)

	require.Nil(t, bare.Stream, "an absent field is nil, not false")
	require.Nil(t, bare.SkipSpecialTokens)
	require.Nil(t, bare.N)
	require.False(t, bare.HasStreamOptions)
	require.Nil(t, bare.IncludeUsage)

	require.True(t, replay.HasEnforced)
	require.Equal(t, false, *replay.Stream)
}

func TestRequestLogRecordsAStreamedExecution(t *testing.T) {
	_, ts := journalServer(t, nil)
	streamRequest(t, ts, streamBody)

	export := getLog(t, ts)
	require.Len(t, export.Records, 1)
	rec := export.Records[0]
	require.True(t, rec.Stream)
	require.Equal(t, 28, rec.TokensOut, "tokens emitted, not chunks")
	require.Equal(t, 1, rec.ChunkTokens)
	require.Equal(t, http.StatusOK, rec.Status)
	require.Equal(t, "ok", rec.Outcome)
}

func TestHonestUsageReportsTheTokensTheAnswerCarried(t *testing.T) {
	const plain = `{"model":"test-model","stream":false,"logprobs":true,"top_logprobs":5,"messages":[{"role":"user","content":"twelve chars"}]}`
	const streamed = `{"model":"test-model","stream":true,"stream_options":{"include_usage":true},"logprobs":true,"top_logprobs":5,"messages":[{"role":"user","content":"twelve chars"}]}`
	streamUsage := func(t *testing.T, ts *httptest.Server) (completion, emitted int) {
		t.Helper()
		resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", strings.NewReader(streamed))
		require.NoError(t, err)
		defer resp.Body.Close()
		for _, ev := range sseEvents(t, resp.Body) {
			if ev == nil {
				continue
			}
			if u, ok := ev["usage"].(map[string]any); ok && u != nil {
				completion = int(u["completion_tokens"].(float64))
			}
			for _, c := range ev["choices"].([]any) {
				if lp, ok := c.(map[string]any)["logprobs"].(map[string]any); ok {
					emitted += len(lp["content"].([]any))
				}
			}
		}
		return completion, emitted
	}

	legacy := httptest.NewServer(mockopenai.NewServer(mockopenai.DefaultConfig()).Handler())
	t.Cleanup(legacy.Close)
	resp := postMap(t, legacy, plain)
	_, completion := usageOf(t, resp)
	emitted := len(logprobEntries(t, firstChoice(t, resp)))
	require.Less(t, completion, emitted, "without the knob usage stays the len(text)/4 estimate")

	cfg := mockopenai.DefaultConfig()
	cfg.Faults.HonestUsage = true
	honest := httptest.NewServer(mockopenai.NewServer(cfg).Handler())
	t.Cleanup(honest.Close)
	resp = postMap(t, honest, plain)
	_, completion = usageOf(t, resp)
	require.Equal(t, len(logprobEntries(t, firstChoice(t, resp))), completion, "a JSON answer reports the tokens it carried")
	completion, emitted = streamUsage(t, honest)
	require.Positive(t, emitted)
	require.Equal(t, emitted, completion, "a streamed answer reports the tokens its chunks carried")
}

// An execution answered from a vLLM recording with the usage knob on keeps both sides apart in the
// journal: the positions the stream carried and the completion count it claimed.
func TestRequestLogSeparatesTheRecordedTokensFromTheUsageClaimed(t *testing.T) {
	sample := firstRecordedWithTokens(t, 8)
	srv := newRecordedServer(t, primaryRecordedFile, mockopenai.FaultConfig{UsageCompletionTokens: 4096})

	resp, raw := postChat(t, srv.URL, sample.Request)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))

	recs := getLog(t, srv).Records
	require.Len(t, recs, 1)
	rec := recs[0]
	require.Equal(t, mockopenai.PhaseExecute, rec.Phase)
	require.Equal(t, recordedPositions(t, sample.Recorded.Stream), rec.TokensOut)
	require.Equal(t, 4096, rec.UsageCompletion)
	require.Equal(t, "ok", rec.Outcome)
}

// A request the node turns away for lack of capacity is logged with its prompt key and the 503, so
// a check can tell a node that refused from an executor that answered.
func TestRequestLogKeepsTheCapacityRefusal(t *testing.T) {
	_, ts := journalServer(t, func(c *mockopenai.Config) {
		c.Faults.PauseStream = true
		c.Workers, c.Queue = 1, 0
	})

	// The held request is a stream paused after its first chunk: once that chunk is read, the
	// only worker is taken.
	const heldBody = `{"model":"test-model","stream":true,"messages":[{"role":"user","content":"busy held"}]}`
	const refusedBody = `{"model":"test-model","messages":[{"role":"user","content":"busy refused"}]}`
	held, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", strings.NewReader(heldBody))
	require.NoError(t, err)
	defer held.Body.Close()
	line, err := bufio.NewReader(held.Body).ReadString('\n')
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(line, "data: "), line)

	status, raw := post(t, ts, refusedBody)
	require.Equal(t, http.StatusServiceUnavailable, status, string(raw))

	release, err := http.Post(ts.URL+"/testenv/stream/release", "application/json", nil)
	require.NoError(t, err)
	require.NoError(t, release.Body.Close())
	_, err = io.Copy(io.Discard, held.Body)
	require.NoError(t, err)

	byKey := map[string]mockopenai.RequestRecord{}
	for _, rec := range getLog(t, ts).Records {
		byKey[rec.PromptKey] = rec
	}
	require.Len(t, byKey, 2)
	refused := byKey[mockopenai.PromptKey([]byte(refusedBody))]
	require.Equal(t, http.StatusServiceUnavailable, refused.Status)
	require.Equal(t, "capacity_exhausted", refused.Outcome)
	require.Zero(t, refused.TokensOut)
	heldRec := byKey[mockopenai.PromptKey([]byte(heldBody))]
	require.Equal(t, http.StatusOK, heldRec.Status)
	require.Equal(t, "ok", heldRec.Outcome)
}
