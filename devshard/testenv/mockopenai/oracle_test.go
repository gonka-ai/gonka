package mockopenai_test

// Checks of the oracle mode and the fault rules. Every expectation is a literal or is decoded with
// encoding/json: a known request must give a known answer, for full and for cut enforced sequences,
// so that a result later read from a load run can be traced to what the mock was given. The last test
// feeds the mock's answers to the production validator as a consumer check.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"common/completionapi"
	"common/validation"
	"devshard/testenv/mockopenai"

	"github.com/stretchr/testify/require"
)

func oracleServer(t *testing.T, tune func(*mockopenai.Config)) (*mockopenai.Server, *httptest.Server) {
	t.Helper()
	cfg := mockopenai.DefaultConfig()
	cfg.Faults.Oracle = true
	if tune != nil {
		tune(&cfg)
	}
	srv := mockopenai.NewServer(cfg)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, ts
}

func TestOracleReplayAnswersFromTheEnforcedTokensItReceived(t *testing.T) {
	_, ts := oracleServer(t, nil)

	full := postMap(t, ts, replayBody("twelve chars", 55, eightTokens))
	entries := logprobEntries(t, firstChoice(t, full))
	require.Len(t, entries, 8)
	for i, e := range entries {
		entry := e.(map[string]any)
		require.Equal(t, fmt.Sprint(101+i), entry["token"], "token id echoed at position %d", i)
		require.InDelta(t, -0.1, entry["logprob"], 1e-12)
		tops := entry["top_logprobs"].([]any)
		require.Len(t, tops, 5)
		wantTops := []string{fmt.Sprint(101 + i), "7", "8", "9", "10"}
		for r, tp := range tops {
			top := tp.(map[string]any)
			require.Equal(t, wantTops[r], top["token"], "alternative %d at position %d", r, i)
			require.InDelta(t, -0.1-float64(r), top["logprob"], 1e-12, "logprob follows the rank only")
		}
	}
	prompt, completion := usageOf(t, full)
	require.Equal(t, 3, prompt, "len(\"twelve chars\") / 4")
	require.Equal(t, 8, completion, "completion_tokens is the number of enforced tokens received")

	// The same prompt replayed with a shorter sequence gives a shorter answer: the replay is not fixed.
	cut := postMap(t, ts, replayBody("twelve chars", 55, fourTokens))
	require.Len(t, logprobEntries(t, firstChoice(t, cut)), 4)
	cutPrompt, cutCompletion := usageOf(t, cut)
	require.Equal(t, 3, cutPrompt)
	require.Equal(t, 4, cutCompletion)
}

func TestOracleReplayEchoesMissingAlternativesAsMissing(t *testing.T) {
	_, ts := oracleServer(t, nil)
	noTops := `[{"token":"101","top_tokens":[]},{"token":"102","top_tokens":null}]`
	resp := postMap(t, ts, replayBody("twelve chars", 55, noTops))
	entries := logprobEntries(t, firstChoice(t, resp))
	require.Len(t, entries, 2)
	for _, e := range entries {
		require.Empty(t, e.(map[string]any)["top_logprobs"], "the mock must not invent alternatives the caller did not send")
	}
	// top_logprobs=3 on the request limits what comes back.
	body := strings.Replace(replayBody("twelve chars", 55, fourTokens), `"top_logprobs":5`, `"top_logprobs":3`, 1)
	limited := postMap(t, ts, body)
	for _, e := range logprobEntries(t, firstChoice(t, limited)) {
		require.Len(t, e.(map[string]any)["top_logprobs"], 3)
	}
}

func TestOracleTokenIdsAreStableAcrossPaths(t *testing.T) {
	body := `{"model":"test-model","logprobs":true,"top_logprobs":2,"messages":[{"role":"user","content":"spell"}]}`
	tokenOf := func(ts *httptest.Server) string {
		entry := logprobEntries(t, firstChoice(t, postMap(t, ts, body)))[0].(map[string]any)
		return entry["token"].(string)
	}

	_, oracle := oracleServer(t, nil)
	token := tokenOf(oracle)
	// An id is derived from the token text alone, so the same request gets the same id again.
	require.Equal(t, token, tokenOf(oracle), "token ids are stable")

	streamed := streamRequest(t, oracle, `{"model":"test-model","stream":true,"logprobs":true,"top_logprobs":2,"messages":[{"role":"user","content":"spell"}]}`)
	var first map[string]any
	for _, ev := range streamed {
		if ev == nil {
			continue
		}
		choice := ev["choices"].([]any)[0].(map[string]any)
		if lp, ok := choice["logprobs"].(map[string]any); ok {
			first = lp["content"].([]any)[0].(map[string]any)
			break
		}
	}
	require.NotNil(t, first)
	require.Equal(t, token, first["token"], "the streamed path gives the same id")
	require.Equal(t, []any{float64('m')}, first["bytes"], "the chosen token's bytes are its decoded text")
}

