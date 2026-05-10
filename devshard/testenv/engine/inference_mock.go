// Package engine provides stub implementations of devshard.InferenceEngine
// and devshard.ValidationEngine for the testenv.
//
// These stubs never contact real ML nodes. They produce deterministic,
// per-inference output suitable for protocol-level assertions; latency
// and per-inference overrides are driven by env vars or an explicit
// Config. Heterogeneous per-node behavior (per-node-id profiles, a
// REST control plane driven by a test orchestrator) is Phase 7b/7c,
// designed in devshard/docs/testenv-stub-engines.md.
//
// See devshard/docs/testenv.md §Phase 7 for the tracking entry.
package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"devshard"
	"devshard/logging"
)

// MockInferenceConfig tunes the static stub. Zero values produce a
// sensible default (no latency, 80/40 token report, deterministic body
// keyed off inference_id + escrow + model).
type MockInferenceConfig struct {
	// Latency is the wall-clock sleep before Execute returns. Respects
	// ctx cancellation.
	Latency time.Duration
	// InputTokens / OutputTokens populate ExecuteResult. Stub engines
	// do not count real tokens; scenarios that check rate-limiting or
	// settlement math override these.
	InputTokens  uint64
	OutputTokens uint64
}

// MockInferenceEngine is a deterministic stub satisfying
// devshard.InferenceEngine. Safe for concurrent use; Execute takes no
// locks on the hot path (config is a snapshot installed at
// construction; after NewMockInference returns, the struct is
// effectively immutable).
type MockInferenceEngine struct {
	cfg MockInferenceConfig
}

// defaultInferenceCfg is the "sensible" config used when the caller
// does not explicitly override a field. Pinned to the numbers the
// original subnet-testenv engine used so existing scenarios that
// assume 80/40 token accounting keep passing.
var defaultInferenceCfg = MockInferenceConfig{
	Latency:      0,
	InputTokens:  80,
	OutputTokens: 40,
}

// NewMockInference builds a MockInferenceEngine from cfg. Any zero
// field on cfg is filled from the default config so tests can pass a
// partial struct and only override what they care about.
func NewMockInference(cfg MockInferenceConfig) *MockInferenceEngine {
	if cfg.InputTokens == 0 {
		cfg.InputTokens = defaultInferenceCfg.InputTokens
	}
	if cfg.OutputTokens == 0 {
		cfg.OutputTokens = defaultInferenceCfg.OutputTokens
	}
	return &MockInferenceEngine{cfg: cfg}
}

// NewMockInferenceFromEnv reads a small, curated set of env vars and
// folds them into the default config. Unrecognized / unset vars leave
// the corresponding field at its default. Malformed values (e.g.
// non-numeric latency) log an error and are treated as unset — the
// test environment should not fail to start just because one env var
// is wrong.
//
// Recognized vars:
//   - TESTENV_INFERENCE_LATENCY_MS (int, milliseconds)
//   - TESTENV_INFERENCE_INPUT_TOKENS (uint64)
//   - TESTENV_INFERENCE_OUTPUT_TOKENS (uint64)
func NewMockInferenceFromEnv() *MockInferenceEngine {
	cfg := defaultInferenceCfg
	if v := os.Getenv("TESTENV_INFERENCE_LATENCY_MS"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms >= 0 {
			cfg.Latency = time.Duration(ms) * time.Millisecond
		} else {
			logging.Error("testenv/engine: invalid TESTENV_INFERENCE_LATENCY_MS",
				"subsystem", "engine", "value", v, "error", err)
		}
	}
	if v := os.Getenv("TESTENV_INFERENCE_INPUT_TOKENS"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			cfg.InputTokens = n
		} else {
			logging.Error("testenv/engine: invalid TESTENV_INFERENCE_INPUT_TOKENS",
				"subsystem", "engine", "value", v, "error", err)
		}
	}
	if v := os.Getenv("TESTENV_INFERENCE_OUTPUT_TOKENS"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			cfg.OutputTokens = n
		} else {
			logging.Error("testenv/engine: invalid TESTENV_INFERENCE_OUTPUT_TOKENS",
				"subsystem", "engine", "value", v, "error", err)
		}
	}
	return NewMockInference(cfg)
}

