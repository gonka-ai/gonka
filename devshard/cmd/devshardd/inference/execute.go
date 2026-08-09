package inference

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"common/completionapi"
	devshardpkg "devshard"
	"devshard/observability"
)

type mlRequestExecutor func(ctx context.Context, model string, body []byte) (*http.Response, error)

type processedExecutionResponse struct {
	responseHash          []byte
	inputTokens           uint64
	outputTokens          uint64
	responseBody          []byte
	partialResponse       bool
	partialResponseReason string
	partialResponseWhere  string
}

func executeInference(
	ctx context.Context,
	req devshardpkg.ExecuteRequest,
	store PayloadStore,
	payloadEpoch uint64,
	execute mlRequestExecutor,
	chainParams ChainParamsProvider,
) (*devshardpkg.ExecuteResult, error) {
	// Detach from the gateway HTTP request: client disconnect must stop
	// proxying only, not abort ML generation / payload persistence.
	drainCtx, cancel := executionContext(ctx)
	defer cancel()

	seed := int32(req.InferenceID)
	inferenceID := fmt.Sprintf("devshard-%s-%d", req.EscrowID, req.InferenceID)

	modified, err := completionapi.ModifyRequestBodyWithLogprobsMode(req.Prompt, seed, chainParams.LogprobsMode())
	if err != nil {
		return nil, observability.Classify(observability.ReasonModifyRequestErr, observability.WhereRuntimeExecute, fmt.Errorf("modify request body: %w", err))
	}

	resp, err := execute(drainCtx, req.Model, modified.NewBody)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	processed, err := processExecutionHTTPResponse(req, resp, inferenceID)
	if err != nil {
		return nil, observability.Classify(observability.ReasonProcessResponseErr, observability.WhereRuntimeExecute, err)
	}
	observability.ObserveTokens(observability.PathExecute, "", observability.TokenKindPrompt, processed.inputTokens)
	observability.ObserveTokens(observability.PathExecute, "", observability.TokenKindCompletion, processed.outputTokens)

	promptPayload, err := devshardpkg.CanonicalizeJSON(req.Prompt)
	if err != nil {
		return nil, observability.Classify(observability.ReasonCanonicalizePromptErr, observability.WhereRuntimeExecute, fmt.Errorf("canonicalize prompt: %w", err))
	}

	if err := store.Store(
		drainCtx,
		req.EscrowID,
		req.InferenceID,
		payloadEpoch,
		promptPayload,
		processed.responseBody,
	); err != nil {
		return nil, observability.Classify(observability.ReasonPayloadStoreErr, observability.WhereRuntimeExecute, fmt.Errorf("store payloads: %w", err))
	}

	return &devshardpkg.ExecuteResult{
		ResponseHash:          processed.responseHash,
		InputTokens:           processed.inputTokens,
		OutputTokens:          processed.outputTokens,
		ResponseBody:          processed.responseBody,
		PartialResponse:       processed.partialResponse,
		PartialResponseReason: processed.partialResponseReason,
		PartialResponseWhere:  processed.partialResponseWhere,
	}, nil
}

func processExecutionHTTPResponse(
	req devshardpkg.ExecuteRequest,
	resp *http.Response,
	inferenceID string,
) (*processedExecutionResponse, error) {
	processor := completionapi.NewExecutorResponseProcessor(inferenceID)

	contentType := resp.Header.Get("Content-Type")
	isSSE := strings.HasPrefix(contentType, "text/event-stream")

	var proxyOutcome streamProxyOutcome
	if req.ResponseWriter != nil && isSSE {
		proxyOutcome = proxyResponse(resp, req.ResponseWriter, true, processor, inferenceID)
	} else {
		if err := completionapi.ProcessHTTPResponse(resp, processor); err != nil {
			return nil, fmt.Errorf("process response: %w", err)
		}
		proxyOutcome.sawDone = true
	}

	completionResp, err := processor.GetResponse()
	if err != nil {
		return nil, fmt.Errorf("get completion response: %w", err)
	}

	bodyBytes, err := completionResp.GetBodyBytes()
	if err != nil {
		return nil, fmt.Errorf("get body bytes: %w", err)
	}

	if req.ResponseWriter != nil && !isSSE {
		fmt.Fprintf(req.ResponseWriter, "data: %s\n\ndata: [DONE]\n\n", bodyBytes)
		if f, ok := req.ResponseWriter.(http.Flusher); ok {
			f.Flush()
		}
	}

	hash := sha256.Sum256(bodyBytes)
	usage, err := completionResp.GetUsage()
	if err != nil {
		if errors.Is(err, completionapi.ErrStreamedUsageMissing) {
			observability.IncMissingUsage()
			slog.Error("streamed response missing usage chunk",
				"inference_id", inferenceID,
				"error", err)
		}
		return nil, fmt.Errorf("get usage: %w", err)
	}
	if usage.PromptTokens == 0 && completionLooksNonEmpty(completionResp, usage) {
		observability.IncMissingUsage()
		slog.Error("streamed response reported zero prompt tokens on non-empty completion",
			"inference_id", inferenceID,
			"completion_tokens", usage.CompletionTokens)
		return nil, fmt.Errorf("get usage: %w", completionapi.ErrStreamedUsageMissing)
	}

	out := &processedExecutionResponse{
		responseHash: hash[:],
		inputTokens:  usage.PromptTokens,
		outputTokens: usage.CompletionTokens,
		responseBody: bodyBytes,
	}
	if isSSE && !proxyOutcome.sawDone {
		out.partialResponse = true
		out.partialResponseReason = string(observability.ReasonPartialResponseInterrupted)
		out.partialResponseWhere = string(observability.WhereRuntimeDrainML)
	}
	return out, nil
}

// completionLooksNonEmpty reports whether the response carries any completion
// output (token counts, content, or logprobs). Used to refuse finishing with
// InputTokens=0 when the model clearly produced something.
func completionLooksNonEmpty(resp completionapi.CompletionResponse, usage *completionapi.Usage) bool {
	if usage != nil && usage.CompletionTokens > 0 {
		return true
	}
	switch r := resp.(type) {
	case *completionapi.StreamedCompletionResponse:
		for _, d := range r.Resp.Data {
			for _, c := range d.Choices {
				if len(c.Logprobs.Content) > 0 {
					return true
				}
				if c.Delta != nil && c.Delta.Content != nil && *c.Delta.Content != "" {
					return true
				}
				if c.Message != nil && c.Message.Content != "" {
					return true
				}
			}
		}
	case *completionapi.JsonCompletionResponse:
		for _, c := range r.Resp.Choices {
			if len(c.Logprobs.Content) > 0 {
				return true
			}
			if c.Message != nil && c.Message.Content != "" {
				return true
			}
		}
	}
	return false
}
