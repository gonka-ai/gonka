package mockopenai_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"common/validation"
	"devshard/testenv/mockopenai"

	"github.com/stretchr/testify/require"
)

func TestChaosPickIsRepeatableAndCoversTheCatalog(t *testing.T) {
	c := mockopenai.ChaosConfig{Enabled: true, Seed: 7}
	streamed, plain := map[string]int{}, map[string]int{}
	for i := 0; i < 5000; i++ {
		seed := i
		key := fmt.Sprintf("key-%d", i%40)
		a := c.Pick(key, &seed, true)
		require.Equal(t, a, c.Pick(key, &seed, true), "the same execution gets the same action")
		streamed[a]++
		plain[c.Pick(key, &seed, false)]++
	}
	all := make([]string, 0, len(mockopenai.DefaultChaosWeights()))
	for name := range mockopenai.DefaultChaosWeights() {
		all = append(all, name)
	}
	sort.Strings(all)
	got := make([]string, 0, len(streamed))
	for name := range streamed {
		got = append(got, name)
	}
	sort.Strings(got)
	require.Equal(t, all, got, "every action of the catalog is picked")
	for _, streamOnly := range []string{mockopenai.ChaosPrelude, mockopenai.ChaosPartial, mockopenai.ChaosSSEError} {
		require.Zero(t, plain[streamOnly], "%s needs a stream", streamOnly)
	}
	require.Greater(t, streamed[mockopenai.ChaosOK], streamed[mockopenai.ChaosAbort], "default weights favour a normal answer")

	other := mockopenai.ChaosConfig{Enabled: true, Seed: 8}
	differ := 0
	for i := 0; i < 200; i++ {
		seed := i
		if c.Pick("k", &seed, true) != other.Pick("k", &seed, true) {
			differ++
		}
	}
	require.Greater(t, differ, 0, "the chaos seed changes the picks")

	only := mockopenai.ChaosConfig{Enabled: true, Weights: map[string]int{mockopenai.ChaosAbort: 1, mockopenai.ChaosOK: 0}}
	require.Equal(t, mockopenai.ChaosAbort, only.Pick("k", nil, false))
	streamOnly := mockopenai.ChaosConfig{Enabled: true, Weights: map[string]int{mockopenai.ChaosPartial: 1}}
	require.Equal(t, mockopenai.ChaosOK, streamOnly.Pick("k", nil, false), "nothing applicable falls back to ok")
}

// chaosServer reports usage honestly, so an action that changes usage is the only thing that does.
func chaosServer(t *testing.T, action string) *httptest.Server {
	t.Helper()
	_, ts := journalServer(t, func(cfg *mockopenai.Config) {
		cfg.Faults.HonestUsage = true
		cfg.Chaos = mockopenai.ChaosConfig{Enabled: true, Seed: 1, Weights: map[string]int{action: 1}, DelayMs: 150}
	})
	return ts
}

const chaosStream = `{"model":"test-model","stream":true,"logprobs":true,"top_logprobs":5,"max_tokens":20,"seed":11,"messages":[{"role":"user","content":"chaos me"}]}`

// streamed reads an execution's answer: status, content tokens, events and whether [DONE] came.
type streamed struct {
	status  int
	body    string
	tokens  int
	events  []map[string]any
	done    bool
	elapsed time.Duration
	err     error
}

func execute(t *testing.T, ts *httptest.Server, body string) streamed {
	t.Helper()
	begin := time.Now()
	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		return streamed{err: err, elapsed: time.Since(begin)}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	out := streamed{status: resp.StatusCode, body: string(raw), err: err, elapsed: time.Since(begin)}
	for _, line := range strings.Split(out.body, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			out.done = true
			continue
		}
		var ev map[string]any
		require.NoError(t, json.Unmarshal([]byte(payload), &ev))
		out.events = append(out.events, ev)
		if choices, ok := ev["choices"].([]any); ok && len(choices) > 0 {
			delta, _ := choices[0].(map[string]any)["delta"].(map[string]any)
			if text, _ := delta["content"].(string); text != "" {
				out.tokens += len([]rune(text))
			}
		}
	}
	return out
}

