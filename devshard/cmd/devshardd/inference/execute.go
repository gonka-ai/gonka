package inference

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"

	"common/completionapi"
	devshardpkg "devshard"
	"devshard/observability"
)

type mlRequestExecutor func(ctx context.Context, model string, body []byte) (*http.Response, error)

type processedExecutionResponse struct {
	responseHash []byte
	inputTokens  uint64
	outputTokens uint64
	responseBody []byte
}

func executeInference(
	ctx context.Context,
	req devshardpkg.ExecuteRequest,
	store PayloadStore,
	payloadEpoch uint64,
	execute mlRequestExecutor,
	chainParams ChainParamsProvider,
) (*devshardpkg.ExecuteResult, error) {
	seed := int32(req.InferenceID)
	inferenceID := fmt.Sprintf("devshard-%s-%d", req.EscrowID, req.InferenceID)

	modified, err := completionapi.ModifyRequestBodyWithLogprobsMode(req.Prompt, seed, chainParams.LogprobsMode())
	if err != nil {
		return nil, observability.Classify(observability.ReasonModifyRequestErr, observability.WhereRuntimeExecute, fmt.Errorf("modify request body: %w", err))
	}

	resp, err := execute(ctx, req.Model, modified.NewBody)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	processed, err := processExecutionHTTPResponse(req, resp, inferenceID, modified.AsksForLogprobs)
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
		ctx,
		req.EscrowID,
		req.InferenceID,
		payloadEpoch,
		promptPayload,
		processed.responseBody,
	); err != nil {
		return nil, observability.Classify(observability.ReasonPayloadStoreErr, observability.WhereRuntimeExecute, fmt.Errorf("store payloads: %w", err))
	}

	return &devshardpkg.ExecuteResult{
		ResponseHash: processed.responseHash,
		InputTokens:  processed.inputTokens,
		OutputTokens: processed.outputTokens,
		ResponseBody: processed.responseBody,
	}, nil
}

func processExecutionHTTPResponse(
	req devshardpkg.ExecuteRequest,
	resp *http.Response,
	inferenceID string,
	forwardLogprobs bool,
) (*processedExecutionResponse, error) {
	processor := completionapi.NewExecutorResponseProcessor(inferenceID, forwardLogprobs)

	isSSE := completionapi.IsEventStream(resp)

	if req.ResponseWriter != nil && isSSE {
		if err := proxyResponse(resp, req.ResponseWriter, true, processor, inferenceID); err != nil {
			return nil, fmt.Errorf("relay response: %w", err)
		}
	} else {
		if err := completionapi.ProcessHTTPResponse(resp, processor); err != nil {
			return nil, fmt.Errorf("process response: %w", err)
		}
	}

	bodyBytes, err := processor.GetResponseBytes()
	if err != nil {
		return nil, fmt.Errorf("get body bytes: %w", err)
	}

	if req.ResponseWriter != nil && !isSSE {
		// The stored copy is no substitute: it carries logprobs the caller may not have asked for.
		relayed := processor.GetForwardedJSONBytes()
		if relayed == nil {
			return nil, fmt.Errorf("relay response: the processor produced no forwarded body")
		}
		fmt.Fprintf(req.ResponseWriter, "data: %s\n\ndata: [DONE]\n\n", relayed)
		if f, ok := req.ResponseWriter.(http.Flusher); ok {
			f.Flush()
		}
	}

	// The processor slimmed each chunk as it parsed it, so what it hands back is already what is stored.
	hash := sha256.Sum256(bodyBytes)
	usage, err := processor.GetUsage()
	if err != nil {
		return nil, fmt.Errorf("get usage: %w", err)
	}

	return &processedExecutionResponse{
		responseHash: hash[:],
		inputTokens:  usage.PromptTokens,
		outputTokens: usage.CompletionTokens,
		responseBody: bodyBytes,
	}, nil
}
