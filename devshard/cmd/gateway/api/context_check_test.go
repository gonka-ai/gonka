package api

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"golang.org/x/text/unicode/norm"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/filters"
	"devshard/cmd/gateway/tokenizer"
)

type fakePromptTokens struct {
	tokens       uint64
	shortfall    uint64
	slack        uint64
	sampled      bool
	known        bool
	countFails   bool
	model        string
	body         string
	tokenizing   atomic.Int64
	cappedCounts atomic.Int64
}

func (f *fakePromptTokens) Prompt(model string, body []byte) (PromptText, bool) {
	f.model, f.body = model, string(body)
	return f, f.known
}

func (f *fakePromptTokens) MaximumTokens() uint64 { return f.tokens + f.slack }

func (f *fakePromptTokens) Count() (TokenizedPrompt, bool) {
	f.tokenizing.Add(1)
	return TokenizedPrompt{Tokens: f.tokens, MinimumTokens: f.tokens - f.shortfall, Sampled: f.sampled}, !f.countFails
}

func (f *fakePromptTokens) CountWithPieceCap() (TokenizedPrompt, bool) {
	f.cappedCounts.Add(1)
	return f.Count()
}

func loadTestTokenizer(t *testing.T, name string) *tokenizer.Counter {
	t.Helper()
	file, err := os.Open(filepath.Join("..", "tokenizer", "testdata", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	counter, err := tokenizer.Load(file)
	if err != nil {
		t.Fatal(err)
	}
	return counter
}

func randomPromptText(size int, rng *rand.Rand) string {
	fragments := []string{"the ", "Шлюз ", "网关", "😀", "é", "क़", "שּׁ", "\U0001d160", "<|im_start|>", "\n\n", `{"id":1}`}
	var builder strings.Builder
	for builder.Len() < size {
		builder.WriteString(fragments[rng.IntN(len(fragments))])
	}
	return builder.String()
}

type spyStatsTokenizerFallbacks struct{ models []string }

func (s *spyStatsTokenizerFallbacks) GateFull(model string) { s.models = append(s.models, model) }

func enableStatsTokenizer(c *config.Config) { c.Server.StatsTokenizerEnabled = true }

func pinContextLength(length int64) func(*config.Config) {
	return func(c *config.Config) {
		c.Limits.ModelLimits = map[string]config.ModelLimits{"qwen": {MaxModelLen: &length}}
	}
}

func chatBodyWith(field string, value uint64) string {
	return fmt.Sprintf(`{"model":"qwen","%s":%d,"messages":[{"role":"user","content":"hi"}]}`, field, value)
}

func decodeErrorDetail(t *testing.T, body []byte) errorDetail {
	t.Helper()
	var envelope errorEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return envelope.Error
}

// Test flow:
//  1. Pin qwen's context length at 20 000 tokens and define requests with the default max_tokens, an explicit max_tokens and an explicit max_completion_tokens.
//  2. For each, wire a prompt count that leaves exactly max_tokens and the template allowance, and one token more.
//  3. Assert the exact fit is served and the one token more is refused 400 with OpenAI's type and code context_length_exceeded.
func TestTheCheckRefusesExactlyWhatTheContextCannotHoldWithMaxTokens(t *testing.T) {
	const contextLength = 20_000
	testCases := []struct {
		name      string
		body      string
		maxTokens uint64
	}{
		{name: "default max_tokens", body: chatBody, maxTokens: filters.DefaultRequestMaxTokens},
		{name: "explicit max_tokens", body: chatBodyWith("max_tokens", 100), maxTokens: 100},
		{name: "explicit max_completion_tokens", body: chatBodyWith("max_completion_tokens", 100), maxTokens: 100},
	}
	for _, testCase := range testCases {
		fits := uint64(contextLength) - testCase.maxTokens - chatTemplateMinimumTokens
		for _, step := range []struct {
			name   string
			tokens uint64
			status int
		}{{"exact fit", fits, http.StatusOK}, {"one token over", fits + 1, http.StatusBadRequest}} {
			t.Run(testCase.name+"/"+step.name, func(t *testing.T) {
				live := newHarness(t, pinContextLength(contextLength))
				live.server.promptTokens = &fakePromptTokens{tokens: step.tokens, known: true}

				recorder := live.request(t, http.MethodPost, "/v1/chat/completions", testCase.body, nil)

				if recorder.Code != step.status {
					t.Fatalf("status = %d (%s), want %d", recorder.Code, recorder.Body.String(), step.status)
				}
				if step.status == http.StatusBadRequest {
					detail := decodeErrorDetail(t, recorder.Body.Bytes())
					if detail.Type != errorTypeInvalidRequest || detail.Code != errorCodeContextLengthExceeded {
						t.Fatalf("error = %+v, want type %q and code %q", detail, errorTypeInvalidRequest, errorCodeContextLengthExceeded)
					}
				}
			})
		}
	}
}

// Test flow:
//  1. Raise the max_tokens cap, pin qwen's context length at 400 000 tokens and wire a prompt count of 380 000.
//  2. Send a chat request with max_tokens 30 000.
//  3. Assert it is refused 400 with a message naming the context length, max_tokens, the prompt, the total and both ways out.
//  4. Assert the count was asked for the normalized body of the requested model, and that neither the limiter nor the race was reached, so no nonce exists.
func TestAPromptAndMaxTokensPastTheContextAreRefusedBeforeTheRace(t *testing.T) {
	live := newHarness(t, pinContextLength(400_000), func(c *config.Config) { c.Limits.MaxTokensCap = 64_000 })
	count := &fakePromptTokens{tokens: 380_000, known: true}
	live.server.promptTokens = count

	recorder := live.request(t, http.MethodPost, "/v1/chat/completions", chatBodyWith("max_tokens", 30_000), nil)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d (%s), want 400", recorder.Code, recorder.Body.String())
	}
	message := decodeErrorDetail(t, recorder.Body.Bytes()).Message
	for _, want := range []string{"is 400000 tokens", "up to 30000 output tokens", "at least 380004 tokens", "at least 410004 in all", "by at least 10004 tokens", "lower max_tokens to at most 19996"} {
		if !strings.Contains(message, want) {
			t.Fatalf("message = %q, want it to name %q", message, want)
		}
	}
	if count.model != "qwen" || !strings.Contains(count.body, `"stream":true`) {
		t.Fatalf("counted model %q body %s, want qwen's normalized body", count.model, count.body)
	}
	if live.limiter.acquires.Load() != 0 || live.inference.raced.Load() != nil {
		t.Fatalf("limiter acquires = %d, raced = %v: a refused prompt must not reach the limiter or the race", live.limiter.acquires.Load(), live.inference.raced.Load() != nil)
	}
}

// Test flow:
//  1. Pin qwen's context length at 20 000 tokens and wire a prompt count of 25 000, past the context on its own.
//  2. Send a chat request.
//  3. Assert the refusal asks for shorter messages and offers no max_tokens that would fit.
func TestAPromptPastTheContextOnItsOwnIsToldToShortenTheMessagesOnly(t *testing.T) {
	live := newHarness(t, pinContextLength(20_000))
	live.server.promptTokens = &fakePromptTokens{tokens: 25_000, known: true}

	recorder := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, nil)

	message := decodeErrorDetail(t, recorder.Body.Bytes()).Message
	if recorder.Code != http.StatusBadRequest || !strings.Contains(message, "Shorten the messages by at least 9100 tokens.") || strings.Contains(message, "max_tokens to") {
		t.Fatalf("status = %d, message = %q, want 400 asking for 9100 fewer message tokens only", recorder.Code, message)
	}
}

