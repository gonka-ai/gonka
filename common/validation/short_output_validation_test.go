package validation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"testing"

	"common/completionapi"

	"github.com/stretchr/testify/require"
)

// Live per-model pass bars: MiniMax-M2.7 0.922; Qwen 0.94 is the strictest.
const (
	miniMaxThreshold = 0.922
	qwenThreshold    = 0.94
)

// realPair is one MiniMax-M2.7 fp8 completion (112 output positions) as the executor stored it
// and as a cross-GPU fp8 validator recomputed it with the tokens forced.
type realPair struct {
	Name      string                  `json:"name"`
	Executor  []completionapi.Logprob `json:"executor"`
	Validator []completionapi.Logprob `json:"validator"`
}

func loadRealPairs(t *testing.T) []realPair {
	t.Helper()
	raw, err := os.ReadFile("testdata/minimax_m27_honest_pairs.json")
	require.NoError(t, err)
	var pairs []realPair
	require.NoError(t, json.Unmarshal(raw, &pairs))
	require.Len(t, pairs, 3)
	return pairs
}

// garbageExecutor is a lazy proof: the forced tokens match, the claimed top_logprobs were never
// computed by the model (the chosen token at ~1.0, junk alternatives).
func garbageExecutor(validation []completionapi.Logprob) []completionapi.Logprob {
	out := make([]completionapi.Logprob, len(validation))
	for i, position := range validation {
		top := []completionapi.TopLogprobs{{Token: position.Token, Logprob: -0.0001}}
		for j := 1; j < len(position.TopLogprobs); j++ {
			top = append(top, completionapi.TopLogprobs{Token: fmt.Sprintf("junk_%d_%d", i, j), Logprob: -0.5})
		}
		out[i] = completionapi.Logprob{Token: position.Token, Logprob: -0.0001, TopLogprobs: top}
	}
	return out
}

// farthestExecutor claims, at every position, logprobs as far from the validator's as the metric
// allows: each term |v-o|/(|v|+|o|)/2 approaches its 0.5 supremum.
func farthestExecutor(validation []completionapi.Logprob) []completionapi.Logprob {
	out := make([]completionapi.Logprob, len(validation))
	for i, position := range validation {
		top := make([]completionapi.TopLogprobs, len(position.TopLogprobs))
		for j, entry := range position.TopLogprobs {
			claimed := -1e-9
			if entry.Logprob > -1e-3 {
				claimed = -1e4
			}
			top[j] = completionapi.TopLogprobs{Token: entry.Token, Logprob: claimed}
		}
		out[i] = completionapi.Logprob{Token: position.Token, TopLogprobs: top}
	}
	return out
}

// repeatedToken is one side of a degenerate run: the same token K times at the given probability.
func repeatedToken(tokenID string, probabilities []float64) []completionapi.Logprob {
	out := make([]completionapi.Logprob, len(probabilities))
	for i, probability := range probabilities {
		top := []completionapi.TopLogprobs{{Token: tokenID, Logprob: math.Log(probability)}}
		for j := 1; j < completionapi.ForcedTopLogprobs; j++ {
			top = append(top, completionapi.TopLogprobs{Token: fmt.Sprintf("filler_%d", j), Logprob: -13})
		}
		out[i] = completionapi.Logprob{Token: tokenID, Logprob: math.Log(probability), TopLogprobs: top}
	}
	return out
}

func constant(value float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = value
	}
	return out
}

var testBase = BaseValidationResult{InferenceId: "t", ResponseBytes: []byte("t")}

func scoreLegacy(original, validation []completionapi.Logprob) ValidationResult {
	return CompareLogits(original, validation, testBase)
}

func scoreShortOutput(original, validation []completionapi.Logprob) ValidationResult {
	return CompareLogitsWithPolicy(original, validation, testBase, DefaultShortOutputScoringPolicy, "processed")
}

func passes(result ValidationResult, threshold float64) bool {
	similarity, isSimilarity := result.(*SimilarityValidationResult)
	return isSimilarity && SimilarityPassesThreshold(similarity.Value, threshold)
}

