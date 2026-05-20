package devshard

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"

	"decentralized-api/broker"
	"decentralized-api/chainphase"
	"decentralized-api/payloadstorage"

	"devshard"
	devshardserver "devshard/server"
	chaintypes "github.com/productscience/inference/x/inference/types"
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
		if errors.Is(err, broker.ErrNoNodesAvailable) && e.isPoCActive() {
			return nil, devshard.NewUnavailableFinishError(devshard.FinishReason_FINISH_REASON_NO_PRESERVE_NODES, err)
		}
		return nil, fmt.Errorf("broker execute: %w", err)
	}
	return resp, nil
}

func (e *EngineAdapter) isPoCActive() bool {
	if e.phaseTracker == nil {
		return false
	}
	state := e.phaseTracker.GetCurrentEpochState()
	if state == nil || !state.IsSynced {
		return false
	}
	switch state.CurrentPhase {
	case chaintypes.PoCGeneratePhase, chaintypes.PoCGenerateWindDownPhase, chaintypes.PoCValidatePhase:
		return true
	default:
		return state.ActiveConfirmationPoCEvent != nil
	}
}

// DevshardPayloadKey creates a namespaced storage key for devshard payloads.
// Format: "devshard:<escrowID>:<inferenceID>" to prevent cross-session collisions.
func DevshardPayloadKey(escrowID string, inferenceID uint64) string {
	return devshardserver.PayloadKey(escrowID, inferenceID)
}

// Compile-time check.
var _ devshard.InferenceEngine = (*EngineAdapter)(nil)