// Test flow:
//  1. Define cases the check cannot judge: a model without a tokenizer, a model with no context length, and no counter wired.
//  2. For each, send a chat request whose count, where there is one, is past any context, through a fresh harness.
//  3. Assert it is served and reaches the race.
func TestARequestTheCheckCannotJudgeIsServed(t *testing.T) {
	testCases := []struct {
		name  string
		tune  func(*config.Config)
		count *fakePromptTokens
	}{
		{name: "model without a tokenizer", tune: pinContextLength(20_000), count: &fakePromptTokens{tokens: 1 << 30}},
		{name: "model without a context length", tune: func(*config.Config) {}, count: &fakePromptTokens{tokens: 1 << 30, known: true}},
		{name: "no counter", tune: pinContextLength(20_000)},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			live := newHarness(t, testCase.tune)
			if testCase.count != nil {
				live.server.promptTokens = testCase.count
			}

			recorder := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, nil)

			if recorder.Code != http.StatusOK || live.inference.raced.Load() == nil {
				t.Fatalf("status = %d (%s), raced = %v, want served", recorder.Code, recorder.Body.String(), live.inference.raced.Load() != nil)
			}
		})
	}
}

// Test flow:
//  1. Pin qwen's context length at 20 000 tokens and let governance report a --max-model-len of 40 000.
//  2. Send one request whose prompt fits 40 000 but not 20 000, and one that fits neither.
//  3. Assert the first is served and the second refused, so the governed length is the one the check reads.
func TestTheCheckReadsTheGovernedContextLengthBeforeThePin(t *testing.T) {
	for _, step := range []struct {
		tokens uint64
		status int
	}{{30_000, http.StatusOK}, {40_000, http.StatusBadRequest}} {
		live := newHarness(t, pinContextLength(20_000))
		live.snapshots.snapshot = chain.PhaseSnapshot{Models: map[string]chain.ModelParams{"qwen": {MaxModelLen: 40_000}}}
		live.server.promptTokens = &fakePromptTokens{tokens: step.tokens, known: true}

		recorder := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, nil)

		if recorder.Code != step.status {
			t.Fatalf("prompt %d: status = %d (%s), want %d", step.tokens, recorder.Code, recorder.Body.String(), step.status)
		}
	}
}