// (i) Honest real completions truncated to every length 1..112 pass the new rule.
func TestShortOutputHonestPairsPassAtEveryLength(t *testing.T) {
	for _, pair := range loadRealPairs(t) {
		for k := 1; k <= len(pair.Executor); k++ {
			result := scoreShortOutput(pair.Executor[:k], pair.Validator[:k])
			require.Truef(t, passes(result, miniMaxThreshold), "%s K=%d honest must pass, got %#v", pair.Name, k, result)
		}
	}
}

// (ii) The lazy-proof construction fails at every length, where the legacy divisor lets it through
// at short lengths. With raw logprobs the per-position ceiling check rejects it outright; processed
// logprobs leave that check a floor, and the mean-distance check fails those.
func TestShortOutputGarbageFailsAtEveryLength(t *testing.T) {
	legacyPassedAt := map[string]int{}
	for _, pair := range loadRealPairs(t) {
		garbage := garbageExecutor(pair.Validator)
		distances, err := scorePositions(garbage, pair.Validator)
		require.NoError(t, err)
		mean := 0.0
		for _, distance := range distances {
			mean += distance / float64(len(distances))
		}
		require.InDelta(t, 0.42, mean, 0.02, "garbage construction drifted from ~0.42 per position")

		for k := 1; k <= len(pair.Validator); k++ {
			if passes(scoreLegacy(garbage[:k], pair.Validator[:k]), miniMaxThreshold) {
				legacyPassedAt[pair.Name] = k
			}
			result := scoreShortOutput(garbage[:k], pair.Validator[:k])
			require.Falsef(t, passes(result, miniMaxThreshold), "%s K=%d garbage must fail, got %#v", pair.Name, k, result)
			raw := CompareLogitsWithPolicy(garbage[:k], pair.Validator[:k], testBase, DefaultShortOutputScoringPolicy, "raw_logprobs")
			invalid, isInvalid := raw.(*InvalidInferenceResult)
			require.Truef(t, isInvalid, "%s K=%d garbage must fail the per-position ceiling check, got %#v", pair.Name, k, raw)
			require.Equal(t, "Output positions diverge from the validator.", invalid.Reason)
		}
	}
	// The hole being closed: under max(100, N) the same garbage passed at short lengths.
	for _, pair := range loadRealPairs(t) {
		require.GreaterOrEqualf(t, legacyPassedAt[pair.Name], 15, "%s: legacy rule no longer shows the dilution", pair.Name)
	}
}

// (iii) Degenerate repetition is scored as a true mean over its own positions, never padded with
// the legacy divisor's phantom slots.
func TestShortOutputRepetitionScoredOnTrueMean(t *testing.T) {
	trap := constant(0.99995, 64)
	trap[0] = 0.974
	run := repeatedToken("167676", trap)
	claimed := repeatedToken("167676", constant(0.99995, 64))

	distances, err := scorePositions(claimed, run)
	require.NoError(t, err)
	sum := 0.0
	for _, distance := range distances {
		sum += distance
	}
	policy := DefaultShortOutputScoringPolicy
	short := scoreShortOutput(claimed, run).(*SimilarityValidationResult)
	require.InDelta(t, 1-max(sum/64-policy.MeanAllowance/8, sum/100), short.Value, 1e-12)
}

// (iv) The phantom-slot hole: a 12-position output whose claimed logprobs are as wrong as the
// metric can register clears even the strictest threshold under max(100, N), and fails now.
func TestShortOutputClosesPhantomSlotDilution(t *testing.T) {
	pair := loadRealPairs(t)[1]
	validation := pair.Validator[:12]
	fabricated := farthestExecutor(validation)

	legacy, isSimilarity := scoreLegacy(fabricated, validation).(*SimilarityValidationResult)
	require.True(t, isSimilarity)
	require.Greater(t, legacy.Value, qwenThreshold, "legacy rule passes arbitrary logprobs at N=12")

	require.False(t, passes(scoreShortOutput(fabricated, validation), miniMaxThreshold))
	require.False(t, passes(scoreShortOutput(fabricated, validation), 0))
}

