package stub

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"

	"devshard"
)

const SessionEchoPrefix = "session:"

// InferenceEngine returns fixed values for testing.
type InferenceEngine struct {
	ResponseHash          []byte
	InputTokens           uint64
	OutputTokens          uint64
	ResponseBody          []byte
	BlockUntilContextDone bool
	EchoSessionID         bool
}

func NewInferenceEngine() *InferenceEngine {
	const inputTokens, outputTokens = 80, 40
	body := completionBody("stub", inputTokens, outputTokens)
	h := sha256.Sum256(body)
	return &InferenceEngine{
		ResponseHash: h[:],
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		ResponseBody: body,
	}
}

func (e *InferenceEngine) Execute(ctx context.Context, req devshard.ExecuteRequest) (*devshard.ExecuteResult, error) {
	if e.BlockUntilContextDone {
		<-ctx.Done()
		return nil, ctx.Err()
	}

	body, responseHash := e.ResponseBody, e.ResponseHash
	if e.EchoSessionID {
		body = completionBody(SessionEchoPrefix+req.SessionID, e.InputTokens, e.OutputTokens)
		digest := sha256.Sum256(body)
		responseHash = digest[:]
	}

	if req.ResponseWriter != nil {
		// Write mock SSE events to the response writer.
		if rw, ok := req.ResponseWriter.(http.Flusher); ok {
			fmt.Fprintf(req.ResponseWriter, "data: %s\n\n", body)
			rw.Flush()
			fmt.Fprintf(req.ResponseWriter, "data: [DONE]\n\n")
			rw.Flush()
		}
	}

	return &devshard.ExecuteResult{
		ResponseHash: responseHash,
		InputTokens:  e.InputTokens,
		OutputTokens: e.OutputTokens,
		ResponseBody: body,
	}, nil
}

func completionBody(content string, inputTokens, outputTokens uint64) []byte {
	quoted, _ := json.Marshal(content)
	return fmt.Appendf(nil, `{"choices":[{"message":{"content":%s}}],"usage":{"prompt_tokens":%d,"completion_tokens":%d}}`,
		quoted, inputTokens, outputTokens)
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
