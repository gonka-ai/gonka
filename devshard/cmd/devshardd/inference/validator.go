package inference

import (
	"bytes"
	"context"
	"fmt"
	"net/http"

	mlnodeclient "common/nodemanager"
	devshardpkg "devshard"
	"devshard/bridge"
)

// Validator implements devshard.ValidationEngine for the standalone devshardd binary.
// It reuses Engine.doWithLockedNode for node acquisition.
type Validator struct {
	mlClient     *mlnodeclient.Client
	httpClient   *http.Client
	bridge       bridge.MainnetBridge
	recorder     PayloadAuthClient
	engine       *Engine
	chainParams  ChainParamsProvider
	boundVersion string
}

// NewValidator creates a Validator. boundVersion is the runtime version string used
// to construct the payload request path.
func NewValidator(
	mlClient *mlnodeclient.Client,
	httpClient *http.Client,
	br bridge.MainnetBridge,
	recorder PayloadAuthClient,
	engine *Engine,
	chainParams ChainParamsProvider,
	boundVersion string,
) *Validator {
	return &Validator{
		mlClient:     mlClient,
		httpClient:   httpClient,
		bridge:       br,
		recorder:     recorder,
		engine:       engine,
		chainParams:  chainParams,
		boundVersion: boundVersion,
	}
}

func (v *Validator) Validate(ctx context.Context, req devshardpkg.ValidateRequest) (*devshardpkg.ValidateResult, error) {
	return validateInference(
		ctx,
		req,
		v.httpClient,
		v.bridge,
		v.recorder,
		0,
		devshardpkg.VersionedSessionPayloadPath(v.boundVersion, req.EscrowID),
		v.executeMLRequest,
		"devshardd",
		v.chainParams,
	)
}

func (v *Validator) executeMLRequest(ctx context.Context, model string, body []byte) (*http.Response, error) {
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

var _ devshardpkg.ValidationEngine = (*Validator)(nil)