// sharpened is a substitute model's proof: every claimed logprob scaled by 1.5, so each position
// sits well above honest noise and below the per-position ceiling.
func sharpened(validation []completionapi.Logprob) []completionapi.Logprob {
	out := make([]completionapi.Logprob, len(validation))
	for i, position := range validation {
		top := make([]completionapi.TopLogprobs, len(position.TopLogprobs))
		for j, entry := range position.TopLogprobs {
			top[j] = completionapi.TopLogprobs{Token: entry.Token, Logprob: entry.Logprob * 1.5}
		}
		out[i] = completionapi.Logprob{Token: position.Token, TopLogprobs: top}
	}
	return out
}

// The mean-distance check on a full output is a true mean over every position: the honest pair
// passes, the substitute fails.
func TestShortOutputMeanDistanceUsesTrueMean(t *testing.T) {
	pair := loadRealPairs(t)[1]
	require.True(t, passes(scoreShortOutput(pair.Executor, pair.Validator), miniMaxThreshold))

	substitute := scoreShortOutput(sharpened(pair.Validator), pair.Validator)
	require.IsType(t, &SimilarityValidationResult{}, substitute, "below the per-position ceiling")
	require.False(t, passes(substitute, miniMaxThreshold))
}

// A 60-token output is scored on its own 60 positions, not diluted over 100 slots: a moderate
// substitute clears the strictest bar under the legacy divisor and fails it under the short-output rule.
func TestShortOutputSubstituteNotDiluted(t *testing.T) {
	pair := loadRealPairs(t)[1]
	substitute := sharpened(pair.Validator[:60])
	require.True(t, passes(scoreLegacy(substitute, pair.Validator[:60]), qwenThreshold))
	result := scoreShortOutput(substitute, pair.Validator[:60])
	require.IsType(t, &SimilarityValidationResult{}, result, "below the per-position ceiling")
	require.False(t, passes(result, qwenThreshold))
	require.True(t, passes(scoreShortOutput(pair.Executor[:60], pair.Validator[:60]), qwenThreshold), "the honest output passes the same bar")
}

// The sampler-support budget grows with the output: an honest executor's sampler can emit a token
// the validator's processed sampler clamps to -9999.
func TestShortOutputSamplerSupportBudgetScalesWithLength(t *testing.T) {
	pair := loadRealPairs(t)[2]
	validation := append([]completionapi.Logprob(nil), pair.Validator...)
	for _, i := range []int{10, 40, 70, 100} {
		validation[i].Logprob = outOfSupportLogprob
	}
	require.True(t, passes(scoreShortOutput(pair.Executor, validation), miniMaxThreshold), "4 of 112 is within ceil(3%)")

	validation[105].Logprob = outOfSupportLogprob
	result := scoreShortOutput(pair.Executor, validation)
	require.IsType(t, &InvalidInferenceResult{}, result)
	require.Equal(t, "Output tokens outside the sampler support.", result.(*InvalidInferenceResult).Reason)
}

// The sampler-support check rejects enforced tokens the validator's processed sampler gives no
// probability to.
func TestShortOutputSamplerSupportRejectsOutOfSupportTokens(t *testing.T) {
	pair := loadRealPairs(t)[0]
	validation := append([]completionapi.Logprob(nil), pair.Validator[:8]...)
	validation[2].Logprob = outOfSupportLogprob
	require.True(t, passes(scoreShortOutput(pair.Executor[:8], validation), miniMaxThreshold), "one boundary position is tolerated")

	validation[5].Logprob = math.Inf(-1)
	result := scoreShortOutput(pair.Executor[:8], validation)
	require.IsType(t, &InvalidInferenceResult{}, result)
	require.Equal(t, "Output tokens outside the sampler support.", result.(*InvalidInferenceResult).Reason)

	raw := CompareLogitsWithPolicy(pair.Executor[:8], validation, testBase, DefaultShortOutputScoringPolicy, "raw_logprobs")
	require.True(t, passes(raw, miniMaxThreshold), "the sampler-support check only reads processed logprobs")
}

