package engine

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"devshard"
	"devshard/logging"
)

// MockValidationConfig tunes the static stub. Zero values produce a
// default of "always Valid=true, no latency, no per-inference
// overrides".
type MockValidationConfig struct {
	// DefaultValid is the verdict returned when no per-inference
	// override is set. True by default.
	DefaultValid bool
	// Latency is the wall-clock sleep before Validate returns.
	// Respects ctx cancellation.
	Latency time.Duration
}

// MockValidationEngine satisfies devshard.ValidationEngine. It is
// safe for concurrent use: the per-inference override maps are
// guarded by an RWMutex so tests can flip verdicts mid-scenario
// without racing the executor's goroutines.
//
// Phase 7a's scope is intentionally narrow:
//   - DefaultValid controls the "happy path" verdict.
//   - SetVerdict(inferenceID, valid) flips an individual inference.
//   - SetError(inferenceID, err) makes Validate return err for that
//     inference (used by scenarios that check transport-level failure
//     handling as opposed to a negative verdict).
//
// Header-driven fault injection (X-Testenv-Inject-Fault) and
// per-node-id profiles are Phase 7b/7c; see
// devshard/docs/testenv-stub-engines.md.
type MockValidationEngine struct {
	defaultValid bool
	latency      time.Duration

	mu        sync.RWMutex
	verdicts  map[uint64]bool  // inferenceID -> verdict override
	injErrors map[uint64]error // inferenceID -> Validate returns this error
}

// defaultValidationCfg pins the initial verdict to Valid=true —
// matching the subnet-testenv MockValidationEngine, so existing
// scenarios keep their expected outcomes.
var defaultValidationCfg = MockValidationConfig{
	DefaultValid: true,
	Latency:      0,
}

// NewMockValidation builds a MockValidationEngine from cfg. Latency
// of 0 is valid (no sleep); DefaultValid is taken verbatim from cfg
// so callers explicitly setting false get exactly that.
func NewMockValidation(cfg MockValidationConfig) *MockValidationEngine {
	return &MockValidationEngine{
		defaultValid: cfg.DefaultValid,
		latency:      cfg.Latency,
		verdicts:     make(map[uint64]bool),
		injErrors:    make(map[uint64]error),
	}
}

// NewMockValidationFromEnv reads a small, curated set of env vars and
// builds a stub. Malformed / unrecognized values log at Error and
// fall back to the default config — the test environment should not
// fail to start just because one env var is wrong.
//
// Recognized vars:
//   - TESTENV_VALIDATION_VERDICT (valid|invalid; default valid)
//   - TESTENV_VALIDATION_LATENCY_MS (int, milliseconds)
func NewMockValidationFromEnv() *MockValidationEngine {
	cfg := defaultValidationCfg
	if v := os.Getenv("TESTENV_VALIDATION_VERDICT"); v != "" {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "valid", "true", "1":
			cfg.DefaultValid = true
		case "invalid", "false", "0":
			cfg.DefaultValid = false
		default:
			logging.Error("testenv/engine: invalid TESTENV_VALIDATION_VERDICT",
				"subsystem", "engine", "value", v)
		}
	}
	if v := os.Getenv("TESTENV_VALIDATION_LATENCY_MS"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms >= 0 {
			cfg.Latency = time.Duration(ms) * time.Millisecond
		} else {
			logging.Error("testenv/engine: invalid TESTENV_VALIDATION_LATENCY_MS",
				"subsystem", "engine", "value", v, "error", err)
		}
	}
	return NewMockValidation(cfg)
}

// SetVerdict installs a per-inference verdict override. Subsequent
// Validate calls for this InferenceID return Valid=valid regardless
// of DefaultValid. Safe to call concurrently with Validate.
func (e *MockValidationEngine) SetVerdict(inferenceID uint64, valid bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.verdicts[inferenceID] = valid
}

// ClearVerdict removes a per-inference verdict override. No-op when
// the ID was never overridden.
func (e *MockValidationEngine) ClearVerdict(inferenceID uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.verdicts, inferenceID)
}

// SetError installs a per-inference error so Validate returns err
// (and a nil *ValidateResult) for this InferenceID. Passing err==nil
// is equivalent to ClearError.
func (e *MockValidationEngine) SetError(inferenceID uint64, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err == nil {
		delete(e.injErrors, inferenceID)
		return
	}
	e.injErrors[inferenceID] = err
}

// ClearError removes an error override.
func (e *MockValidationEngine) ClearError(inferenceID uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.injErrors, inferenceID)
}

// Reset removes every override and returns the engine to "default
// verdict, no errors". Useful between scenarios sharing a host.
func (e *MockValidationEngine) Reset() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.verdicts = make(map[uint64]bool)
	e.injErrors = make(map[uint64]error)
}

// Validate returns the configured verdict for req.InferenceID. Error
// overrides take precedence over verdict overrides; verdict overrides
// take precedence over DefaultValid. Respects ctx cancellation
// during Latency sleep.
func (e *MockValidationEngine) Validate(ctx context.Context, req devshard.ValidateRequest) (*devshard.ValidateResult, error) {
	logging.Debug("testenv validation called",
		"subsystem", "engine",
		"inference_id", req.InferenceID,
		"escrow", req.EscrowID,
		"model", req.Model,
		"response_hash", hex.EncodeToString(req.ResponseHash),
	)

	if e.latency > 0 {
		if err := sleepCtx(ctx, e.latency); err != nil {
			return nil, err
		}
	}

	e.mu.RLock()
	if err, ok := e.injErrors[req.InferenceID]; ok {
		e.mu.RUnlock()
		logging.Debug("testenv validation: injected error",
			"subsystem", "engine",
			"inference_id", req.InferenceID,
			"error", err,
		)
		return nil, err
	}
	verdict, hasOverride := e.verdicts[req.InferenceID]
	e.mu.RUnlock()

	if !hasOverride {
		verdict = e.defaultValid
	}

	logging.Debug("testenv validation result",
		"subsystem", "engine",
		"inference_id", req.InferenceID,
		"valid", verdict,
		"override", hasOverride,
	)

	return &devshard.ValidateResult{Valid: verdict}, nil
}

// ErrTestenvInjectedFault is a convenience sentinel for SetError
// callers that just want a recognizable error.
var ErrTestenvInjectedFault = errors.New("testenv: injected validation fault")

// Compile-time assertion that MockValidationEngine satisfies the
// production interface.
var _ devshard.ValidationEngine = (*MockValidationEngine)(nil)
