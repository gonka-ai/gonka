package inference

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"common/completionapi"
	"common/storage/payloads"
	devshardpkg "devshard"
	"devshard/observability"
)

type mlRequestExecutor func(ctx context.Context, model string, body []byte) (*http.Response, error)

type processedExecutionResponse struct {
	responseHash []byte
	servedHash   []byte
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
	logprobsOptimizationEnabled bool,
	vocabularySize func() int,
) (*devshardpkg.ExecuteResult, error) {
	seed := int32(req.InferenceID)
	inferenceID := fmt.Sprintf("devshard-%s-%d", req.EscrowID, req.InferenceID)

	tokenIDLimit := 0
	if completionapi.RequestNeedsVocabulary(req.Prompt) {
		if tokenIDLimit = vocabularySize(); tokenIDLimit <= 0 {
			return nil, observability.Classify(observability.ReasonModifyRequestErr, observability.WhereRuntimeExecute, fmt.Errorf("modify request body: %w", completionapi.ErrVocabularyUnknown))
		}
	}
	modified, err := completionapi.ModifyRequestBodyForVocabulary(req.Prompt, seed, chainParams.LogprobsMode(), tokenIDLimit)
	if err != nil {
		return nil, observability.Classify(observability.ReasonModifyRequestErr, observability.WhereRuntimeExecute, fmt.Errorf("modify request body: %w", err))
	}
	// The executor does not trust the gateway's check: an out-of-range stop id crashes the node.
	if err := checkStopTokenIDs(modified.NewBody, vocabularySize); err != nil {
		return nil, observability.Classify(observability.ReasonModifyRequestErr, observability.WhereRuntimeExecute, err)
	}

	resp, err := execute(ctx, req.Model, modified.NewBody)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	processor := completionapi.NewExecutorResponseProcessor(inferenceID, modified.AsksForLogprobs)
	processor.SetLogprobsOptimization(req.LogprobsOptimizationOverride, logprobsOptimizationEnabled)

	processed, err := processExecutionHTTPResponse(req, resp, inferenceID, processor)
	if err != nil {
		return nil, observability.Classify(observability.ReasonProcessResponseErr, observability.WhereRuntimeExecute, err)
	}
	observability.ObserveTokens(observability.PathExecute, "", observability.TokenKindPrompt, processed.inputTokens)
	observability.ObserveTokens(observability.PathExecute, "", observability.TokenKindCompletion, processed.outputTokens)

	promptPayload, err := devshardpkg.CanonicalizeJSON(req.Prompt)
	if err != nil {
		return nil, observability.Classify(observability.ReasonCanonicalizePromptErr, observability.WhereRuntimeExecute, fmt.Errorf("canonicalize prompt: %w", err))
	}

	err = store.Store(
		ctx,
		req.EscrowID,
		req.InferenceID,
		payloadEpoch,
		promptPayload,
		processed.responseBody,
	)
	if errors.Is(err, payloads.ErrAlreadyStored) {
		// An earlier run of this inference stored first; validators fetch
		// those bytes, so the finish commits their hashes.
		reader, _ := store.(PayloadReader)
		return storedExecutionResult(ctx, req, reader, payloadEpoch)
	}
	if err != nil {
		return nil, observability.Classify(observability.ReasonPayloadStoreErr, observability.WhereRuntimeExecute, fmt.Errorf("store payloads: %w", err))
	}

	return &devshardpkg.ExecuteResult{
		ResponseHash: processed.responseHash,
		ServedHash:   processed.servedHash,
		InputTokens:  processed.inputTokens,
		OutputTokens: processed.outputTokens,
		ResponseBody: processed.responseBody,
	}, nil
}