// The per-position ceiling check tolerates a small fraction of outlying positions on a long output
// and, with raw logprobs, none on a short one. Processed logprobs keep a floor.
func TestShortOutputCeilingCheckTolerance(t *testing.T) {
	pair := loadRealPairs(t)[2]
	executor := append([]completionapi.Logprob(nil), pair.Executor...)
	executor[100] = garbageExecutor(pair.Validator[100:101])[0]
	require.True(t, passes(scoreShortOutput(executor, pair.Validator), miniMaxThreshold), "1 of 112 is within 2%")

	short := append([]completionapi.Logprob(nil), pair.Executor[:40]...)
	short[10] = garbageExecutor(pair.Validator[10:11])[0]
	raw := CompareLogitsWithPolicy(short, pair.Validator[:40], testBase, DefaultShortOutputScoringPolicy, "raw_logprobs")
	require.IsType(t, &InvalidInferenceResult{}, raw)
	require.IsType(t, &SimilarityValidationResult{}, scoreShortOutput(short, pair.Validator[:40]), "within the processed floor")

	for _, i := range []int{15, 20, 25} {
		short[i] = garbageExecutor(pair.Validator[i : i+1])[0]
	}
	require.IsType(t, &InvalidInferenceResult{}, scoreShortOutput(short, pair.Validator[:40]))
}

func storedCompletion(t *testing.T, positions []completionapi.Logprob) []byte {
	t.Helper()
	response := map[string]any{
		"id":      "test",
		"object":  "chat.completion",
		"choices": []map[string]any{{"index": 0, "finish_reason": "stop", "logprobs": map[string]any{"content": positions}}},
	}
	body, err := json.Marshal(response)
	require.NoError(t, err)
	return body
}

type capturedReplay struct {
	body map[string]any
}

func (c *capturedReplay) executor(t *testing.T, response []byte) func(context.Context, []byte) (*http.Response, error) {
	return func(_ context.Context, body []byte) (*http.Response, error) {
		require.NoError(t, json.Unmarshal(body, &c.body))
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(response))}, nil
	}
}

func validate(t *testing.T, prompt, stored, replay []byte, vocabularySize int, policy ScoringPolicy) (ValidationResult, map[string]any) {
	t.Helper()
	var captured capturedReplay
	result, err := ExecuteValidationWithPolicy(context.Background(), "7", prompt, stored, captured.executor(t, replay), 0, 0, "processed", vocabularySize, policy)
	require.NoError(t, err)
	return result, captured.body
}

// (v) A short honest output is not failed for being under 64 tokens, and the replay is not forced
// to 64.
func TestExecuteValidationShortOutputNoFloor(t *testing.T) {
	pair := loadRealPairs(t)[0]
	prompt := []byte(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}],"max_tokens":16}`)
	stored := storedCompletion(t, pair.Executor[:8])
	replay := storedCompletion(t, pair.Validator[:8])

	result, replayed := validate(t, prompt, stored, replay, 0, DefaultShortOutputScoringPolicy)
	require.True(t, passes(result, miniMaxThreshold), "%#v", result)
	require.NotContains(t, replayed, "min_tokens")
	require.EqualValues(t, 16, replayed["max_tokens"])
	require.NotContains(t, replayed, "prompt_logprobs")
}

// Only a min_tokens the caller asked for binds the output length.
func TestExecuteValidationShortOutputHonorsCallerMinTokens(t *testing.T) {
	pair := loadRealPairs(t)[0]
	prompt := []byte(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}],"max_tokens":32,"min_tokens":16}`)
	result, replayed := validate(t, prompt, storedCompletion(t, pair.Executor[:8]), storedCompletion(t, pair.Validator[:8]), 0, DefaultShortOutputScoringPolicy)
	require.Equal(t, "Output shorter than the requested min_tokens.", result.(*InvalidInferenceResult).Reason)
	require.EqualValues(t, 16, replayed["min_tokens"])
}