func TestOracleOffKeepsLegacyReplayBehaviour(t *testing.T) {
	cfg := mockopenai.DefaultConfig()
	ts := httptest.NewServer(mockopenai.NewServer(cfg).Handler())
	t.Cleanup(ts.Close)

	replay := postMap(t, ts, replayBody("twelve chars", 55, fourTokens))
	plain := postMap(t, ts, `{"model":"test-model","stream":false,"logprobs":true,"top_logprobs":5,"messages":[{"role":"user","content":"twelve chars"}]}`)
	require.Equal(t, firstChoice(t, plain)["message"], firstChoice(t, replay)["message"],
		"without the oracle knob enforced_tokens is ignored, as before")
	require.Greater(t, len(logprobEntries(t, firstChoice(t, replay))), 4)
}

// contentChunks keeps the events that carry content text, with the number of logprob entries each holds.
type contentChunk struct {
	text    string
	entries int
}

func contentChunks(t *testing.T, events []map[string]any) []contentChunk {
	t.Helper()
	var out []contentChunk
	for _, ev := range events {
		if ev == nil {
			continue
		}
		choice := ev["choices"].([]any)[0].(map[string]any)
		delta, _ := choice["delta"].(map[string]any)
		text, _ := delta["content"].(string)
		if text == "" {
			continue
		}
		entries := 0
		if lp, ok := choice["logprobs"].(map[string]any); ok {
			entries = len(lp["content"].([]any))
		}
		out = append(out, contentChunk{text: text, entries: entries})
	}
	return out
}

func TestOracleStreamGroupsTokensPerChunk(t *testing.T) {
	// The unpadded text is "mock-openai:" (12 runes) and 16 hex digits: 28 tokens, one rune each.
	const wantTokens = 28
	cases := []struct {
		name  string
		per   int
		sizes []int
	}{
		{"default one per chunk", 0, repeat(1, 28)},
		{"explicit one", 1, repeat(1, 28)},
		{"four", 4, repeat(4, 7)},
		{"uneven", 5, []int{5, 5, 5, 5, 5, 3}},
		{"all in one", 100, []int{28}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, ts := oracleServer(t, func(c *mockopenai.Config) { c.Faults.ChunkTokens = tc.per })
			events := streamRequest(t, ts, streamBody)
			chunks := contentChunks(t, events)
			var sizes []int
			var text strings.Builder
			for _, c := range chunks {
				sizes = append(sizes, c.entries)
				require.Equal(t, len([]rune(c.text)), c.entries, "a chunk carries one logprob entry per token it contains")
				text.WriteString(c.text)
			}
			require.Equal(t, tc.sizes, sizes)
			require.Len(t, []rune(text.String()), wantTokens)
			require.True(t, strings.HasPrefix(text.String(), "mock-openai:"))

			usage := lastUsage(t, events)
			require.Equal(t, wantTokens, int(usage["completion_tokens"].(float64)),
				"oracle usage is the number of tokens emitted, whatever the grouping")
			require.Equal(t, 2, int(usage["prompt_tokens"].(float64)), "len(\"stream me\") / 4")
		})
	}
}

func repeat(n, times int) []int {
	out := make([]int, times)
	for i := range out {
		out[i] = n
	}
	return out
}

func lastUsage(t *testing.T, events []map[string]any) map[string]any {
	t.Helper()
	for i := len(events) - 1; i >= 0; i-- {
		if events[i] != nil {
			if u, ok := events[i]["usage"].(map[string]any); ok {
				return u
			}
		}
	}
	t.Fatal("no usage event in the stream")
	return nil
}

