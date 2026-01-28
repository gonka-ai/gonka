package mlnodeclient

import (
	"context"
	"decentralized-api/utils"
	"encoding/json"
	"fmt"
	"io"
	"net/url"

	"github.com/productscience/inference/x/inference/types"
)

// PoCParamsV2 contains model-specific parameters for PoC v2 generation/validation.
type PoCParamsV2 struct {
	Model  string `json:"model"`
	SeqLen int64  `json:"seq_len"`
	// k_dim is intentionally omitted - MLNode will use its default
}

// PoCInitGenerateRequestV2 represents the request body for /api/v1/inference/pow/init/generate.
type PoCInitGenerateRequestV2 struct {
	BlockHash   string      `json:"block_hash"`
	BlockHeight int64       `json:"block_height"`
	PublicKey   string      `json:"public_key"`
	NodeId      int         `json:"node_id"`
	NodeCount   int         `json:"node_count"`
	Params      PoCParamsV2 `json:"params"`
	URL         string      `json:"url,omitempty"`
	// batch_size is intentionally omitted - MLNode will use its default
}

// PoCGenerateRequestV2 represents the request body for /api/v1/inference/pow/generate.
// Used for both generation (nonces only) and validation (with validation.artifacts).
type PoCGenerateRequestV2 struct {
	BlockHash   string            `json:"block_hash"`
	BlockHeight int64             `json:"block_height"`
	PublicKey   string            `json:"public_key"`
	NodeId      int               `json:"node_id"`
	NodeCount   int               `json:"node_count"`
	Nonces      []int64           `json:"nonces"`
	Params      PoCParamsV2       `json:"params"`
	Wait        bool              `json:"wait,omitempty"`
	URL         string            `json:"url,omitempty"`
	Validation  *ValidationV2     `json:"validation,omitempty"`
	StatTest    *StatTestParamsV2 `json:"stat_test,omitempty"`
	// batch_size is intentionally omitted - MLNode will use its default
}

// ValidationV2 contains artifacts to validate for PoC v2.
type ValidationV2 struct {
	Artifacts []ArtifactV2 `json:"artifacts"`
}

// StatTestParamsV2 contains optional statistical test parameters for validation.
type StatTestParamsV2 struct {
	DistThreshold   float64 `json:"dist_threshold,omitempty"`
	PMismatch       float64 `json:"p_mismatch,omitempty"`
	PValueThreshold float64 `json:"p_value_threshold,omitempty"`
}

// Default stat test parameter values.
const (
	DefaultDistThreshold   = 0.4
	DefaultPMismatch       = 0.1
	DefaultPValueThreshold = 0.05
)

// DefaultStatTestParamsV2 returns the default statistical test parameters for PoC v2 validation.
func DefaultStatTestParamsV2() *StatTestParamsV2 {
	return &StatTestParamsV2{
		DistThreshold:   DefaultDistThreshold,
		PMismatch:       DefaultPMismatch,
		PValueThreshold: DefaultPValueThreshold,
	}
}

// StatTestParamsFromChain converts chain PoCStatTestParams to API StatTestParamsV2.
// Falls back to defaults for any nil or zero values.
func StatTestParamsFromChain(chainParams *types.PoCStatTestParams) *StatTestParamsV2 {
	result := DefaultStatTestParamsV2()
	if chainParams == nil {
		return result
	}
	if chainParams.DistThreshold != nil {
		result.DistThreshold = chainParams.DistThreshold.ToFloat()
	}
	if chainParams.PMismatch != nil {
		result.PMismatch = chainParams.PMismatch.ToFloat()
	}
	if chainParams.PValueThreshold != nil {
		result.PValueThreshold = chainParams.PValueThreshold.ToFloat()
	}
	return result
}

// PoCStatusResponseV2 represents the response from /api/v1/inference/pow/status.
type PoCStatusResponseV2 struct {
	Status   string            `json:"status"` // "IDLE", "GENERATING", "MIXED", "NO_BACKENDS"
	Backends []BackendStatusV2 `json:"backends,omitempty"`
}

// BackendStatusV2 represents the status of a single vLLM backend.
type BackendStatusV2 struct {
	Port   int    `json:"port"`
	Status string `json:"status"`
}

// PoCInitGenerateResponseV2 represents the response from /api/v1/inference/pow/init/generate.
type PoCInitGenerateResponseV2 struct {
	Status   string          `json:"status"`
	Backends int             `json:"backends,omitempty"`
	NGroups  int             `json:"n_groups,omitempty"`
	Results  []BackendResult `json:"results,omitempty"`
	Errors   []BackendError  `json:"errors,omitempty"`
}

