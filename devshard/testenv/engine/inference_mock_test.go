package engine_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard"
	"devshard/testenv/engine"
)

// TestNewMockInference_DefaultsApplied asserts that a zero-value cfg
// is filled with the pinned 80/40 token defaults. Protocol-level
// scenarios depend on these numbers.
func TestNewMockInference_DefaultsApplied(t *testing.T) {
	e := engine.NewMockInference(engine.MockInferenceConfig{})
	got := e.Config()
	require.EqualValues(t, 80, got.InputTokens)
	require.EqualValues(t, 40, got.OutputTokens)
	require.Zero(t, got.Latency)
}

// TestNewMockInference_ExplicitOverridesKept asserts that explicit
// fields survive the default merge (they are not clobbered).
func TestNewMockInference_ExplicitOverridesKept(t *testing.T) {
	e := engine.NewMockInference(engine.MockInferenceConfig{
		Latency:      5 * time.Millisecond,
		InputTokens:  7,
		OutputTokens: 11,
	})
	got := e.Config()
	require.Equal(t, 5*time.Millisecond, got.Latency)
	require.EqualValues(t, 7, got.InputTokens)
	require.EqualValues(t, 11, got.OutputTokens)
}

// TestExecute_DeterministicPerTriple asserts that the same
// (inference_id, escrow, model) triple always produces the same hash
// — the property protocol scenarios rely on when checking "second
// verifier saw the same response".
func TestExecute_DeterministicPerTriple(t *testing.T) {
	e := engine.NewMockInference(engine.MockInferenceConfig{})
	req := devshard.ExecuteRequest{
		InferenceID: 42,
		Model:       "llama",
		EscrowID:    "esc-1",
	}
	r1, err := e.Execute(context.Background(), req)
	require.NoError(t, err)
	r2, err := e.Execute(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, r1.ResponseHash, r2.ResponseHash)
	require.Equal(t, r1.ResponseBody, r2.ResponseBody)

	want := sha256.Sum256(r1.ResponseBody)
	require.Equal(t, want[:], r1.ResponseHash,
		"ResponseHash must be sha256(ResponseBody) so downstream hash checks pass")
}

// TestExecute_DistinctTriplesDifferentHashes asserts that changing
// any one of inference_id/escrow/model changes the hash. Without
// this, protocol tests cannot distinguish inferences.
func TestExecute_DistinctTriplesDifferentHashes(t *testing.T) {
	e := engine.NewMockInference(engine.MockInferenceConfig{})
	ctx := context.Background()

	base := devshard.ExecuteRequest{InferenceID: 1, Model: "m", EscrowID: "e"}

	r0, err := e.Execute(ctx, base)
	require.NoError(t, err)

	cases := []devshard.ExecuteRequest{
		{InferenceID: 2, Model: "m", EscrowID: "e"},
		{InferenceID: 1, Model: "m2", EscrowID: "e"},
		{InferenceID: 1, Model: "m", EscrowID: "e2"},
	}
	for i, c := range cases {
		r, err := e.Execute(ctx, c)
		require.NoError(t, err)
		require.NotEqual(t, r0.ResponseHash, r.ResponseHash,
			"case %d: response hash must differ when a triple field changes", i)
	}
}

// TestExecute_HonorsLatency confirms the stub actually sleeps for the
// configured latency. Uses a tight lower bound + generous upper bound
// to keep the test stable on slow CI.
func TestExecute_HonorsLatency(t *testing.T) {
	const want = 30 * time.Millisecond
	e := engine.NewMockInference(engine.MockInferenceConfig{Latency: want})

	start := time.Now()
	_, err := e.Execute(context.Background(), devshard.ExecuteRequest{InferenceID: 1})
	require.NoError(t, err)
	elapsed := time.Since(start)

	require.GreaterOrEqual(t, elapsed, want,
		"Execute returned before Latency elapsed")
	require.Less(t, elapsed, want+500*time.Millisecond,
		"Execute slept far longer than Latency — likely buggy sleep loop")
}

