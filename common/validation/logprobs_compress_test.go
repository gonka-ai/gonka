package validation

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"testing"

	"common/completionapi"
)

// generateLogprobs mimics a serving engine: descending alternatives, unscored sentinels, 200k vocabulary.
func generateLogprobs(source *rand.Rand, positions, topK int, sentinelShare float64) []completionapi.Logprob {
	content := make([]completionapi.Logprob, positions)
	for position := range content {
		alternatives := make([]completionapi.TopLogprobs, topK)
		logprob := -source.Float64() * 3
		for rank := range alternatives {
			value := logprob
			if rank > 0 && source.Float64() < sentinelShare {
				value = -9999
			}
			token := strconv.Itoa(source.IntN(200_051))
			spelled := make([]int, len(token))
			for index := range token {
				spelled[index] = int(token[index])
			}
			alternatives[rank] = completionapi.TopLogprobs{Token: token, Logprob: value, Bytes: spelled}
			logprob -= source.Float64() * 2
		}
		content[position] = completionapi.Logprob{
			Token:       alternatives[0].Token,
			Logprob:     alternatives[0].Logprob,
			Bytes:       []int{65, 66},
			TopLogprobs: alternatives,
		}
	}
	return content
}

func replayWithNoise(source *rand.Rand, content []completionapi.Logprob, noise float64) []completionapi.Logprob {
	replayed := make([]completionapi.Logprob, len(content))
	for position, original := range content {
		alternatives := make([]completionapi.TopLogprobs, len(original.TopLogprobs))
		for rank, alternative := range original.TopLogprobs {
			value := alternative.Logprob
			if value != -9999 {
				value += (source.Float64()*2 - 1) * noise
			}
			alternatives[rank] = completionapi.TopLogprobs{Token: alternative.Token, Logprob: value}
		}
		replayed[position] = completionapi.Logprob{Token: original.Token, TopLogprobs: alternatives}
	}
	return replayed
}

