package inference

import (
	"bytes"
	"context"
	"fmt"
	"net/http"

	"common/chain"
	devshardpkg "devshard"
	"devshard/bridge"
)

// Validator implements devshard.ValidationEngine for the standalone devshardd binary.
// It reuses Engine.doWithLockedNode for node acquisition.
type Validator struct {
	bridge       bridge.MainnetBridge
	recorder     PayloadAuthClient
	engine       *Engine
	phase        *chain.Phase
	boundVersion string
}

// NewValidator creates a Validator. boundVersion is the runtime version string used
// to construct the payload request path.
func NewValidator(
	br bridge.MainnetBridge,
	recorder PayloadAuthClient,
	engine *Engine,
	phase *chain.Phase,
	boundVersion string,
) *Validator {
	return &Validator{
		bridge:       br,
		recorder:     recorder,
		engine:       engine,
		phase:        phase,
		boundVersion: boundVersion,
	}
}

func (v *Validator) Validate(ctx context.Context, req devshardpkg.ValidateRequest) (*devshardpkg.ValidateResult, error) {
	return validateInference(
		ctx,
		req,
		v.bridge,
		v.recorder,
		v.phase.EpochID(),
		devshardpkg.VersionedSessionPayloadPath(v.boundVersion, req.EscrowID),
		v.executeMLRequest,
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
		return v.engine.httpClient.Do(httpReq)
	})
	if err != nil {
		return nil, fmt.Errorf("validate inference: %w", err)
	}
	return resp, nil
}

var _ devshardpkg.ValidationEngine = (*Validator)(nil)