func TestOracleStreamAndReplayAgreeOnTheSameTokens(t *testing.T) {
	// What an execution emits, fed back as enforced tokens, is replayed without loss.
	_, ts := oracleServer(t, func(c *mockopenai.Config) { c.Faults.ChunkTokens = 4 })
	events := streamRequest(t, ts, streamBody)
	var enforced []map[string]any
	for _, ev := range events {
		if ev == nil {
			continue
		}
		choice := ev["choices"].([]any)[0].(map[string]any)
		lp, ok := choice["logprobs"].(map[string]any)
		if !ok {
			continue
		}
		for _, e := range lp["content"].([]any) {
			entry := e.(map[string]any)
			var tops []string
			for _, tp := range entry["top_logprobs"].([]any) {
				tops = append(tops, tp.(map[string]any)["token"].(string))
			}
			enforced = append(enforced, map[string]any{"token": entry["token"], "top_tokens": tops})
		}
	}
	require.Len(t, enforced, 28)
	raw, err := json.Marshal(enforced)
	require.NoError(t, err)
	replay := postMap(t, ts, replayBody("stream me", 9, string(raw)))
	entries := logprobEntries(t, firstChoice(t, replay))
	require.Len(t, entries, 28)
	_, completion := usageOf(t, replay)
	require.Equal(t, 28, completion)
	for i, e := range entries {
		require.Equal(t, enforced[i]["token"], e.(map[string]any)["token"])
	}
}

func TestOracleStreamSchedule(t *testing.T) {
	// 28 tokens in chunks of 7: four chunks. First content no earlier than 50 ms, last no earlier than 400 ms.
	_, ts := oracleServer(t, func(c *mockopenai.Config) {
		c.Faults.ChunkTokens = 7
		c.Faults.FirstContentDelay = 50 * time.Millisecond
		c.Faults.ExecDuration = 400 * time.Millisecond
	})
	begin := time.Now()
	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", strings.NewReader(streamBody))
	require.NoError(t, err)
	defer resp.Body.Close()
	var stamps []time.Duration
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: {") {
			continue
		}
		var ev struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev))
		if len(ev.Choices) > 0 && ev.Choices[0].Delta.Content != "" {
			stamps = append(stamps, time.Since(begin))
		}
	}
	require.Len(t, stamps, 4)
	require.GreaterOrEqual(t, stamps[0], 50*time.Millisecond, "first content is not early")
	require.Less(t, stamps[0], 350*time.Millisecond, "first content is not held to the end")
	require.GreaterOrEqual(t, stamps[3], 400*time.Millisecond, "the last chunk waits for the execution time")
	require.Less(t, stamps[3], 2*time.Second)
	for i := 1; i < len(stamps); i++ {
		require.Greater(t, stamps[i], stamps[i-1])
	}
}

func TestOracleReplayDelayAppliesToReplayOnly(t *testing.T) {
	_, ts := oracleServer(t, func(c *mockopenai.Config) { c.Faults.ReplayDelay = 300 * time.Millisecond })

	begin := time.Now()
	postMap(t, ts, replayBody("twelve chars", 55, fourTokens))
	require.GreaterOrEqual(t, time.Since(begin), 300*time.Millisecond)

	begin = time.Now()
	postMap(t, ts, `{"model":"test-model","messages":[{"role":"user","content":"twelve chars"}]}`)
	require.Less(t, time.Since(begin), 250*time.Millisecond, "an execution is not slowed by the replay delay")
}

func setRules(t *testing.T, ts *httptest.Server, rules string) int {
	t.Helper()
	resp, err := http.Post(ts.URL+"/testenv/rules", "application/json", strings.NewReader(rules))
	require.NoError(t, err)
	defer resp.Body.Close()
	return resp.StatusCode
}

func TestRulesHitOnlyTheRequestsTheyName(t *testing.T) {
	_, ts := oracleServer(t, nil)
	require.Equal(t, http.StatusOK, setRules(t, ts, `[
	  {"name":"seed777-once","seeds":[777],"times":1,"http_status":503},
	  {"name":"nonce-bad","prompt_contains":"NONCE-BAD","http_status":500}]`))

	// A request with another seed goes first: it must not spend the rule's one time.
	status, _ := post(t, ts, execBody("a", 778))
	require.Equal(t, http.StatusOK, status, "another seed is untouched")
	status, _ = post(t, ts, execBody("a", 777))
	require.Equal(t, http.StatusServiceUnavailable, status, "first request with the named seed fails")
	status, _ = post(t, ts, execBody("a", 777))
	require.Equal(t, http.StatusOK, status, "the rule fired its one time")

	status, _ = post(t, ts, execBody("NONCE-BAD", 1))
	require.Equal(t, http.StatusInternalServerError, status)
	status, _ = post(t, ts, execBody("NONCE-BAD", 2))
	require.Equal(t, http.StatusInternalServerError, status, "no times limit: every match")
	status, _ = post(t, ts, execBody("NONCE-GOOD", 1))
	require.Equal(t, http.StatusOK, status)

	applied := getStats(t, ts).RuleApplied
	require.Equal(t, 1, applied["seed777-once"])
	require.Equal(t, 2, applied["nonce-bad"])
}