// The replay carries in-vocab stop ids; an output that runs past one is rejected; ids outside a
// known vocabulary are rejected without replay; an unknown vocabulary drops them from the replay.
func TestExecuteValidationShortOutputStopTokenIDs(t *testing.T) {
	pair := loadRealPairs(t)[0]
	stop := pair.Executor[3].Token
	prompt := []byte(fmt.Sprintf(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}],"max_tokens":16,"stop_token_ids":[%s]}`, stop))

	result, replayed := validate(t, prompt, storedCompletion(t, pair.Executor[:4]), storedCompletion(t, pair.Validator[:4]), 200064, DefaultShortOutputScoringPolicy)
	require.True(t, passes(result, miniMaxThreshold), "stop id on the final position: %#v", result)
	require.Equal(t, []any{json.Number(stop)}, jsonNumbers(t, replayed["stop_token_ids"]))

	result, _ = validate(t, prompt, storedCompletion(t, pair.Executor[:8]), storedCompletion(t, pair.Validator[:8]), 200064, DefaultShortOutputScoringPolicy)
	require.Equal(t, "Output continues past a requested stop token.", result.(*InvalidInferenceResult).Reason)

	outOfRange := []byte(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}],"max_tokens":16,"stop_token_ids":[200064]}`)
	var replays int
	_, err := ExecuteValidationWithPolicy(context.Background(), "7", outOfRange, storedCompletion(t, pair.Executor[:4]),
		func(context.Context, []byte) (*http.Response, error) {
			replays++
			return nil, fmt.Errorf("must not replay")
		},
		0, 0, "processed", 200064, DefaultShortOutputScoringPolicy)
	require.NoError(t, err)
	require.Zero(t, replays)

	_, replayed = validate(t, outOfRange, storedCompletion(t, pair.Executor[:4]), storedCompletion(t, pair.Validator[:4]), 0, DefaultShortOutputScoringPolicy)
	require.NotContains(t, replayed, "stop_token_ids", "unknown vocab: never send unchecked ids to the node")
}

func jsonNumbers(t *testing.T, value any) []any {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var out []any
	require.NoError(t, decoder.Decode(&out))
	return out
}

// The mean-distance check is the true mean over every output position minus MeanAllowance/sqrt(N),
// never below the legacy sum/max(100, N): on N >= 100 it is the legacy score.
func TestShortOutputMeanDistanceIsAllPositionMean(t *testing.T) {
	policy := DefaultShortOutputScoringPolicy
	for _, pair := range loadRealPairs(t) {
		for _, k := range []int{1, 7, 40, len(pair.Executor)} {
			sum := 0.0
			for i := 0; i < k; i++ {
				distance, err := positionDistance(pair.Executor[i].TopLogprobs, pair.Validator[i].TopLogprobs)
				require.NoError(t, err)
				sum += distance
			}
			want := 1 - max(sum/float64(k)-policy.MeanAllowance/math.Sqrt(float64(k)), sum/max(float64(k), 100))
			result := scoreShortOutput(pair.Executor[:k], pair.Validator[:k])
			require.IsType(t, &SimilarityValidationResult{}, result)
			require.InDeltaf(t, want, result.(*SimilarityValidationResult).Value, 1e-12, "%s K=%d", pair.Name, k)
		}
		legacy := scoreLegacy(pair.Executor, pair.Validator).(*SimilarityValidationResult).Value
		short := scoreShortOutput(pair.Executor, pair.Validator).(*SimilarityValidationResult).Value
		require.InDeltaf(t, legacy, short, 1e-12, "%s: N >= 100 is the legacy score", pair.Name)
	}
	require.Equal(t, 0.10, policy.MeanAllowance)
	require.Equal(t, 0.02, policy.OutputCeilingTolerance)
}