// TestExecute_CancelDuringLatency asserts that cancelling ctx during
// the Latency sleep returns ctx.Err() promptly — protocol scenarios
// cancelling a verifier mid-flight must not block waiting for the
// full latency.
func TestExecute_CancelDuringLatency(t *testing.T) {
	e := engine.NewMockInference(engine.MockInferenceConfig{Latency: time.Second})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := e.Execute(ctx, devshard.ExecuteRequest{InferenceID: 1})
	elapsed := time.Since(start)

	require.Error(t, err)
	require.True(t, errors.Is(err, context.Canceled),
		"expected context.Canceled, got %v", err)
	require.Less(t, elapsed, 500*time.Millisecond,
		"Execute did not return promptly after ctx cancel (slept %s)", elapsed)
}

// TestExecute_StreamingWritesSSE asserts that the SSE frames match
// the subnet-testenv wire shape when a ResponseWriter is supplied.
// Downstream scenario fixtures depend on the exact `data: ...\n\n`
// framing.
func TestExecute_StreamingWritesSSE(t *testing.T) {
	e := engine.NewMockInference(engine.MockInferenceConfig{})
	rec := httptest.NewRecorder()

	_, err := e.Execute(context.Background(), devshard.ExecuteRequest{
		InferenceID:    7,
		Model:          "m",
		EscrowID:       "esc",
		ResponseWriter: rec,
	})
	require.NoError(t, err)

	body := rec.Body.String()
	require.Contains(t, body, "data: ")
	require.Contains(t, body, "[DONE]")
	require.True(t, strings.HasSuffix(strings.TrimSpace(body), "data: [DONE]"),
		"SSE stream did not terminate with [DONE]: %q", body)
}

// TestExecute_NoStreamingWithoutResponseWriter asserts that omitting
// ResponseWriter produces no side-effects (the writer is nil-safe).
func TestExecute_NoStreamingWithoutResponseWriter(t *testing.T) {
	e := engine.NewMockInference(engine.MockInferenceConfig{})
	_, err := e.Execute(context.Background(), devshard.ExecuteRequest{
		InferenceID: 1,
	})
	require.NoError(t, err)
}

// TestNewMockInferenceFromEnv_ReadsKnownVars asserts that the curated
// env-var set is applied and that unrecognized / malformed values
// fall back to defaults without panicking.
func TestNewMockInferenceFromEnv_ReadsKnownVars(t *testing.T) {
	t.Setenv("TESTENV_INFERENCE_LATENCY_MS", "12")
	t.Setenv("TESTENV_INFERENCE_INPUT_TOKENS", "9")
	t.Setenv("TESTENV_INFERENCE_OUTPUT_TOKENS", "13")

	e := engine.NewMockInferenceFromEnv()
	got := e.Config()
	require.Equal(t, 12*time.Millisecond, got.Latency)
	require.EqualValues(t, 9, got.InputTokens)
	require.EqualValues(t, 13, got.OutputTokens)
}

// TestNewMockInferenceFromEnv_MalformedValuesFallBack asserts the
// stub never fails to construct just because an env var is wrong.
func TestNewMockInferenceFromEnv_MalformedValuesFallBack(t *testing.T) {
	t.Setenv("TESTENV_INFERENCE_LATENCY_MS", "not-a-number")
	t.Setenv("TESTENV_INFERENCE_INPUT_TOKENS", "banana")
	// Negative latency is treated as malformed (ms < 0).
	t.Setenv("TESTENV_INFERENCE_OUTPUT_TOKENS", "-1")

	e := engine.NewMockInferenceFromEnv()
	got := e.Config()
	require.Zero(t, got.Latency)
	require.EqualValues(t, 80, got.InputTokens)
	require.EqualValues(t, 40, got.OutputTokens)
}

// TestExecute_ConcurrentSafe runs many goroutines against one engine
// to catch races. The engine holds no per-call state so this is a
// cheap guarantee; with -race this catches accidental locking on
// mutable fields.
func TestExecute_ConcurrentSafe(t *testing.T) {
	e := engine.NewMockInference(engine.MockInferenceConfig{})
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(id uint64) {
			defer wg.Done()
			_, err := e.Execute(ctx, devshard.ExecuteRequest{InferenceID: id})
			require.NoError(t, err)
		}(uint64(i))
	}
	wg.Wait()
}
