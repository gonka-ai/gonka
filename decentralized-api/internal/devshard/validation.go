package devshard

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"time"

	"decentralized-api/broker"
	"decentralized-api/chainphase"

	"devshard"
	"devshard/bridge"
	"devshard/observability"
)

// ValidationAdapter implements devshard.ValidationEngine by re-executing inference
// with enforced tokens and comparing logits.
type ValidationAdapter struct {
	broker       *broker.Broker
	nodeVersion  string
	phaseTracker *chainphase.ChainPhaseTracker
	httpClient   *http.Client
	bridge       bridge.MainnetBridge
	recorder     PayloadAuthClient
	chainParams  ChainParamsProvider
	thresholds   *ValidationThresholdResolver
}

func NewValidationAdapter(
	b *broker.Broker,
	nodeVersion string,
	phaseTracker *chainphase.ChainPhaseTracker,
	httpClient *http.Client,
	br bridge.MainnetBridge,
	recorder PayloadAuthClient,
	chainParams ChainParamsProvider,
) *ValidationAdapter {
	return &ValidationAdapter{
		broker:       b,
		nodeVersion:  nodeVersion,
		phaseTracker: phaseTracker,
		httpClient:   httpClient,
		bridge:       br,
		recorder:     recorder,
		chainParams:  chainParams,
		thresholds:   NewValidationThresholdResolver(br, ValidationThresholdCacheTTL),
	}
}

func (v *ValidationAdapter) Validate(ctx context.Context, req devshard.ValidateRequest) (*devshard.ValidateResult, error) {
	return ValidateInferenceWithExecutor(
		ctx,
		req,
		v.httpClient,
		v.bridge,
		v.recorder,
		req.EpochID,
		devshard.LegacySessionPayloadPath(req.EscrowID),
		v.executeMLRequest,
		"devshard",
		v.chainParams,
		v.thresholds,
	)
}

func (v *ValidationAdapter) executeMLRequest(ctx context.Context, model string, body []byte) (*http.Response, error) {
	lastReason := observability.ReasonAcquireErr
	resp, err := broker.DoWithLockedNodeHTTPRetry(v.broker, model, nil, 3,
		func(node *broker.Node) (*http.Response, *broker.ActionError) {
			url := node.InferenceUrlWithVersion(v.nodeVersion) + "/v1/chat/completions"
			started := time.Now()
			httpReq, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
			if reqErr != nil {
				lastReason = observability.ReasonApplicationErr
				observability.IncMLNodeAttempt(observability.PathValidate, lastReason, node.Id)
				return nil, broker.NewApplicationActionError(reqErr)
			}
			httpReq.Header.Set("Content-Type", "application/json")
			observability.InjectRequestContext(ctx, httpReq.Header)
			observability.AttachRequestID(httpReq)
			httpResp, postErr := v.httpClient.Do(httpReq)
			if postErr == nil {
				observability.ObserveMLNodeCall(observability.PathValidate, node.Id, observability.MetricPhaseTotal, started)
			}
			lastReason = observability.ClassifyMLNodeHTTP(httpResp, postErr, ctx.Err())
			observability.IncMLNodeAttempt(observability.PathValidate, lastReason, node.Id)
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
		return nil, observability.Classify(lastReason, observability.WhereEngineMLNodeCall, fmt.Errorf("broker validate: %w", err))
	}
	return resp, nil
}

// Compile-time check.
var _ devshard.ValidationEngine = (*ValidationAdapter)(nil)
