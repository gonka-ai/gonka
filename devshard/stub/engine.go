package stub

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"

	"devshard"
)

// InferenceEngine returns fixed values for testing.
type InferenceEngine struct {
	ResponseHash []byte
	InputTokens  uint64
	OutputTokens uint64
	ResponseBody []byte
}

func NewInferenceEngine() *InferenceEngine {
	body := []byte(`{"choices":[{"message":{"content":"stub"}}],"usage":{"prompt_tokens":80,"completion_tokens":40}}`)
	h := sha256.Sum256(body)
	return &InferenceEngine{
		ResponseHash: h[:],
		InputTokens:  80,
		OutputTokens: 40,
		ResponseBody: body,
	}
}

func (e *InferenceEngine) Execute(_ context.Context, req devshard.ExecuteRequest) (*devshard.ExecuteResult, error) {
	if req.ResponseWriter != nil {
		// Write mock SSE events to the response writer.
		if rw, ok := req.ResponseWriter.(http.Flusher); ok {
			fmt.Fprintf(req.ResponseWriter, "data: %s\n\n", e.ResponseBody)
			rw.Flush()
			fmt.Fprintf(req.ResponseWriter, "data: [DONE]\n\n")
			rw.Flush()
		}
	}

	return &devshard.ExecuteResult{
		ResponseHash: e.ResponseHash,
		InputTokens:  e.InputTokens,
		OutputTokens: e.OutputTokens,
		ResponseBody: e.ResponseBody,
	}, nil
}

// ConfigurableEngine allows per-inference overrides for testing with
// varying token counts. Falls back to Default for IDs not in Override.
type ConfigurableEngine struct {
	Default  devshard.ExecuteResult
	Override map[uint64]devshard.ExecuteResult // inference_id -> result
}

func (e *ConfigurableEngine) Execute(_ context.Context, req devshard.ExecuteRequest) (*devshard.ExecuteResult, error) {
	if r, ok := e.Override[req.InferenceID]; ok {
		cp := r
		return &cp, nil
	}
	cp := e.Default
	return &cp, nil
}

// FailingEngine always returns an error from Execute.
type FailingEngine struct {
	Err error
}

func NewFailingEngine(err error) *FailingEngine {
	return &FailingEngine{Err: err}
}

func (e *FailingEngine) Execute(_ context.Context, _ devshard.ExecuteRequest) (*devshard.ExecuteResult, error) {
	return nil, e.Err
}

// ValidationEngine returns fixed validation results for testing.
type ValidationEngine struct {
	Valid bool
}

func NewValidationEngine() *ValidationEngine {
	return &ValidationEngine{Valid: true}
}

func (e *ValidationEngine) Validate(_ context.Context, _ devshard.ValidateRequest) (*devshard.ValidateResult, error) {
	return &devshard.ValidateResult{Valid: e.Valid}, nil
}
