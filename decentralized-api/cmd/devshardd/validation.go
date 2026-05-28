package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"sync"

	internaldevshard "decentralized-api/internal/devshard"

	chaintypes "github.com/productscience/inference/x/inference/types"

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

	contextMu      sync.Mutex
	contextWindows map[string]uint64
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
		mlClient:       mlClient,
		httpClient:     httpClient,
		bridge:         br,
		recorder:       recorder,
		engine:         engine,
		chainParams:    chainParams,
		contextWindows: map[string]uint64{},
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
		v.modelContextWindow(ctx, req.Model),
	)
}

// modelContextWindow returns the chain context_window for the given model id.
// Known models are cached for the process lifetime; unknown models cause a
// fresh ModelsAll query so newly-added governance models get picked up
// without a restart.
func (v *devshardValidator) modelContextWindow(ctx context.Context, model string) uint64 {
	v.contextMu.Lock()
	if cw, ok := v.contextWindows[model]; ok {
		v.contextMu.Unlock()
		return cw
	}
	v.contextMu.Unlock()

	resp, err := v.recorder.NewInferenceQueryClient().ModelsAll(ctx, &chaintypes.QueryModelsAllRequest{})
	if err != nil {
		return 0
	}
	v.contextMu.Lock()
	defer v.contextMu.Unlock()
	for _, m := range resp.Model {
		if m.Id != "" {
			v.contextWindows[m.Id] = m.ContextWindow
		}
	}
	return v.contextWindows[model]
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
