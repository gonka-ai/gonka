package engine_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard"
	"devshard/testenv/engine"
)

// TestNewMockValidation_DefaultsMatchSubnetTestenv asserts that the
// zero-ish default verdict is Valid=true, matching subnet-testenv so
// existing scenarios keep passing.
func TestNewMockValidation_DefaultsMatchSubnetTestenv(t *testing.T) {
	e := engine.NewMockValidation(engine.MockValidationConfig{DefaultValid: true})

	r, err := e.Validate(context.Background(), devshard.ValidateRequest{InferenceID: 1})
	require.NoError(t, err)
	require.True(t, r.Valid)
}

// TestNewMockValidation_RespectsExplicitFalseDefault asserts
// DefaultValid=false is taken verbatim; the engine does not silently
// promote it to true.
func TestNewMockValidation_RespectsExplicitFalseDefault(t *testing.T) {
	e := engine.NewMockValidation(engine.MockValidationConfig{DefaultValid: false})
	r, err := e.Validate(context.Background(), devshard.ValidateRequest{InferenceID: 1})
	require.NoError(t, err)
	require.False(t, r.Valid)
}

// TestSetVerdict_OverrideFlipsResult asserts per-inference verdict
// overrides take precedence over DefaultValid.
func TestSetVerdict_OverrideFlipsResult(t *testing.T) {
	e := engine.NewMockValidation(engine.MockValidationConfig{DefaultValid: true})
	e.SetVerdict(42, false)

	r, err := e.Validate(context.Background(), devshard.ValidateRequest{InferenceID: 42})
	require.NoError(t, err)
	require.False(t, r.Valid)

	// A different inference_id still follows DefaultValid.
	r2, err := e.Validate(context.Background(), devshard.ValidateRequest{InferenceID: 99})
	require.NoError(t, err)
	require.True(t, r2.Valid)
}

// TestClearVerdict_RestoresDefault asserts that clearing an override
// reverts the inference to DefaultValid.
func TestClearVerdict_RestoresDefault(t *testing.T) {
	e := engine.NewMockValidation(engine.MockValidationConfig{DefaultValid: true})
	e.SetVerdict(1, false)
	e.ClearVerdict(1)

	r, err := e.Validate(context.Background(), devshard.ValidateRequest{InferenceID: 1})
	require.NoError(t, err)
	require.True(t, r.Valid)
}

// TestSetError_PrecedesVerdict asserts that an error override wins
// over a verdict override, and that Validate returns a nil *Result
// on error (matching the devshard.ValidationEngine contract).
func TestSetError_PrecedesVerdict(t *testing.T) {
	e := engine.NewMockValidation(engine.MockValidationConfig{DefaultValid: true})
	e.SetVerdict(5, true)
	e.SetError(5, engine.ErrTestenvInjectedFault)

	res, err := e.Validate(context.Background(), devshard.ValidateRequest{InferenceID: 5})
	require.Error(t, err)
	require.True(t, errors.Is(err, engine.ErrTestenvInjectedFault))
	require.Nil(t, res)
}

// TestSetError_NilClearsOverride asserts that SetError(_, nil)
// behaves like ClearError.
func TestSetError_NilClearsOverride(t *testing.T) {
	e := engine.NewMockValidation(engine.MockValidationConfig{DefaultValid: true})
	e.SetError(1, engine.ErrTestenvInjectedFault)
	e.SetError(1, nil)

	r, err := e.Validate(context.Background(), devshard.ValidateRequest{InferenceID: 1})
	require.NoError(t, err)
	require.True(t, r.Valid)
}

// TestReset_ClearsEverything asserts that Reset wipes both verdict
// and error overrides.
func TestReset_ClearsEverything(t *testing.T) {
	e := engine.NewMockValidation(engine.MockValidationConfig{DefaultValid: true})
	e.SetVerdict(1, false)
	e.SetError(2, engine.ErrTestenvInjectedFault)

	e.Reset()

	r1, err := e.Validate(context.Background(), devshard.ValidateRequest{InferenceID: 1})
	require.NoError(t, err)
	require.True(t, r1.Valid)

	r2, err := e.Validate(context.Background(), devshard.ValidateRequest{InferenceID: 2})
	require.NoError(t, err)
	require.True(t, r2.Valid)
}

// TestValidate_HonorsLatency confirms the stub actually sleeps.
func TestValidate_HonorsLatency(t *testing.T) {
	const want = 25 * time.Millisecond
	e := engine.NewMockValidation(engine.MockValidationConfig{
		DefaultValid: true,
		Latency:      want,
	})
	start := time.Now()
	_, err := e.Validate(context.Background(), devshard.ValidateRequest{InferenceID: 1})
	require.NoError(t, err)
	require.GreaterOrEqual(t, time.Since(start), want)
}

// TestValidate_CancelDuringLatency asserts that cancelling ctx during
// the Latency sleep returns ctx.Err() promptly.
func TestValidate_CancelDuringLatency(t *testing.T) {
	e := engine.NewMockValidation(engine.MockValidationConfig{
		DefaultValid: true,
		Latency:      time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := e.Validate(ctx, devshard.ValidateRequest{InferenceID: 1})
	require.Error(t, err)
	require.True(t, errors.Is(err, context.Canceled))
	require.Less(t, time.Since(start), 500*time.Millisecond)
}

// TestNewMockValidationFromEnv_ReadsKnownVars covers the env parsing
// happy path for both recognized verdict strings.
func TestNewMockValidationFromEnv_ReadsKnownVars(t *testing.T) {
	t.Setenv("TESTENV_VALIDATION_VERDICT", "invalid")
	t.Setenv("TESTENV_VALIDATION_LATENCY_MS", "5")

	e := engine.NewMockValidationFromEnv()
	r, err := e.Validate(context.Background(), devshard.ValidateRequest{InferenceID: 1})
	require.NoError(t, err)
	require.False(t, r.Valid, "invalid default must apply")
}

// TestNewMockValidationFromEnv_MalformedValuesFallBack asserts bad
// env input never blocks construction.
func TestNewMockValidationFromEnv_MalformedValuesFallBack(t *testing.T) {
	t.Setenv("TESTENV_VALIDATION_VERDICT", "banana")
	t.Setenv("TESTENV_VALIDATION_LATENCY_MS", "not-a-number")

	e := engine.NewMockValidationFromEnv()
	r, err := e.Validate(context.Background(), devshard.ValidateRequest{InferenceID: 1})
	require.NoError(t, err)
	// Default of MockValidationConfig is Valid=true (defaultValidationCfg).
	require.True(t, r.Valid)
}

// TestValidate_ConcurrentReadersAndWriters exercises the internal
// RWMutex by interleaving SetVerdict / Validate from many goroutines.
// With -race this catches accidentally-unlocked map access.
func TestValidate_ConcurrentReadersAndWriters(t *testing.T) {
	e := engine.NewMockValidation(engine.MockValidationConfig{DefaultValid: true})
	ctx := context.Background()

	const n = 64
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func(id uint64) {
			defer wg.Done()
			e.SetVerdict(id, id%2 == 0)
		}(uint64(i))
		go func(id uint64) {
			defer wg.Done()
			_, err := e.Validate(ctx, devshard.ValidateRequest{InferenceID: id})
			require.NoError(t, err)
		}(uint64(i))
	}
	wg.Wait()
}
