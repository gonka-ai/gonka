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
)

// devshardValidator implements devshard.ValidationEngine for the standalone
// devshardd binary. Same shape as dapi's in-process ValidationAdapter; the
// only structural differences are:
//   - node acquisition uses NodeManager gRPC (no broker)
//   - the payload-store epoch is fixed to 0 (devshardd has no phase tracker)
type devshardValidator struct {
	mlClient    *mlnodeclient.Client
	httpClient  *http.Client
	bridge      bridge.MainnetBridge
	recorder    internaldevshard.PayloadAuthClient
	engine      *devshardEngine // reused for doWithLockedNode retry loop
	chainParams internaldevshard.ChainParamsProvider
}

func newDevshardValidator(
	mlClient *mlnodeclient.Client,
	httpClient *http.Client,
	br bridge.MainnetBridge,
	recorder internaldevshard.PayloadAuthClient,
	engine *devshardEngine,
	chainParams internaldevshard.ChainParamsProvider,
) *devshardValidator {
	return &devshardValidator{
		mlClient:    mlClient,
		httpClient:  httpClient,
		bridge:      br,
		recorder:    recorder,
		engine:      engine,
		chainParams: chainParams,
	}
}

func (v *devshardValidator) Validate(ctx context.Context, req devshardpkg.ValidateRequest) (*devshardpkg.ValidateResult, error) {
	return internaldevshard.ValidateInferenceWithExecutor(
		ctx,
		req,
		v.httpClient,
		v.bridge,
		v.recorder,
		0,
		devshardpkg.VersionedSessionPayloadPath(Version, req.EscrowID),
		v.executeMLRequest,
		"devshardd",
		v.chainParams,
	)
}

func (v *devshardValidator) executeMLRequest(ctx context.Context, model string, body []byte) (*http.Response, error) {
	resp, err := v.engine.doWithLockedNode(ctx, model, func(endpoint string) (*http.Response, error) {
		url := endpoint + "/v1/chat/completions"
		httpReq, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if reqErr != nil {
			return nil, reqErr
		}
		httpReq.Header.Set("Content-Type", "application/json")
		return v.httpClient.Do(httpReq)
	})
	if err != nil {
		return nil, fmt.Errorf("validate inference: %w", err)
	}
	return resp, nil
}

// Compile-time check.
var _ devshardpkg.ValidationEngine = (*devshardValidator)(nil)