// Test flow:
//  1. Turn the stats tokenizer on, pin qwen's context length at 20 000 tokens and define a prompt count that fits it under a byte bound that does too, one that fits it under a byte bound past it, and one a token past it.
//  2. Send a chat request with each.
//  3. Assert each was tokenized exactly once, the first two served and the third refused, and only the first, counted after the cache, with the piece cap.
//  4. Assert the served ones handed the limiter and the race that same count, marked as the tokenizer's exact count.
func TestThePromptIsTokenizedOnceAndThatCountIsWhatTheLimiterAndTheRaceGet(t *testing.T) {
	fits := uint64(20_000) - filters.DefaultRequestMaxTokens - chatTemplateMinimumTokens
	for _, step := range []struct {
		name         string
		tokens       uint64
		slack        uint64
		status       int
		cappedCounts int64
	}{
		{"count and bound fit", fits, 0, http.StatusOK, 1},
		{"count fits, bound over", fits, 1, http.StatusOK, 0},
		{"count over", fits + 1, 0, http.StatusBadRequest, 0},
	} {
		t.Run(step.name, func(t *testing.T) {
			live := newHarness(t, pinContextLength(20_000), enableStatsTokenizer)
			count := &fakePromptTokens{tokens: step.tokens, slack: step.slack, known: true}
			live.server.promptTokens = count

			recorder := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, nil)

			if recorder.Code != step.status || count.tokenizing.Load() != 1 || count.cappedCounts.Load() != step.cappedCounts {
				t.Fatalf("status = %d, tokenized %d times, %d with the piece cap, want %d after once, %d with the cap", recorder.Code, count.tokenizing.Load(), count.cappedCounts.Load(), step.status, step.cappedCounts)
			}
			if step.status != http.StatusOK {
				return
			}
			raced := live.inference.raced.Load()
			if raced.InputTokens != fits || raced.InputTokensSource != filters.InputTokensTokenizerExact || live.limiter.acquired.Load() != int64(fits) {
				t.Fatalf("race got %d tokens from %q, limiter %d, want the one count %d from %q", raced.InputTokens, raced.InputTokensSource, live.limiter.acquired.Load(), fits, filters.InputTokensTokenizerExact)
			}
		})
	}
}

// Test flow:
//  1. Pin qwen's context length at 20 000 tokens and wire sampled counts past it whose minimum fits it, and one whose minimum is a token past it.
//  2. Send a chat request with each.
//  3. Assert the first is served after one count and races the whole sampled count marked tokenizer_sampled, and the second is refused naming its minimum.
func TestASampledCountIsCheckedByItsMinimumAndRacedWhole(t *testing.T) {
	const shortfall = 50
	fits := uint64(20_000) - filters.DefaultRequestMaxTokens - chatTemplateMinimumTokens
	live := newHarness(t, pinContextLength(20_000))
	count := &fakePromptTokens{tokens: fits + shortfall, shortfall: shortfall, sampled: true, known: true}
	live.server.promptTokens = count

	if recorder := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, nil); recorder.Code != http.StatusOK || count.tokenizing.Load() != 1 {
		t.Fatalf("status = %d (%s), tokenized %d times, want served after one count", recorder.Code, recorder.Body.String(), count.tokenizing.Load())
	}
	raced := live.inference.raced.Load()
	if raced.InputTokens != fits+shortfall || raced.InputTokensSource != filters.InputTokensTokenizerSampled {
		t.Fatalf("race got %d tokens from %q, want the whole sampled count %d from %q", raced.InputTokens, raced.InputTokensSource, fits+shortfall, filters.InputTokensTokenizerSampled)
	}

	refused := newHarness(t, pinContextLength(20_000))
	refused.server.promptTokens = &fakePromptTokens{tokens: fits + shortfall, shortfall: shortfall - 1, sampled: true, known: true}
	recorder := refused.request(t, http.MethodPost, "/v1/chat/completions", chatBody, nil)
	message := decodeErrorDetail(t, recorder.Body.Bytes()).Message
	if want := fmt.Sprintf("at least %d tokens with the chat template", fits+1+chatTemplateMinimumTokens); recorder.Code != http.StatusBadRequest || !strings.Contains(message, want) {
		t.Fatalf("status = %d, message = %q, want 400 naming %q", recorder.Code, message, want)
	}
}

