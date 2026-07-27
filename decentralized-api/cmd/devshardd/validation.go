package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"

	internaldevshard "decentralized-api/internal/devshard"

	devshardpkg "devshard"
	"devshard/bridge"
	mlnodeclient "devshard/mlnode"
	"devshard/observability"
)

// devshardValidator implements devshard.ValidationEngine for the standalone
// devshardd binary. It differs from the old embedded dapi validator in two ways:
//   - node acquisition uses NodeManager gRPC (no broker)
//   - the payload-store epoch comes from the mainnet-pinned escrow epoch
type devshardValidator struct {
	mlClient       *mlnodeclient.Client
	httpClient     *http.Client
	bridge         bridge.MainnetBridge
	recorder       internaldevshard.PayloadAuthClient
	engine         *devshardEngine // reused for doWithLockedNode retry loop
	chainParams    internaldevshard.ChainParamsProvider
	thresholds     *internaldevshard.ValidationThresholdResolver
	runtimeVersion string
}

func newDevshardValidator(
	mlClient *mlnodeclient.Client,
	httpClient *http.Client,
	runtimeVersion string,
	br bridge.MainnetBridge,
	recorder internaldevshard.PayloadAuthClient,
	engine *devshardEngine,
	chainParams internaldevshard.ChainParamsProvider,
) *devshardValidator {
	return &devshardValidator{
		mlClient:       mlClient,
		httpClient:     httpClient,
		bridge:         br,
		recorder:       recorder,
		engine:         engine,
		chainParams:    chainParams,
		thresholds:     internaldevshard.NewValidationThresholdResolver(br, internaldevshard.ValidationThresholdCacheTTL),
		runtimeVersion: runtimeVersion,
	}
}

func (v *devshardValidator) Validate(ctx context.Context, req devshardpkg.ValidateRequest) (*devshardpkg.ValidateResult, error) {
	return internaldevshard.ValidateInferenceWithExecutor(
		ctx,
		req,
		v.httpClient,
		v.bridge,
		v.recorder,
		req.EpochID,
		v.sessionPayloadPath(req),
		func(ctx context.Context, model string, body []byte) (*http.Response, error) {
			return v.executeMLRequest(ctx, model, req.EscrowID, body)
		},
		"devshardd",
		v.chainParams,
		v.thresholds,
	)
}

func (v *devshardValidator) sessionPayloadPath(req devshardpkg.ValidateRequest) string {
	return devshardpkg.VersionedSessionPayloadPath(v.runtimeVersion, req.EscrowID)
}

func (v *devshardValidator) executeMLRequest(ctx context.Context, model, escrowID string, body []byte) (*http.Response, error) {
	resp, err := v.engine.doWithLockedNode(ctx, observability.PathValidate, model, escrowID, func(endpoint string) (*http.Response, error) {
		url := endpoint + "/v1/chat/completions"
		httpReq, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if reqErr != nil {
			return nil, reqErr
		}
		httpReq.Header.Set("Content-Type", "application/json")
		observability.InjectRequestContext(ctx, httpReq.Header)
		observability.AttachRequestID(httpReq)
		return v.httpClient.Do(httpReq)
	})
	if err != nil {
		return nil, fmt.Errorf("validate inference: %w", err)
	}
	return resp, nil
}

// Compile-time check.
var _ devshardpkg.ValidationEngine = (*devshardValidator)(nil)