// compressThroughPayload runs content through the path production uses and hands back what a validator
// would read, so these tests exercise the compressor that actually runs rather than a helper beside it.
func compressThroughPayload(t *testing.T, content []completionapi.Logprob) []completionapi.Logprob {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"logprobs": map[string]any{"content": content}}}})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	compressed, err := completionapi.CompressResponsePayload(payload)
	if err != nil {
		t.Fatalf("CompressResponsePayload: %v", err)
	}
	var restored struct {
		Choices []struct {
			Logprobs struct {
				Content []completionapi.Logprob `json:"content"`
			} `json:"logprobs"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(compressed, &restored); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	return restored.Choices[0].Logprobs.Content
}

func similarityOf(t *testing.T, original, replayed []completionapi.Logprob) float64 {
	t.Helper()
	result := CompareLogits(original, replayed, BaseValidationResult{InferenceId: "test"})
	similarity, ok := result.(*SimilarityValidationResult)
	if !ok {
		t.Fatalf("CompareLogits returned %T, want a similarity", result)
	}
	return similarity.Value
}

// Bit-for-bit, not close: a tolerance here is where a real loss would hide.
func TestSlimmingDoesNotMoveTheValidationVerdictAtAll(t *testing.T) {
	t.Parallel()
	for _, sentinelShare := range []float64{0, 0.6} {
		for _, noise := range []float64{0, 1e-4, 1e-2} {
			source := rand.New(rand.NewPCG(42, uint64(sentinelShare*10)))
			content := generateLogprobs(source, 4096, 5, sentinelShare)
			replayed := replayWithNoise(source, content, noise)

			restored := compressThroughPayload(t, content)

			full := similarityOf(t, content, replayed)
			compress := similarityOf(t, restored, replayed)
			if full != compress {
				t.Fatalf("sentinels %.0f%% noise %g: slimming moved similarity %v -> %v", sentinelShare*100, noise, full, compress)
			}
			t.Logf("sentinels %3.0f%% noise %-5g similarity %.9f identical", sentinelShare*100, noise, full)
		}
	}
}

// The enforced-token list the validator pins its replay to must survive slimming exactly.
func TestSlimmingRebuildsTheEnforcedTokensExactly(t *testing.T) {
	t.Parallel()
	source := rand.New(rand.NewPCG(7, 7))
	content := generateLogprobs(source, 512, 5, 0.4)

	restored := compressThroughPayload(t, content)

	for position := range content {
		if restored[position].Token != content[position].Token {
			t.Fatalf("position %d: enforced token %q, want %q", position, restored[position].Token, content[position].Token)
		}
		for rank, want := range content[position].TopLogprobs {
			if restored[position].TopLogprobs[rank].Token != want.Token {
				t.Fatalf("position %d rank %d: enforced alternative %q, want %q",
					position, rank, restored[position].TopLogprobs[rank].Token, want.Token)
			}
		}
	}
}

// The whole flow against a compressed payload: parse, enforce tokens, replay, compare, threshold.
func TestValidationRunsUnchangedAgainstASlimmedPayload(t *testing.T) {
	t.Parallel()
	source := rand.New(rand.NewPCG(11, 13))
	content := generateLogprobs(source, 64, 5, 0.5)

	executorResponse := map[string]any{
		"id": "devshard-60453-7", "object": "chat.completion", "created": 1786458557, "model": "m",
		"choices": []any{map[string]any{
			"index": 0, "finish_reason": "length",
			"message":  map[string]any{"role": "assistant", "content": "Hi"},
			"logprobs": map[string]any{"content": content},
		}},
		"usage": map[string]any{"prompt_tokens": 7, "completion_tokens": len(content), "total_tokens": 7 + len(content)},
	}
	whole, err := json.Marshal(executorResponse)
	if err != nil {
		t.Fatal(err)
	}
	compressed, err := completionapi.CompressResponsePayload(whole)
	if err != nil {
		t.Fatalf("CompressResponsePayload: %v", err)
	}
	if len(compressed) >= len(whole) {
		t.Fatalf("slimming did not shrink the payload: %d -> %d", len(whole), len(compressed))
	}

	prompt := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	var replayedRequest map[string]any
	result, err := ExecuteValidation(context.Background(), "devshard-60453-7", prompt, compressed,
		func(_ context.Context, body []byte) (*http.Response, error) {
			if err := json.Unmarshal(body, &replayedRequest); err != nil {
				return nil, err
			}
			replay, err := json.Marshal(executorResponse)
			if err != nil {
				return nil, err
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewReader(replay)),
			}, nil
		}, 7, uint64(len(content)), "")
	if err != nil {
		t.Fatalf("ExecuteValidation: %v", err)
	}

	similarity, ok := result.(*SimilarityValidationResult)
	if !ok {
		t.Fatalf("validation returned %T (%v), want a similarity", result, result)
	}
	if !SimilarityPassesThreshold(similarity.Value, LegacySimilarityThreshold) {
		t.Fatalf("an honest replay of a compressed payload scored %v, below the %v bar", similarity.Value, LegacySimilarityThreshold)
	}

	enforced, present := replayedRequest["enforced_tokens"]
	if !present {
		t.Fatal("the replay carried no enforced tokens, so it was never pinned to the executor's path")
	}
	encoded, err := json.Marshal(enforced)
	if err != nil {
		t.Fatal(err)
	}
	var tokens completionapi.EnforcedTokens
	if err := json.Unmarshal(encoded, &tokens); err != nil {
		t.Fatal(err)
	}
	if len(tokens.Tokens) != len(content) {
		t.Fatalf("the replay pinned %d positions, want %d", len(tokens.Tokens), len(content))
	}
	if HasNonNumericTokens(tokens) {
		t.Fatal("the compressed payload produced enforced tokens a validator would reject as decoded text")
	}
	for position, want := range content {
		if tokens.Tokens[position].Token != want.Token {
			t.Fatalf("position %d: pinned token %q, want %q", position, tokens.Tokens[position].Token, want.Token)
		}
	}
}
