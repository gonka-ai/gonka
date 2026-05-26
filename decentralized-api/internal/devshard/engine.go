package devshard

import (
	"bytes"
	"context"
	"fmt"
	"net/http"

	"decentralized-api/broker"
	"decentralized-api/chainphase"
	"decentralized-api/payloadstorage"

	"devshard"
	devshardserver "devshard/server"
)

// EngineAdapter implements devshard.InferenceEngine by delegating to broker and completionapi.
type EngineAdapter struct {
	broker       *broker.Broker
	nodeVersion  string
	payloadStore payloadstorage.PayloadStorage
	phaseTracker *chainphase.ChainPhaseTracker
	httpClient   *http.Client
	chainParams  ChainParamsProvider
}

func NewEngineAdapter(
	b *broker.Broker,
	nodeVersion string,
	ps payloadstorage.PayloadStorage,
	phaseTracker *chainphase.ChainPhaseTracker,
	httpClient *http.Client,
	chainParams ChainParamsProvider,
) *EngineAdapter {
	return &EngineAdapter{
		broker:       b,
		nodeVersion:  nodeVersion,
		payloadStore: ps,
		phaseTracker: phaseTracker,
		httpClient:   httpClient,
		chainParams:  chainParams,
	}
}

func (e *EngineAdapter) Execute(ctx context.Context, req devshard.ExecuteRequest) (*devshard.ExecuteResult, error) {
	return ExecuteInferenceWithExecutor(
		ctx,
		req,
		e.payloadStore,
		req.EpochID,
		e.executeMLRequest,
		e.chainParams,
	)
}

func (e *EngineAdapter) executeMLRequest(ctx context.Context, model string, body []byte) (*http.Response, error) {
	resp, err := broker.DoWithLockedNodeHTTPRetry(e.broker, model, nil, 3,
		func(node *broker.Node) (*http.Response, *broker.ActionError) {
			url := node.InferenceUrlWithVersion(e.nodeVersion) + "/v1/chat/completions"
			httpReq, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
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
	return resp, nil
}

// DevshardPayloadKey creates a namespaced storage key for devshard payloads.
// Format: "devshard:<escrowID>:<inferenceID>" to prevent cross-session collisions.
func DevshardPayloadKey(escrowID string, inferenceID uint64) string {
	return devshardserver.PayloadKey(escrowID, inferenceID)
}

// Compile-time check.
var _ devshard.InferenceEngine = (*EngineAdapter)(nil)