// BackendResult represents a successful backend response.
type BackendResult struct {
	Port   int    `json:"port"`
	Status string `json:"status"`
}

// BackendError represents a failed backend response.
type BackendError struct {
	Port  int    `json:"port"`
	Error string `json:"error"`
}

// PoCGenerateResponseV2 represents the response from /api/v1/inference/pow/generate.
type PoCGenerateResponseV2 struct {
	Status    string `json:"status"` // "queued", "completed", etc.
	RequestId string `json:"request_id,omitempty"`
}

// PoCStopResponseV2 represents the response from /api/v1/inference/pow/stop.
type PoCStopResponseV2 struct {
	Status  string          `json:"status"`
	Results []BackendResult `json:"results,omitempty"`
	Errors  []BackendError  `json:"errors,omitempty"`
}

// InitGenerateV2 starts PoC v2 generation on the MLNode.
// This is the entry point for mining - it starts generation and artifacts are delivered via callback.
func (c *Client) InitGenerateV2(ctx context.Context, req PoCInitGenerateRequestV2) (*PoCInitGenerateResponseV2, error) {
	requestUrl, err := url.JoinPath(c.pocUrl, "/api/v1/inference/pow/init/generate")
	if err != nil {
		return nil, err
	}

	httpResp, err := utils.SendPostJsonRequest(ctx, &c.client, requestUrl, req)
	if err != nil {
		return nil, err
	}
	defer httpResp.Body.Close()

	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, err
	}

	if httpResp.StatusCode >= 400 {
		return nil, fmt.Errorf("InitGenerateV2 failed with status %d: %s", httpResp.StatusCode, string(body))
	}

	var resp PoCInitGenerateResponseV2
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// GenerateV2 triggers PoC v2 generation for specific nonces, optionally with validation artifacts.
// This is used for validation - the validator provides nonces and their expected artifacts.
func (c *Client) GenerateV2(ctx context.Context, req PoCGenerateRequestV2) (*PoCGenerateResponseV2, error) {
	requestUrl, err := url.JoinPath(c.pocUrl, "/api/v1/inference/pow/generate")
	if err != nil {
		return nil, err
	}

	httpResp, err := utils.SendPostJsonRequest(ctx, &c.client, requestUrl, req)
	if err != nil {
		return nil, err
	}
	defer httpResp.Body.Close()

	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, err
	}

	if httpResp.StatusCode >= 400 {
		return nil, fmt.Errorf("GenerateV2 failed with status %d: %s", httpResp.StatusCode, string(body))
	}

	var resp PoCGenerateResponseV2
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// GetPowStatusV2 retrieves the current PoC v2 status from the MLNode.
// This can be used to check if generation is in progress without calling Stop.
func (c *Client) GetPowStatusV2(ctx context.Context) (*PoCStatusResponseV2, error) {
	requestUrl, err := url.JoinPath(c.pocUrl, "/api/v1/inference/pow/status")
	if err != nil {
		return nil, err
	}

	httpResp, err := utils.SendGetRequest(ctx, &c.client, requestUrl)
	if err != nil {
		return nil, err
	}
	defer httpResp.Body.Close()

	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, err
	}

	if httpResp.StatusCode >= 400 {
		return nil, fmt.Errorf("GetPowStatusV2 failed with status %d: %s", httpResp.StatusCode, string(body))
	}

	var resp PoCStatusResponseV2
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// StopPowV2 stops PoC v2 generation on the MLNode.
// This fans out to all vLLM backends via the MLNode /api/v1/inference/pow/stop endpoint.
func (c *Client) StopPowV2(ctx context.Context) (*PoCStopResponseV2, error) {
	requestUrl, err := url.JoinPath(c.pocUrl, "/api/v1/inference/pow/stop")
	if err != nil {
		return nil, err
	}

	// POST with empty body
	httpResp, err := utils.SendPostJsonRequest(ctx, &c.client, requestUrl, struct{}{})
	if err != nil {
		return nil, err
	}
	defer httpResp.Body.Close()

	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, err
	}

	if httpResp.StatusCode >= 400 {
		return nil, fmt.Errorf("StopPowV2 failed with status %d: %s", httpResp.StatusCode, string(body))
	}

	var resp PoCStopResponseV2
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