func TestRulePhaseSeparatesExecutionFromReplay(t *testing.T) {
	_, ts := oracleServer(t, nil)
	require.Equal(t, http.StatusOK, setRules(t, ts,
		`[{"name":"replay-only","phase":"replay","prompt_contains":"req x","http_status":503}]`))

	status, _ := post(t, ts, execBody("x", 5))
	require.Equal(t, http.StatusOK, status, "an execution does not match a replay rule")
	status, _ = post(t, ts, replayBody("req x", 5, fourTokens))
	require.Equal(t, http.StatusServiceUnavailable, status)
}

func TestRuleDelayAndAbort(t *testing.T) {
	_, ts := oracleServer(t, nil)
	require.Equal(t, http.StatusOK, setRules(t, ts, `[
	  {"name":"slow","prompt_contains":"SLOW","delay_ms":300},
	  {"name":"cut","prompt_contains":"CUT","times":1,"abort":true}]`))

	begin := time.Now()
	status, _ := post(t, ts, execBody("SLOW", 1))
	require.Equal(t, http.StatusOK, status)
	require.GreaterOrEqual(t, time.Since(begin), 300*time.Millisecond)

	_, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", strings.NewReader(execBody("CUT", 2)))
	require.Error(t, err, "an abort rule is a transport error for the caller")
	status, _ = post(t, ts, execBody("CUT", 2))
	require.Equal(t, http.StatusOK, status, "the abort fired its one time")

	var cut *mockopenai.RequestRecord
	for _, r := range getLog(t, ts).Records {
		if r.Rule == "cut" {
			r := r
			cut = &r
		}
	}
	require.NotNil(t, cut)
	require.Equal(t, "rule_abort", cut.Outcome)
}

func TestRulePreludeStreamsRoleOnlyChunksBeforeTheContent(t *testing.T) {
	_, ts := oracleServer(t, nil)
	require.Equal(t, http.StatusOK, setRules(t, ts,
		`[{"name":"prelude","phase":"execute","prompt_contains":"PRELUDE","prelude_chunks":5,"prelude_hold_ms":200}]`))

	body := strings.Replace(execBody("PRELUDE", 3), `"seed"`, `"stream":true,"seed"`, 1)
	begin := time.Now()
	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	raw, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.NoError(t, err)
	require.GreaterOrEqual(t, time.Since(begin), 200*time.Millisecond, "the stream is held after the prelude")

	roleOnly, contentSeen := 0, false
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "data: {") {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta map[string]any `json:"delta"`
			} `json:"choices"`
		}
		require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk))
		if len(chunk.Choices) == 0 {
			continue
		}
		delta := chunk.Choices[0].Delta
		if _, ok := delta["content"]; ok {
			contentSeen = true
			continue
		}
		if _, ok := delta["role"]; !ok {
			continue // the closing chunk carries finish_reason and an empty delta
		}
		require.False(t, contentSeen, "role-only chunks come before the content")
		roleOnly++
	}
	require.Equal(t, 5, roleOnly)
	require.True(t, contentSeen, "the answer follows the prelude")

	require.Equal(t, http.StatusBadRequest, setRules(t, ts,
		`[{"name":"neg","prompt_contains":"X","prelude_chunks":-1}]`), "a negative prelude is refused")
}

func TestRulesAreRefusedWhenTheyCouldBecomeGlobalKnobs(t *testing.T) {
	_, ts := oracleServer(t, nil)
	for name, body := range map[string]string{
		"no selector": `[{"name":"x","http_status":500}]`,
		"no action":   `[{"name":"x","seeds":[1]}]`,
		"no name":     `[{"seeds":[1],"http_status":500}]`,
		"bad phase":   `[{"name":"x","seeds":[1],"phase":"later","http_status":500}]`,
		"status 200":  `[{"name":"x","seeds":[1],"http_status":200}]`,
	} {
		require.Equal(t, http.StatusBadRequest, setRules(t, ts, body), name)
	}
}

