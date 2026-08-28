package inference

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"

	"common/completionapi"
	"common/logging"
	devshardpkg "devshard"
	"devshard/observability"

	"github.com/productscience/inference/x/inference/types"
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
) (*processedExecutionResponse, error) {
	processor := completionapi.NewExecutorResponseProcessor(inferenceID)

	contentType := resp.Header.Get("Content-Type")
	isSSE := strings.HasPrefix(contentType, "text/event-stream")

	if req.ResponseWriter != nil && isSSE {
		proxyResponse(resp, req.ResponseWriter, true, processor, inferenceID)
	} else {
		if err := completionapi.ProcessHTTPResponse(resp, processor); err != nil {
			return nil, fmt.Errorf("process response: %w", err)
		}
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
		// Falling back to the stored copy would quietly hand the gateway a body with the logprobs
		// already cut out, which is the regression this split exists to prevent.
		relayed := processor.GetForwardedJSONBytes()
		if relayed == nil {
			logging.Warn("Relaying the stored copy: no forwarded body was produced", types.Inferences,
				"inference_id", inferenceID)
			relayed = bodyBytes
		}
		fmt.Fprintf(req.ResponseWriter, "data: %s\n\ndata: [DONE]\n\n", relayed)
		if f, ok := req.ResponseWriter.(http.Flusher); ok {
			f.Flush()
		}
	}

	// The processor slimmed each chunk as it parsed it, so what it hands back is already what is stored.
	hash := sha256.Sum256(bodyBytes)
	usage, err := completionResp.GetUsage()
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
