package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"subnet"
	"subnet/logging"
)

// MockInferenceEngine is the testenv implementation of subnet.InferenceEngine.
// It does not call any real ML node — it builds a deterministic fake response
// and logs at every level so inference activity is clearly visible in the logs.
//
// Logging levels used:
//
//	TRACE – entering Execute, raw field values
//	DEBUG – intermediate steps (body construction, hash)
//	INFO  – successful completion with result summary
//	ERROR – unexpected states (missing flusher on streaming path)
type MockInferenceEngine struct{}

func NewMockInferenceEngine() *MockInferenceEngine { return &MockInferenceEngine{} }

func (e *MockInferenceEngine) Execute(ctx context.Context, req subnet.ExecuteRequest) (*subnet.ExecuteResult, error) {
	start := time.Now()

	// TRACE — entering the call; high-frequency, printed for every inference.
	trace("inference execute called",
		"subsystem", "engine",
		"inference_id", req.InferenceID,
		"escrow", req.EscrowID,
		"model", req.Model,
		"input_length", req.InputLength,
		"max_tokens", req.MaxTokens,
		"streaming", req.ResponseWriter != nil,
	)
	logging.Info("dev-reload-marker subnethost inference execute entered",
		"subsystem", "engine",
		"inference_id", req.InferenceID,
		"escrow", req.EscrowID,
		"model", req.Model,
	)

	// DEBUG — body construction step.
	body := buildMockBody(req)
	slog.Debug("mock response body built",
		"subsystem", "engine",
		"inference_id", req.InferenceID,
		"body_len", len(body),
		"body_preview", previewBytes(body, 80),
	)

	// DEBUG — hash step.
	hash := sha256.Sum256(body)
	slog.Debug("response hash computed",
		"subsystem", "engine",
		"inference_id", req.InferenceID,
		"hash", hex.EncodeToString(hash[:]),
	)

	// Streaming path: write mock SSE events to the ResponseWriter if provided.
	if req.ResponseWriter != nil {
		flusher, ok := req.ResponseWriter.(http.Flusher)
		if !ok {
			// ERROR — ResponseWriter was set but does not support flushing.
			// This is an unexpected configuration; log and continue without streaming.
			logging.Error("ResponseWriter does not implement http.Flusher — streaming skipped",
				"subsystem", "engine",
				"inference_id", req.InferenceID,
			)
		} else {
			fmt.Fprintf(req.ResponseWriter, "data: %s\n\n", body)
			flusher.Flush()
			fmt.Fprintf(req.ResponseWriter, "data: [DONE]\n\n")
			flusher.Flush()

			slog.Debug("SSE events written to ResponseWriter",
				"subsystem", "engine",
				"inference_id", req.InferenceID,
				"events", 2,
			)
		}
	}

	elapsed := time.Since(start)

	// INFO — one line per completed inference: visible in normal operation.
	logging.Info("inference completed",
		"subsystem", "engine",
		"inference_id", req.InferenceID,
		"escrow", req.EscrowID,
		"model", req.Model,
		"input_tokens", 80,
		"output_tokens", 40,
		"elapsed_ms", elapsed.Milliseconds(),
	)

	return &subnet.ExecuteResult{
		ResponseHash: hash[:],
		InputTokens:  80,
		OutputTokens: 40,
		ResponseBody: body,
	}, nil
}

// buildMockBody returns a deterministic JSON response body that includes
// the inference_id so individual calls are distinguishable in logs.
func buildMockBody(req subnet.ExecuteRequest) []byte {
	content := fmt.Sprintf("mock inference #%d for escrow %s model %s",
		req.InferenceID, req.EscrowID, req.Model)
	return []byte(fmt.Sprintf(
		`{"choices":[{"message":{"role":"assistant","content":%q}}],"usage":{"prompt_tokens":80,"completion_tokens":40}}`,
		content,
	))
}

// previewBytes returns the first n bytes of b as a string for log display.
func previewBytes(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}

// ── MockValidationEngine ──────────────────────────────────────────────────────

// MockValidationEngine is the testenv implementation of subnet.ValidationEngine.
// Always returns Valid=true and logs the validation at DEBUG level.
type MockValidationEngine struct{}

func NewMockValidationEngine() *MockValidationEngine { return &MockValidationEngine{} }

func (e *MockValidationEngine) Validate(_ context.Context, req subnet.ValidateRequest) (*subnet.ValidateResult, error) {
	// TRACE — entering validation; very high-frequency in multi-verifier sessions.
	trace("validation called",
		"subsystem", "engine",
		"inference_id", req.InferenceID,
		"escrow", req.EscrowID,
		"model", req.Model,
		"response_hash", hex.EncodeToString(req.ResponseHash),
	)

	// DEBUG — validation decision.
	slog.Debug("mock validation result",
		"subsystem", "engine",
		"inference_id", req.InferenceID,
		"valid", true,
	)

	return &subnet.ValidateResult{Valid: true}, nil
}