// The consumer check: the production validator reads the mock's answers. A full sequence is accepted;
// a sequence cut by more than the validator's tolerance fails the token-count gate; a cut within the
// tolerance passes. The expectations are the validator's published tolerance (3 tokens), not the mock's.
func TestOracleAnswersDriveTheProductionValidator(t *testing.T) {
	_, ts := oracleServer(t, nil)
	const total = 80 // padded by max_tokens; above the 64-token floor the validator enforces
	prompt := []byte(`{"model":"test-model","max_tokens":80,"logprobs":true,"top_logprobs":5,"messages":[{"role":"user","content":"hello oracle"}]}`)

	_, raw := post(t, ts, string(prompt))
	var stored map[string]any
	require.NoError(t, json.Unmarshal(raw, &stored))
	entries := logprobEntries(t, firstChoice(t, stored))
	require.Len(t, entries, total)
	storedPrompt, storedCompletion := usageOf(t, stored)
	require.Equal(t, 3, storedPrompt, "an execution reports the same prompt figure its replay will")
	require.Equal(t, total, storedCompletion, "oracle usage on an execution is the token count")

	execute := func(ctx context.Context, body []byte) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/v1/chat/completions", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		return http.DefaultClient.Do(req)
	}
	storedCut := func(keep int) []byte {
		cut := map[string]any{}
		for k, v := range stored {
			cut[k] = v
		}
		choice := map[string]any{}
		for k, v := range firstChoice(t, stored) {
			choice[k] = v
		}
		choice["logprobs"] = map[string]any{"content": entries[:keep]}
		cut["choices"] = []any{choice}
		out, err := json.Marshal(cut)
		require.NoError(t, err)
		return out
	}
	const claimedInput, claimedOutput = 3, total // 12 chars / 4 = 3 prompt tokens; 80 tokens generated

	res, err := validation.ExecuteValidation(context.Background(), "12345", prompt, storedCut(total), execute, claimedInput, claimedOutput, "", 0)
	require.NoError(t, err)
	sim, ok := res.(*validation.SimilarityValidationResult)
	require.True(t, ok, "full sequence: %T %+v", res, res)
	require.InDelta(t, 1.0, sim.Value, 1e-9)

	res, err = validation.ExecuteValidation(context.Background(), "12345", prompt, storedCut(total-2), execute, claimedInput, claimedOutput, "", 0)
	require.NoError(t, err)
	_, ok = res.(*validation.SimilarityValidationResult)
	require.True(t, ok, "2 tokens short is inside the tolerance: %T %+v", res, res)

	res, err = validation.ExecuteValidation(context.Background(), "12345", prompt, storedCut(total-10), execute, claimedInput, claimedOutput, "", 0)
	require.NoError(t, err)
	invalid, ok := res.(*validation.InvalidInferenceResult)
	require.True(t, ok, "10 tokens short: %T %+v", res, res)
	require.Equal(t, "Inflated token counts.", invalid.Reason)
}

func TestRuleStatusMustBeAnHTTPErrorStatus(t *testing.T) {
	_, ts := oracleServer(t, nil)
	require.Equal(t, http.StatusBadRequest, setRules(t, ts, `[{"name":"typo","prompt_contains":"X","http_status":5030}]`))
	require.Equal(t, http.StatusBadRequest, setRules(t, ts, `[{"name":"typo","prompt_contains":"X","http_status":600}]`))
	require.Equal(t, http.StatusBadRequest, setRules(t, ts, `[{"name":"ok","prompt_contains":"X","delay_ms":1,"http_status":200}]`))
}

func TestRulesWithTheSameNameAreRefused(t *testing.T) {
	_, ts := oracleServer(t, nil)
	require.Equal(t, http.StatusBadRequest, setRules(t, ts, `[
	  {"name":"r","prompt_contains":"A","http_status":503},
	  {"name":"r","prompt_contains":"B","http_status":500}]`))
}