// The per-position ceiling check allows 2% of output positions above the ceiling: 2 of 112 pass
// with raw logprobs, 3 fail.
func TestShortOutputCeilingCheckTwoPercentTolerance(t *testing.T) {
	pair := loadRealPairs(t)[2]
	executor := append([]completionapi.Logprob(nil), pair.Executor...)
	for _, i := range []int{30, 100} {
		executor[i] = garbageExecutor(pair.Validator[i : i+1])[0]
	}
	raw := CompareLogitsWithPolicy(executor, pair.Validator, testBase, DefaultShortOutputScoringPolicy, "raw_logprobs")
	require.IsType(t, &SimilarityValidationResult{}, raw, "2 of 112 is within 2%")

	executor[60] = garbageExecutor(pair.Validator[60:61])[0]
	raw = CompareLogitsWithPolicy(executor, pair.Validator, testBase, DefaultShortOutputScoringPolicy, "raw_logprobs")
	require.IsType(t, &InvalidInferenceResult{}, raw)
}

// A non-streamed request is scored on its output positions and replayed without prompt logprobs.
func TestExecuteValidationShortOutputNonStreamed(t *testing.T) {
	pair := loadRealPairs(t)[0]
	prompt := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"max_tokens":16}`)
	result, replayed := validate(t, prompt, storedCompletion(t, pair.Executor[:4]), storedCompletion(t, pair.Validator[:4]), 0, DefaultShortOutputScoringPolicy)
	require.True(t, passes(result, miniMaxThreshold), "%#v", result)
	require.NotContains(t, replayed, "prompt_logprobs")
}

// The per-position ceiling and sampler-support checks reject only on the enforced models; on every
// other model they log what they would reject and the mean-distance check alone decides.
func TestShortOutputChecksLogOnlyPerModel(t *testing.T) {
	pair := loadRealPairs(t)[2]
	enforced := DefaultShortOutputScoringPolicy.ForModel("deepseek-ai/DeepSeek-V4-Flash-0731")
	require.False(t, enforced.OutputChecksLogOnly)
	require.False(t, DefaultShortOutputScoringPolicy.ForModel("zai-org/GLM-5.3-Flash").OutputChecksLogOnly)
	for _, model := range []string{"MiniMaxAI/MiniMax-M2.7", "moonshotai/Kimi-K2.6", "zai-org/GLM-5.2-FP8", ""} {
		require.Truef(t, DefaultShortOutputScoringPolicy.ForModel(model).OutputChecksLogOnly, "%q has no calibration data", model)
	}
	logOnly := DefaultShortOutputScoringPolicy.ForModel("MiniMaxAI/MiniMax-M2.7")

	// Per-position ceiling check: one garbage position in a 40-position raw output.
	short := append([]completionapi.Logprob(nil), pair.Executor[:40]...)
	short[10] = garbageExecutor(pair.Validator[10:11])[0]
	require.IsType(t, &InvalidInferenceResult{}, CompareLogitsWithPolicy(short, pair.Validator[:40], testBase, enforced, "raw_logprobs"))
	require.True(t, passes(CompareLogitsWithPolicy(short, pair.Validator[:40], testBase, logOnly, "raw_logprobs"), miniMaxThreshold))

	// Sampler-support check: five out-of-support tokens in 112 processed positions.
	validation := append([]completionapi.Logprob(nil), pair.Validator...)
	for _, i := range []int{10, 40, 70, 100, 105} {
		validation[i].Logprob = outOfSupportLogprob
	}
	require.IsType(t, &InvalidInferenceResult{}, CompareLogitsWithPolicy(pair.Executor, validation, testBase, enforced, "processed"))
	require.True(t, passes(CompareLogitsWithPolicy(pair.Executor, validation, testBase, logOnly, "processed"), miniMaxThreshold))

	// Log-only does not let a fabricated output through: the mean-distance check still fails it.
	garbage := garbageExecutor(pair.Validator[:40])
	result := CompareLogitsWithPolicy(garbage, pair.Validator[:40], testBase, logOnly, "raw_logprobs")
	require.IsType(t, &SimilarityValidationResult{}, result)
	require.False(t, passes(result, miniMaxThreshold))
}