func usageCompletion(s streamed) (int, bool) {
	for _, ev := range s.events {
		if u, ok := ev["usage"].(map[string]any); ok {
			return int(u["completion_tokens"].(float64)), true
		}
	}
	return 0, false
}

func lastRecord(t *testing.T, ts *httptest.Server) mockopenai.RequestRecord {
	t.Helper()
	log := getLog(t, ts)
	require.NotEmpty(t, log.Records)
	return log.Records[len(log.Records)-1]
}

func TestChaosActionsShapeTheExecutionAndTheLogNamesThem(t *testing.T) {
	type check func(t *testing.T, s streamed)
	cases := map[string]check{
		mockopenai.ChaosOK: func(t *testing.T, s streamed) {
			require.Equal(t, http.StatusOK, s.status)
			require.True(t, s.done)
			n, ok := usageCompletion(s)
			require.True(t, ok)
			require.Equal(t, s.tokens, n)
		},
		mockopenai.ChaosDelay: func(t *testing.T, s streamed) {
			require.Equal(t, http.StatusOK, s.status)
			require.GreaterOrEqual(t, s.elapsed, 150*time.Millisecond)
			require.True(t, s.done)
		},
		mockopenai.ChaosClientError: func(t *testing.T, s streamed) {
			require.Equal(t, http.StatusBadRequest, s.status)
			require.JSONEq(t, `{"error":{"code":400,"message":"mock-openai chaos: request rejected","param":null,"type":"BadRequestError"}}`, s.body)
		},
		mockopenai.ChaosServerError: func(t *testing.T, s streamed) {
			require.Equal(t, http.StatusInternalServerError, s.status)
			require.Contains(t, s.body, `"type":"InternalServerError"`)
		},
		mockopenai.ChaosErrorString: func(t *testing.T, s streamed) {
			require.Equal(t, http.StatusInternalServerError, s.status)
			require.JSONEq(t, `{"error":"mock-openai chaos: error as a string"}`, s.body)
		},
		mockopenai.ChaosAbort: func(t *testing.T, s streamed) {
			require.Error(t, s.err, "the connection is dropped without a response")
		},
		mockopenai.ChaosPrelude: func(t *testing.T, s streamed) {
			require.Equal(t, http.StatusOK, s.status)
			require.GreaterOrEqual(t, s.elapsed, 2*time.Second, "held after the role-only chunks")
			require.True(t, s.done)
		},
		mockopenai.ChaosPartial: func(t *testing.T, s streamed) {
			require.Equal(t, http.StatusOK, s.status)
			require.False(t, s.done)
			_, ok := usageCompletion(s)
			require.False(t, ok, "no finish, no usage")
			require.Greater(t, s.tokens, 0)
		},
		mockopenai.ChaosSSEError: func(t *testing.T, s streamed) {
			require.Equal(t, http.StatusOK, s.status)
			require.True(t, s.done)
			last := s.events[len(s.events)-1]
			require.Contains(t, fmt.Sprint(last["error"]), "engine failure mid-stream")
		},
		mockopenai.ChaosEmpty: func(t *testing.T, s streamed) {
			require.Equal(t, http.StatusOK, s.status)
			require.True(t, s.done)
			require.Zero(t, s.tokens)
			n, ok := usageCompletion(s)
			require.True(t, ok)
			require.Zero(t, n)
		},
		mockopenai.ChaosOddUsage: func(t *testing.T, s streamed) {
			require.Equal(t, http.StatusOK, s.status)
			n, ok := usageCompletion(s)
			require.True(t, ok)
			require.Equal(t, 3*s.tokens+7, n)
		},
		mockopenai.ChaosWrongAnswer: func(t *testing.T, s streamed) {
			require.Equal(t, http.StatusOK, s.status)
			require.True(t, s.done)
			require.Contains(t, s.body, `"logprob":-20`)
			require.NotContains(t, s.body, `"logprob":-0.1`)
		},
	}
	require.Len(t, cases, len(mockopenai.DefaultChaosWeights()), "every action has a case")
	for action, verify := range cases {
		t.Run(action, func(t *testing.T) {
			ts := chaosServer(t, action)
			verify(t, execute(t, ts, chaosStream))
			rec := lastRecord(t, ts)
			require.Equal(t, mockopenai.PhaseExecute, rec.Phase)
			require.Equal(t, action, rec.Chaos)

			// A validator's replay is never touched.
			_, raw := post(t, ts, replayBody("chaos me", 99, fourTokens))
			var replay map[string]any
			require.NoError(t, json.Unmarshal(raw, &replay))
			require.Len(t, logprobEntries(t, firstChoice(t, replay)), 28)
			require.NotContains(t, string(raw), `"logprob":-20`)
			rec = lastRecord(t, ts)
			require.Equal(t, mockopenai.PhaseReplay, rec.Phase)
			require.Empty(t, rec.Chaos)
		})
	}
}