// Execute is deterministic: the ResponseBody is keyed off
// (inference_id, escrow, model) so the same triple always yields the
// same bytes (and therefore the same ResponseHash). This is the
// property protocol tests rely on when asserting "the second
// verifier observed the same response hash as the first".
//
// When req.ResponseWriter is non-nil, the engine writes two SSE frames
// (the body as a single `data:` event + `[DONE]`) before returning —
// matching the subnet-testenv wire shape so existing scenario code
// ports without change. If the writer is not an http.Flusher we log
// at Error and fall through to the non-streaming return path; this is
// a configuration bug from the caller, not an inference failure.
//
// Respects ctx cancellation during Latency sleep.
func (e *MockInferenceEngine) Execute(ctx context.Context, req devshard.ExecuteRequest) (*devshard.ExecuteResult, error) {
	start := time.Now()

	logging.Debug("testenv inference execute called",
		"subsystem", "engine",
		"inference_id", req.InferenceID,
		"escrow", req.EscrowID,
		"model", req.Model,
		"input_length", req.InputLength,
		"max_tokens", req.MaxTokens,
		"streaming", req.ResponseWriter != nil,
	)

	if e.cfg.Latency > 0 {
		if err := sleepCtx(ctx, e.cfg.Latency); err != nil {
			return nil, err
		}
	}

	body := buildInferenceBody(req)
	hash := sha256.Sum256(body)

	logging.Debug("testenv inference body built",
		"subsystem", "engine",
		"inference_id", req.InferenceID,
		"body_len", len(body),
		"hash", hex.EncodeToString(hash[:]),
	)

	if req.ResponseWriter != nil {
		flusher, ok := req.ResponseWriter.(http.Flusher)
		if !ok {
			// Defensive: misconfigured caller. Log loudly so the bug
			// doesn't hide behind the non-streaming return path.
			logging.Error("testenv inference: ResponseWriter does not implement http.Flusher; streaming skipped",
				"subsystem", "engine",
				"inference_id", req.InferenceID,
			)
		} else {
			fmt.Fprintf(req.ResponseWriter, "data: %s\n\n", body)
			flusher.Flush()
			fmt.Fprintf(req.ResponseWriter, "data: [DONE]\n\n")
			flusher.Flush()
			logging.Info("testenv inference: SSE wrote ML data line and [DONE] terminator",
				"subsystem", "engine",
				"inference_id", req.InferenceID,
				"response_writer_type", fmt.Sprintf("%T", req.ResponseWriter),
				"body_len", len(body),
			)
		}
	}

	logging.Info("testenv inference completed",
		"subsystem", "engine",
		"inference_id", req.InferenceID,
		"escrow", req.EscrowID,
		"model", req.Model,
		"input_tokens", e.cfg.InputTokens,
		"output_tokens", e.cfg.OutputTokens,
		"elapsed_ms", time.Since(start).Milliseconds(),
	)

	return &devshard.ExecuteResult{
		ResponseHash: hash[:],
		InputTokens:  e.cfg.InputTokens,
		OutputTokens: e.cfg.OutputTokens,
		ResponseBody: body,
	}, nil
}

// buildInferenceBody returns a deterministic JSON response that
// embeds the per-call triple (inference_id, escrow, model) in the
// assistant content so distinct inferences produce distinct hashes.
// Pinned to the subnet-testenv shape so existing scenario fixtures
// remain valid.
func buildInferenceBody(req devshard.ExecuteRequest) []byte {
	content := fmt.Sprintf(
		"mock inference #%d for escrow %s model %s",
		req.InferenceID, req.EscrowID, req.Model,
	)
	return []byte(fmt.Sprintf(
		`{"choices":[{"message":{"role":"assistant","content":%q}}],"usage":{"prompt_tokens":80,"completion_tokens":40}}`,
		content,
	))
}

// sleepCtx is a tiny helper that respects ctx cancellation. Extracted
// for reuse in the validation engine.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Config returns a defensive copy of the stub's active config. Useful
// for tests that need to assert on the post-merge defaults.
func (e *MockInferenceEngine) Config() MockInferenceConfig { return e.cfg }

// Compile-time assertion that MockInferenceEngine satisfies the
// production interface.
var _ devshard.InferenceEngine = (*MockInferenceEngine)(nil)
