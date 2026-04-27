package subnet

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"

	"decentralized-api/broker"
	"decentralized-api/chainphase"
	"decentralized-api/completionapi"
	"decentralized-api/internal/server/public"
	"decentralized-api/payloadstorage"

	"subnet"
)

// EngineAdapter implements subnet.InferenceEngine by delegating to broker and completionapi.
type EngineAdapter struct {
	broker       *broker.Broker
	nodeVersion  string
	payloadStore payloadstorage.PayloadStorage
	phaseTracker *chainphase.ChainPhaseTracker
	httpClient   *http.Client
}

func NewEngineAdapter(
	b *broker.Broker,
	nodeVersion string,
	ps payloadstorage.PayloadStorage,
	phaseTracker *chainphase.ChainPhaseTracker,
	httpClient *http.Client,
) *EngineAdapter {
	return &EngineAdapter{
		broker:       b,
		nodeVersion:  nodeVersion,
		payloadStore: ps,
		phaseTracker: phaseTracker,
		httpClient:   httpClient,
	}
}

func (e *EngineAdapter) Execute(ctx context.Context, req subnet.ExecuteRequest) (*subnet.ExecuteResult, error) {
	seed := int32(req.InferenceID)
	inferenceId := fmt.Sprintf("subnet-%s-%d", req.EscrowID, req.InferenceID)

	modified, err := completionapi.ModifyRequestBody(req.Prompt, seed)
	if err != nil {
		return nil, fmt.Errorf("modify request body: %w", err)
	}

	resp, err := broker.DoWithLockedNodeHTTPRetry(e.broker, req.Model, nil, 3,
		func(node *broker.Node) (*http.Response, *broker.ActionError) {
			url := node.InferenceUrlWithVersion(e.nodeVersion) + "/v1/chat/completions"
			httpReq, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(modified.NewBody))
			if reqErr != nil {
				return nil, broker.NewApplicationActionError(reqErr)
			}
			httpReq.Header.Set("Content-Type", "application/json")
			httpResp, postErr := e.httpClient.Do(httpReq)
			if postErr != nil {
				return nil, broker.NewTransportActionError(postErr)
			}
			return httpResp, nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("broker execute: %w", err)
	}
	defer resp.Body.Close()

	processor := completionapi.NewExecutorResponseProcessor(inferenceId)

	contentType := resp.Header.Get("Content-Type")
	isSSE := strings.HasPrefix(contentType, "text/event-stream")

	if req.ResponseWriter != nil && isSSE {
		// Streaming ML response: proxy SSE events directly to the client.
		// ProxyResponse writes each SSE line with proper framing (data: prefix).
		public.ProxyResponse(resp, req.ResponseWriter, true, processor, inferenceId)
	} else {
		// Non-streaming ML response or no client connection.
		// Process body via processor only (no proxy to client yet).
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

	// For non-streaming ML responses with a ResponseWriter (SSE transport),
	// write the JSON body as a proper SSE data event. ProxyResponse/proxyJsonResponse
	// would write raw bytes without SSE framing, corrupting the stream.
	if req.ResponseWriter != nil && !isSSE {
		fmt.Fprintf(req.ResponseWriter, "data: %s\n\ndata: [DONE]\n\n", bodyBytes)
		if f, ok := req.ResponseWriter.(http.Flusher); ok {
			f.Flush()
		}
	}

	hash := sha256.Sum256(bodyBytes)

	usage, err := completionResp.GetUsage()
	if err != nil {
		return nil, fmt.Errorf("get usage: %w", err)
	}

	// Store the canonicalized ORIGINAL prompt (not the modified one with seed).
	promptPayload, err := subnet.CanonicalizeJSON(req.Prompt)
	if err != nil {
		return nil, fmt.Errorf("canonicalize prompt: %w", err)
	}

	storageKey := SubnetPayloadKey(req.EscrowID, req.InferenceID)
	epochID := currentEpochID(e.phaseTracker)
	if err := e.payloadStore.Store(ctx, storageKey, epochID, promptPayload, bodyBytes); err != nil {
		return nil, fmt.Errorf("store payloads: %w", err)
	}

	return &subnet.ExecuteResult{
		ResponseHash: hash[:],
		InputTokens:  usage.PromptTokens,
		OutputTokens: usage.CompletionTokens,
		ResponseBody: bodyBytes,
	}, nil
}

// SubnetPayloadKey creates a namespaced storage key for subnet payloads.
// Format: "subnet:<escrowID>:<inferenceID>" to prevent cross-session collisions.
func SubnetPayloadKey(escrowID string, inferenceID uint64) string {
	return fmt.Sprintf("subnet:%s:%d", escrowID, inferenceID)
}

// Compile-time check.
var _ subnet.InferenceEngine = (*EngineAdapter)(nil)