func TestChaosIsOffUntilEnabledAndAPatchTurnsItOn(t *testing.T) {
	_, ts := journalServer(t, nil)
	execute(t, ts, chaosStream)
	require.Empty(t, lastRecord(t, ts).Chaos)

	patch := func(body string) int {
		resp, err := http.Post(ts.URL+"/testenv/fault", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		resp.Body.Close()
		return resp.StatusCode
	}
	require.Equal(t, http.StatusBadRequest, patch(`{"chaos":{"enabled":true,"weights":{"nonsense":1}}}`))
	require.Equal(t, http.StatusOK, patch(`{"chaos":{"enabled":true,"seed":3,"weights":{"status_500":1}}}`))
	require.Equal(t, http.StatusInternalServerError, execute(t, ts, chaosStream).status)
	require.Equal(t, mockopenai.ChaosServerError, lastRecord(t, ts).Chaos)
	require.Equal(t, http.StatusOK, patch(`{"chaos":{"enabled":false}}`))
	require.Equal(t, http.StatusOK, execute(t, ts, chaosStream).status)
}

// A wrong answer must be one the production validator rejects at mainnet's threshold, and a correct
// one must pass it; otherwise a run with a dishonest executor tests nothing. The validator may reject
// it before the similarity: positions that diverge from the replay are invalid.
func TestChaosWrongAnswerFailsTheProductionValidator(t *testing.T) {
	const threshold = 0.958 // mainnet's bar since v0.2.10
	prompt := []byte(`{"model":"test-model","max_tokens":80,"logprobs":true,"top_logprobs":5,"messages":[{"role":"user","content":"hello validator"}]}`)
	validate := func(action string) validation.ValidationResult {
		ts := chaosServer(t, action)
		_, stored := post(t, ts, string(prompt))
		replay := func(ctx context.Context, body []byte) (*http.Response, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/v1/chat/completions", bytes.NewReader(body))
			if err != nil {
				return nil, err
			}
			req.Header.Set("Content-Type", "application/json")
			return http.DefaultClient.Do(req)
		}
		res, err := validation.ExecuteValidation(context.Background(), "12345", prompt, stored, replay, 3, 80, "", 0)
		require.NoError(t, err)
		return res
	}
	ok, isSimilarity := validate(mockopenai.ChaosOK).(*validation.SimilarityValidationResult)
	require.True(t, isSimilarity, "an honest answer reaches the similarity")
	require.True(t, validation.SimilarityPassesThreshold(ok.Value, threshold))
	switch res := validate(mockopenai.ChaosWrongAnswer).(type) {
	case *validation.InvalidInferenceResult:
	case *validation.SimilarityValidationResult:
		require.False(t, validation.SimilarityPassesThreshold(res.Value, threshold), "similarity %.3f", res.Value)
	default:
		t.Fatalf("wrong_answer: %T %+v", res, res)
	}
}