// Test flow:
//  1. Turn the stats tokenizer on and define requests without a tokenizer count: no counter wired, and a counter that knows no tokenizer for the model.
//  2. Send a chat request with each through a fresh harness.
//  3. Assert the limiter and the race were handed the body's byte estimate, marked bytes_estimate.
func TestWithoutATokenizerCountTheRequestCarriesTheByteEstimate(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		count *fakePromptTokens
	}{{name: "no counter"}, {name: "model without a tokenizer", count: &fakePromptTokens{tokens: 7}}} {
		t.Run(testCase.name, func(t *testing.T) {
			live := newHarness(t, pinContextLength(20_000), enableStatsTokenizer)
			if testCase.count != nil {
				live.server.promptTokens = testCase.count
			}

			if recorder := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, nil); recorder.Code != http.StatusOK {
				t.Fatalf("status = %d (%s), want served", recorder.Code, recorder.Body.String())
			}
			raced := live.inference.raced.Load()
			want := filters.EstimatedPromptTokens(int(raced.InputBytes))
			if raced.InputTokens != want || raced.InputTokensSource != filters.InputTokensBytesEstimate || live.limiter.acquired.Load() != int64(want) {
				t.Fatalf("race got %d tokens from %q, limiter %d, want the byte estimate %d", raced.InputTokens, raced.InputTokensSource, live.limiter.acquired.Load(), want)
			}
		})
	}
}

// Test flow:
//  1. Load the NFC and a plain testdata tokenizer, and build chat bodies of random ASCII, Cyrillic, CJK, emoji, special-token spellings, decomposed and NFC-expanding text, as written and decomposed, from empty to past 64 KiB.
//  2. For each, take the Minimum of the tokenizer's estimate of the prompt text and context lengths around it with the template allowance and max_tokens.
//  3. Assert the check refuses exactly the requests that Minimum leaves no room for, sampled past 64 KiB included.
func TestTheCheckJudgesByTheTokenizersMinimum(t *testing.T) {
	live := newHarness(t)
	rng := rand.New(rand.NewPCG(3, 4))
	for _, name := range []string{"removed-nfc", "sequence"} {
		counter := loadTestTokenizer(t, name)
		live.server.promptTokens = counterPromptTokens{counter: counter}
		for _, size := range []int{0, 1, 200, 4 << 10, 70 << 10} {
			for _, text := range []string{randomPromptText(size, rng), norm.NFD.String(randomPromptText(size, rng))} {
				content, err := json.Marshal(text)
				if err != nil {
					t.Fatal(err)
				}
				body := []byte(`{"model":"qwen","messages":[{"role":"user","content":` + string(content) + `}]}`)
				parts, err := filters.PromptText(body)
				if err != nil {
					t.Fatal(err)
				}
				estimate, err := counter.EstimateTokens(parts)
				if err != nil {
					t.Fatal(err)
				}
				minimum := estimate.Minimum()
				for _, maxTokens := range []uint64{1, filters.DefaultRequestMaxTokens} {
					normalized := filters.Result{Model: "qwen", Body: body, MaxTokens: maxTokens}
					for _, length := range []uint64{minimum + chatTemplateMinimumTokens + maxTokens - 1, minimum + chatTemplateMinimumTokens + maxTokens} {
						pinned := int64(length)
						limits := config.Limits{ModelLimits: map[string]config.ModelLimits{"qwen": {MaxModelLen: &pinned}}}

						_, err := live.server.checkContext(limits, normalized)

						if fits := minimum+chatTemplateMinimumTokens+maxTokens <= length; fits != (err == nil) {
							t.Fatalf("%s, %d bytes, max_tokens %d, context %d: minimum %d, check said %v", name, len(text), maxTokens, length, minimum, err)
						}
					}
				}
			}
		}
	}
}