func processExecutionHTTPResponse(
	req devshardpkg.ExecuteRequest,
	resp *http.Response,
	inferenceID string,
	processor *completionapi.ExecutorResponseProcessor,
) (*processedExecutionResponse, error) {
	isSSE := completionapi.IsEventStream(resp)
	streamProcessor := compactingClientFaultProcessor{ResponseProcessor: processor}

	if !isSSE && isClientFaultStatus(resp.StatusCode) {
		return processClientFaultResponse(req, resp, processor)
	}

	if req.ResponseWriter != nil && isSSE {
		if err := proxyResponse(resp, req.ResponseWriter, true, streamProcessor, inferenceID); err != nil {
			return nil, fmt.Errorf("relay response: %w", err)
		}
	} else {
		if err := completionapi.ProcessHTTPResponse(resp, streamProcessor); err != nil {
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
	servedHash, err := processor.GetServedHash()
	if err != nil {
		return nil, fmt.Errorf("get served hash: %w", err)
	}
	usage := &completionapi.Usage{}
	if !completionapi.IsClientFaultErrorResponse(bodyBytes) {
		usage, err = processor.GetUsage()
		if err != nil {
			return nil, fmt.Errorf("get usage: %w", err)
		}
	}

	return &processedExecutionResponse{
		responseHash: hash[:],
		servedHash:   servedHash[:],
		inputTokens:  usage.PromptTokens,
		outputTokens: usage.CompletionTokens,
		responseBody: bodyBytes,
	}, nil
}

// checkStopTokenIDs resolves vocabularySize only for a request with stop ids: the lookup can block on the chain.
func checkStopTokenIDs(body []byte, vocabularySize func() int) error {
	var requestMap map[string]interface{}
	if err := json.Unmarshal(body, &requestMap); err != nil {
		return fmt.Errorf("modify request body: %w", err)
	}
	if !completionapi.HasStopTokenIDs(requestMap) {
		return nil
	}
	return completionapi.ValidateStopTokenIDs(requestMap, vocabularySize())
}

func isClientFaultStatus(statusCode int) bool {
	return statusCode == http.StatusBadRequest || statusCode == http.StatusUnprocessableEntity
}

func processClientFaultResponse(
	req devshardpkg.ExecuteRequest,
	resp *http.Response,
	processor *completionapi.ExecutorResponseProcessor,
) (*processedExecutionResponse, error) {
	body, err := io.ReadAll(completionapi.NewCappedResponseReader(resp.Body))
	if err != nil {
		return nil, fmt.Errorf("read client fault response: %w", err)
	}
	body = completionapi.CompactClientFaultBody(bytes.TrimSpace(body))
	forwarded, err := processor.ProcessStreamedResponse(completionapi.DataPrefix + string(body))
	if err != nil {
		return nil, fmt.Errorf("upstream status %d: %w", resp.StatusCode, err)
	}

	done, err := processor.ProcessStreamedResponse("data: [DONE]")
	if err != nil {
		return nil, fmt.Errorf("process client fault terminator: %w", err)
	}
	bodyBytes, err := processor.GetResponseBytes()
	if err != nil {
		return nil, fmt.Errorf("get body bytes: %w", err)
	}
	if !completionapi.IsClientFaultErrorResponse(bodyBytes) {
		return nil, fmt.Errorf("upstream status %d without a client fault error body", resp.StatusCode)
	}

	if req.ResponseWriter != nil {
		fmt.Fprintf(req.ResponseWriter, "%s\n\n%s\n\n", forwarded, done)
		if f, ok := req.ResponseWriter.(http.Flusher); ok {
			f.Flush()
		}
	}

	hash := sha256.Sum256(bodyBytes)
	servedHash, err := processor.GetServedHash()
	if err != nil {
		return nil, fmt.Errorf("get served hash: %w", err)
	}

	return &processedExecutionResponse{
		responseHash: hash[:],
		servedHash:   servedHash[:],
		responseBody: bodyBytes,
	}, nil
}

type compactingClientFaultProcessor struct {
	completionapi.ResponseProcessor
}

func (p compactingClientFaultProcessor) ProcessStreamedResponse(line string) (string, error) {
	if len(line) > completionapi.MaxClientFaultBodyBytes && strings.HasPrefix(line, completionapi.DataPrefix) {
		body := []byte(strings.TrimPrefix(line, completionapi.DataPrefix))
		if compact := completionapi.CompactClientFaultBody(body); !bytes.Equal(compact, body) {
			line = completionapi.DataPrefix + string(compact)
		}
	}
	return p.ResponseProcessor.ProcessStreamedResponse(line)
}