// Test flow:
//  1. Build the execution body the host sends for inference 77 and replay its answer through the
//     production validator.
//  2. Capture the seed of both requests.
//  3. Check they are the same number: a seed selects the inference, not the phase.
func TestReplaySeedIsTheExecutionSeed(t *testing.T) {
	_, ts := oracleServer(t, nil)
	const inferenceID = 77
	prompt := []byte(`{"model":"test-model","max_tokens":80,"messages":[{"role":"user","content":"seed check"}]}`)
	exec, err := completionapi.ModifyRequestBodyWithLogprobsMode(prompt, int32(inferenceID), "")
	require.NoError(t, err)
	_, stored := post(t, ts, string(exec.NewBody))
	var replaySeed *int
	execute := func(ctx context.Context, body []byte) (*http.Response, error) {
		var probe struct {
			Seed *int `json:"seed"`
		}
		require.NoError(t, json.Unmarshal(body, &probe))
		replaySeed = probe.Seed
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/v1/chat/completions", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		return http.DefaultClient.Do(req)
	}
	_, err = validation.ExecuteValidation(context.Background(), fmt.Sprint(inferenceID), prompt, stored, execute, 0, 0, "", 0)
	require.NoError(t, err)
	var execProbe struct {
		Seed *int `json:"seed"`
	}
	require.NoError(t, json.Unmarshal(exec.NewBody, &execProbe))
	require.NotNil(t, execProbe.Seed)
	require.NotNil(t, replaySeed)
	require.Equal(t, inferenceID, *execProbe.Seed)
	require.Equal(t, *execProbe.Seed, *replaySeed, "a seed rule hits the execution and its replays alike; phase separates them")
}

func TestRuleWithoutPreludeKeepsTheChaosPrelude(t *testing.T) {
	_, ts := oracleServer(t, func(c *mockopenai.Config) {
		c.Faults.Oracle = false
		c.Chaos = mockopenai.ChaosConfig{Enabled: true, Weights: map[string]int{mockopenai.ChaosPrelude: 1}}
	})
	require.Equal(t, http.StatusOK, setRules(t, ts, `[{"name":"slow","phase":"execute","prompt_contains":"PRE","delay_ms":10}]`))
	events := streamRequest(t, ts, `{"model":"test-model","stream":true,"messages":[{"role":"user","content":"PRE"}]}`)
	roleOnly := 0
	for _, ev := range events {
		if ev == nil {
			continue
		}
		delta, _ := ev["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)
		_, role := delta["role"]
		_, content := delta["content"]
		if role && !content {
			roleOnly++
		}
	}
	rec := getLog(t, ts).Records[0]
	require.Equal(t, mockopenai.ChaosPrelude, rec.Chaos)
	require.Equal(t, "slow", rec.Rule)
	require.Equal(t, 3, roleOnly, "the chaos prelude the log names went out")
}

func TestOracleReplayStreamsWhenAskedLikeVLLM(t *testing.T) {
	_, ts := oracleServer(t, func(c *mockopenai.Config) { c.Faults.ChunkTokens = 3 })
	body := strings.Replace(replayBody("twelve chars", 55, fourTokens), `"stream":false`,
		`"stream":true,"stream_options":{"include_usage":true}`, 1)
	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.True(t, strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream"),
		"the validator always replays with stream:true; vLLM answers with SSE, got %q", resp.Header.Get("Content-Type"))
	events := sseEvents(t, resp.Body)
	require.Nil(t, events[len(events)-1], "stream ends with [DONE]")
	var perChunk []int
	var ids []any
	for _, ev := range events {
		if ev == nil {
			continue
		}
		if ch, ok := ev["choices"].([]any); ok && len(ch) > 0 {
			if lp, ok := ch[0].(map[string]any)["logprobs"].(map[string]any); ok {
				content := lp["content"].([]any)
				perChunk = append(perChunk, len(content))
				for _, e := range content {
					ids = append(ids, e.(map[string]any)["token"])
				}
			}
		}
	}
	require.Equal(t, []int{3, 1}, perChunk, "replayed tokens are grouped by chunk_tokens")
	require.Equal(t, []any{"101", "102", "103", "104"}, ids, "the enforced tokens, in order")
	require.Equal(t, 4, int(lastUsage(t, events)["completion_tokens"].(float64)))

	rec := getLog(t, ts).Records[0]
	require.Equal(t, mockopenai.PhaseReplay, rec.Phase)
	require.Equal(t, 4, rec.TokensOut)
	require.Equal(t, 4, rec.UsageCompletion)
	require.Equal(t, 3, rec.ChunkTokens)
}

// The production validator reads a streamed oracle replay the way it reads vLLM's.
func TestOracleStreamedReplayDrivesTheProductionValidator(t *testing.T) {
	_, ts := oracleServer(t, func(c *mockopenai.Config) { c.Faults.ChunkTokens = 4 })
	prompt := []byte(`{"model":"test-model","max_tokens":80,"logprobs":true,"top_logprobs":5,"messages":[{"role":"user","content":"hello oracle"}]}`)
	_, stored := post(t, ts, string(prompt))
	var replays []mockopenai.RequestRecord
	execute := func(ctx context.Context, body []byte) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/v1/chat/completions", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		return http.DefaultClient.Do(req)
	}
	res, err := validation.ExecuteValidation(context.Background(), "12345", prompt, stored, execute, 3, 80, "", 0)
	require.NoError(t, err)
	sim, ok := res.(*validation.SimilarityValidationResult)
	require.True(t, ok, "%T %+v", res, res)
	require.InDelta(t, 1.0, sim.Value, 1e-9)
	for _, r := range getLog(t, ts).Records {
		if r.Phase == mockopenai.PhaseReplay {
			replays = append(replays, r)
		}
	}
	require.Len(t, replays, 1)
	require.True(t, replays[0].Stream, "the validator replays with stream:true")
}