// Test flow:
//  1. Turn the stats tokenizer on, wire a prompt count whose byte bound fits qwen's context, and send a chat request twice from one caller, so the second is a cache hit.
//  2. Start a harness with the stats tokenizer on whose snapshot blocks requests for PoC with the gateway's PoC mode off, and send a chat request.
//  3. Assert the cache miss was tokenized once and the hit not again, and the request admission refused was never tokenized.
func TestACacheHitAndAnAdmissionRefusalTakeNoCount(t *testing.T) {
	live := newHarness(t, enableStatsTokenizer)
	count := &fakePromptTokens{tokens: 9, known: true}
	live.server.promptTokens = count
	for range 2 {
		if recorder := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, callerHeaders("caller-a")); recorder.Code != http.StatusOK {
			t.Fatalf("status = %d (%s), want served", recorder.Code, recorder.Body.String())
		}
	}
	if count.tokenizing.Load() != 1 || live.inference.runs.Load() != 1 {
		t.Fatalf("tokenized %d times over %d races, want once for the miss and not for the hit", count.tokenizing.Load(), live.inference.runs.Load())
	}

	refusing := newHarness(t, enableStatsTokenizer)
	refused := &fakePromptTokens{tokens: 9, known: true}
	refusing.server.promptTokens = refused
	refusing.snapshots.snapshot = chain.PhaseSnapshot{RequestsBlocked: true, BlockReason: chain.BlockReasonPoC, EpochPhase: chain.EpochPhasePoCGenerate}
	refusing.swapConfig(func(next *config.Config) { next.Modes.PoCMode = config.PoCModeOff })
	if recorder := refusing.request(t, http.MethodPost, "/v1/chat/completions", chatBody, nil); recorder.Code != http.StatusServiceUnavailable || refused.tokenizing.Load() != 0 {
		t.Fatalf("status = %d, tokenized %d times, want 503 with no count", recorder.Code, refused.tokenizing.Load())
	}
}

// Test flow:
//  1. Turn the stats tokenizer on, wire a prompt count whose byte bound fits qwen's context, and take every slot of the prompt count gate.
//  2. Send a chat request, then free one slot and send another.
//  3. Assert the first was not tokenized and carried the byte estimate marked bytes_estimate to the limiter and the race, and the second carried the tokenizer's count.
//  4. Assert the full gate was reported once, for the first request's model.
func TestAFullPromptCountGateLeavesTheRequestOnTheByteEstimate(t *testing.T) {
	live := newHarness(t, enableStatsTokenizer)
	count := &fakePromptTokens{tokens: 9, known: true}
	live.server.promptTokens = count
	fallbacks := &spyStatsTokenizerFallbacks{}
	live.server.statsTokenizerFallbacks = fallbacks
	for range cap(live.server.promptCounts) {
		if !live.server.promptCounts.tryAcquire() {
			t.Fatal("the gate refused a slot before it was full")
		}
	}

	if recorder := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, nil); recorder.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), want served", recorder.Code, recorder.Body.String())
	}
	raced := live.inference.raced.Load()
	want := filters.EstimatedPromptTokens(int(raced.InputBytes))
	if count.tokenizing.Load() != 0 || raced.InputTokens != want || raced.InputTokensSource != filters.InputTokensBytesEstimate || live.limiter.acquired.Load() != int64(want) {
		t.Fatalf("tokenized %d times, race got %d tokens from %q, limiter %d, want the byte estimate %d untokenized", count.tokenizing.Load(), raced.InputTokens, raced.InputTokensSource, live.limiter.acquired.Load(), want)
	}

	live.server.promptCounts.release()
	if recorder := live.request(t, http.MethodPost, "/v1/chat/completions", chatBodyWith("max_tokens", 100), nil); recorder.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), want served", recorder.Code, recorder.Body.String())
	}
	if raced := live.inference.raced.Load(); count.tokenizing.Load() != 1 || raced.InputTokens != 9 || raced.InputTokensSource != filters.InputTokensTokenizerExact {
		t.Fatalf("tokenized %d times, race got %d tokens from %q, want the tokenizer's 9 once a slot is free", count.tokenizing.Load(), raced.InputTokens, raced.InputTokensSource)
	}
	if !slices.Equal(fallbacks.models, []string{"qwen"}) {
		t.Fatalf("full gate reported for %v, want once for qwen", fallbacks.models)
	}
}

