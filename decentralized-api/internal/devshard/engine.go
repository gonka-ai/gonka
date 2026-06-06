package devshard

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"time"

	"decentralized-api/broker"
	"decentralized-api/chainphase"
	"decentralized-api/payloadstorage"

	"devshard"
	"devshard/observability"
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
	lastReason := observability.ReasonAcquireErr
	resp, err := broker.DoWithLockedNodeHTTPRetry(e.broker, model, nil, 3,
		func(node *broker.Node) (*http.Response, *broker.ActionError) {
			url := node.InferenceUrlWithVersion(e.nodeVersion) + "/v1/chat/completions"
			started := time.Now()
			httpReq, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
			if reqErr != nil {
				lastReason = observability.ReasonApplicationErr
				observability.IncMLNodeAttempt(observability.PathExecute, lastReason, node.Id)
				return nil, broker.NewApplicationActionError(reqErr)
			}
			httpReq.Header.Set("Content-Type", "application/json")
			observability.InjectRequestContext(ctx, httpReq.Header)
			observability.AttachRequestID(httpReq)
			httpResp, postErr := e.httpClient.Do(httpReq)
			if postErr == nil {
				observability.ObserveMLNodeCall(observability.PathExecute, node.Id, observability.MetricPhaseTotal, started)
			}
			lastReason = observability.ClassifyMLNodeHTTP(httpResp, postErr, ctx.Err())
			observability.IncMLNodeAttempt(observability.PathExecute, lastReason, node.Id)
			if postErr != nil {
				return nil, broker.NewTransportActionError(postErr)
			}
			return httpResp, nil
		},
	)
	if err != nil {
		if lastReason == observability.ReasonOK {
			lastReason = observability.ReasonTransportErr
		}
		return nil, observability.Classify(lastReason, observability.WhereEngineMLNodeCall, fmt.Errorf("broker execute: %w", err))
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