func TestOracleChosenBytesAreTheDecodedTextLikeVLLM(t *testing.T) {
	_, ts := oracleServer(t, nil)
	choice := firstChoice(t, postMap(t, ts, `{"model":"test-model","logprobs":true,"top_logprobs":2,"messages":[{"role":"user","content":"spell"}]}`))
	content := choice["message"].(map[string]any)["content"].(string)
	entry := logprobEntries(t, choice)[0].(map[string]any)
	var want []any
	for _, b := range []byte(string([]rune(content)[0])) {
		want = append(want, float64(b))
	}
	require.Equal(t, want, entry["bytes"], "vLLM spells the chosen token's bytes from its text; only alternatives spell the id")
}

func TestExecScheduleDoesNotStretchAReplay(t *testing.T) {
	_, ts := journalServer(t, func(c *mockopenai.Config) { c.Faults.ExecDuration = 1500 * time.Millisecond })
	body := strings.Replace(replayBody("req r", 5, fourTokens), `"stream":false`, `"stream":true`, 1)
	begin := time.Now()
	streamRequest(t, ts, body)
	require.Less(t, time.Since(begin), time.Second, "ExecDuration shapes executions; a replay is not one")
}

func TestOracleReplayRecordsTheUsageItReported(t *testing.T) {
	_, ts := oracleServer(t, nil)
	postMap(t, ts, replayBody("req u", 5, fourTokens))
	rec := getLog(t, ts).Records[0]
	require.Equal(t, mockopenai.PhaseReplay, rec.Phase)
	require.Equal(t, 4, rec.TokensOut)
	require.Equal(t, 4, rec.UsageCompletion)
}

// Test flow:
//  1. A prelude rule that fires once, then a non-stream request it selects, then a streamed one.
//  2. Check the non-stream answer did not spend the rule and the streamed one got the prelude.
func TestPreludeRuleWaitsForAStream(t *testing.T) {
	_, ts := oracleServer(t, nil)
	require.Equal(t, http.StatusOK, setRules(t, ts,
		`[{"name":"prelude","phase":"execute","prompt_contains":"PRELUDE","times":1,"prelude_chunks":2}]`))
	postMap(t, ts, execBody("PRELUDE", 1))
	streamRequest(t, ts, strings.Replace(execBody("PRELUDE", 2), `"seed"`, `"stream":true,"seed"`, 1))
	recs := getLog(t, ts).Records
	require.Len(t, recs, 2)
	require.Empty(t, recs[0].Rule, "a prelude cannot shape a JSON answer")
	require.Equal(t, "prelude", recs[1].Rule)
	require.Equal(t, "prelude", recs[1].Outcome)
}

func TestNonStreamAnswerRecordsNoChunkGrouping(t *testing.T) {
	_, ts := oracleServer(t, func(c *mockopenai.Config) { c.Faults.ChunkTokens = 4 })
	postMap(t, ts, execBody("plain", 1))
	postMap(t, ts, replayBody("req plain", 1, fourTokens))
	for _, r := range getLog(t, ts).Records {
		require.Zero(t, r.ChunkTokens, "%s answered as JSON, no chunk grouping", r.Phase)
	}
}