// Test flow:
//  1. Leave the stats tokenizer off and wire a prompt count for a model without a context length, one whose byte bound fits qwen's pinned 20 000 tokens, and one whose count fits under a byte bound past it.
//  2. Send a chat request with each.
//  3. Assert the first was never read for the tokenizer, the second never tokenized, both carrying the byte estimate marked bytes_estimate, and the third tokenized once by the context check, without the piece cap, carrying the tokenizer's count.
func TestWithTheStatsTokenizerOffOnlyAPromptThatMayNotFitIsTokenized(t *testing.T) {
	fits := uint64(20_000) - filters.DefaultRequestMaxTokens - chatTemplateMinimumTokens
	for _, testCase := range []struct {
		name             string
		tune             func(*config.Config)
		slack            uint64
		readForTokenizer bool
		tokenizing       int64
		source           filters.InputTokensSource
	}{
		{"model without a context length", func(*config.Config) {}, 0, false, 0, filters.InputTokensBytesEstimate},
		{"byte bound fits", pinContextLength(20_000), 0, true, 0, filters.InputTokensBytesEstimate},
		{"byte bound past the context", pinContextLength(20_000), 1, true, 1, filters.InputTokensTokenizerExact},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			live := newHarness(t, testCase.tune)
			count := &fakePromptTokens{tokens: fits, slack: testCase.slack, known: true}
			live.server.promptTokens = count

			if recorder := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, nil); recorder.Code != http.StatusOK {
				t.Fatalf("status = %d (%s), want served", recorder.Code, recorder.Body.String())
			}
			raced := live.inference.raced.Load()
			want := filters.EstimatedPromptTokens(int(raced.InputBytes))
			if testCase.source.Tokenized() {
				want = fits
			}
			if (count.model != "") != testCase.readForTokenizer || count.tokenizing.Load() != testCase.tokenizing || count.cappedCounts.Load() != 0 {
				t.Fatalf("read %v, tokenized %d times, %d with the piece cap, want read %v, tokenized %d times, none with the cap", count.model != "", count.tokenizing.Load(), count.cappedCounts.Load(), testCase.readForTokenizer, testCase.tokenizing)
			}
			if raced.InputTokens != want || raced.InputTokensSource != testCase.source || live.limiter.acquired.Load() != int64(want) {
				t.Fatalf("race got %d tokens from %q, limiter %d, want %d from %q", raced.InputTokens, raced.InputTokensSource, live.limiter.acquired.Load(), want, testCase.source)
			}
		})
	}
}

// Test flow:
//  1. Wire a tokenizer that fails, for a prompt whose byte bound passes qwen's pinned context, with the stats tokenizer off and on, and for one whose bound fits it, with the stats tokenizer on.
//  2. Send a chat request with each.
//  3. Assert each is served after one tokenizer pass, the context check's or the count after the cache, and carries the byte estimate marked bytes_estimate.
func TestAFailedTokenizerLeavesTheRequestOnTheByteEstimateAtEitherCount(t *testing.T) {
	fits := uint64(20_000) - filters.DefaultRequestMaxTokens - chatTemplateMinimumTokens
	for _, testCase := range []struct {
		name         string
		tune         func(*config.Config)
		slack        uint64
		cappedCounts int64
	}{
		{"context check, stats tokenizer off", func(*config.Config) {}, 1, 0},
		{"context check, stats tokenizer on", enableStatsTokenizer, 1, 0},
		{"count after the cache", enableStatsTokenizer, 0, 1},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			live := newHarness(t, pinContextLength(20_000), testCase.tune)
			count := &fakePromptTokens{tokens: fits, slack: testCase.slack, known: true, countFails: true}
			live.server.promptTokens = count

			if recorder := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, nil); recorder.Code != http.StatusOK {
				t.Fatalf("status = %d (%s), want served", recorder.Code, recorder.Body.String())
			}
			raced := live.inference.raced.Load()
			want := filters.EstimatedPromptTokens(int(raced.InputBytes))
			if count.tokenizing.Load() != 1 || count.cappedCounts.Load() != testCase.cappedCounts {
				t.Fatalf("tokenized %d times, %d with the piece cap, want once, %d with the cap", count.tokenizing.Load(), count.cappedCounts.Load(), testCase.cappedCounts)
			}
			if raced.InputTokens != want || raced.InputTokensSource != filters.InputTokensBytesEstimate || live.limiter.acquired.Load() != int64(want) {
				t.Fatalf("race got %d tokens from %q, limiter %d, want the byte estimate %d", raced.InputTokens, raced.InputTokensSource, live.limiter.acquired.Load(), want)
			}
		})
	}
}
