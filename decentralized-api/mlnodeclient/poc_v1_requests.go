package mlnodeclient

import (
	"context"
	"decentralized-api/utils"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/productscience/inference/testenv"
	"github.com/productscience/inference/x/inference/types"
)

// V1 PoC endpoints (different path prefix than V2)
const (
	InitGeneratePathV1  = "/api/v1/pow/init/generate"
	InitValidatePathV1  = "/api/v1/pow/init/validate"
	ValidateBatchPathV1 = "/api/v1/pow/validate"
	PowStatusPathV1     = "/api/v1/pow/status"

	DefaultRTarget        = 1.398077
	DefaultBatchSize      = 100
	DefaultFraudThreshold = 1e-7
)

// InitDtoV1 is the request body for V1 PoC init endpoints.
type InitDtoV1 struct {
	BlockHash      string    `json:"block_hash"`
	BlockHeight    int64     `json:"block_height"`
	PublicKey      string    `json:"public_key"`
	BatchSize      int       `json:"batch_size"`
	RTarget        float64   `json:"r_target"`
	FraudThreshold float64   `json:"fraud_threshold"`
	Params         *ParamsV1 `json:"params"`
	NodeNum        uint64    `json:"node_id"`
	TotalNodes     int64     `json:"node_count"`
	URL            string    `json:"url"`
}

// ParamsV1 contains V1 model parameters.
type ParamsV1 struct {
	Dim              int     `json:"dim"`
	NLayers          int     `json:"n_layers"`
	NHeads           int     `json:"n_heads"`
	NKVHeads         int     `json:"n_kv_heads"`
	VocabSize        int     `json:"vocab_size"`
	FFNDimMultiplier float64 `json:"ffn_dim_multiplier"`
	MultipleOf       int     `json:"multiple_of"`
	NormEps          float64 `json:"norm_eps"`
	RopeTheta        int     `json:"rope_theta"`
	UseScaledRope    bool    `json:"use_scaled_rope"`
	SeqLen           int     `json:"seq_len"`
}

// Default V1 model params for different environments.
var (
	DefaultParamsV1 = ParamsV1{
		Dim:              512,
		NLayers:          64,
		NHeads:           128,
		NKVHeads:         128,
		VocabSize:        8192,
		FFNDimMultiplier: 16.0,
		MultipleOf:       1024,
		NormEps:          1e-05,
		RopeTheta:        500000,
		UseScaledRope:    true,
		SeqLen:           4,
	}

	TestNetParamsV1 = ParamsV1{
		Dim:              1024,
		NLayers:          32,
		NHeads:           32,
		NKVHeads:         32,
		VocabSize:        8196,
		FFNDimMultiplier: 10.0,
		MultipleOf:       2048,
		NormEps:          1e-5,
		RopeTheta:        10000,
		UseScaledRope:    false,
		SeqLen:           128,
	}

	MainNetParamsV1 = ParamsV1{
		Dim:              1792,
		NLayers:          64,
		NHeads:           64,
		NKVHeads:         64,
		VocabSize:        8196,
		FFNDimMultiplier: 10.0,
		MultipleOf:       4 * 2048,
		NormEps:          1e-5,
		RopeTheta:        10000,
		UseScaledRope:    false,
		SeqLen:           256,
	}
)

// PowStateV1 represents the V1 PoW status enum.
type PowStateV1 string

const (
	PowStateV1Idle         PowStateV1 = "IDLE"
	PowStateV1NoController PowStateV1 = "NOT_LOADED"
	PowStateV1Loading      PowStateV1 = "LOADING"
	PowStateV1Generating   PowStateV1 = "GENERATING"
	PowStateV1Validating   PowStateV1 = "VALIDATING"
	PowStateV1Stopped      PowStateV1 = "STOPPED"
	PowStateV1Mixed        PowStateV1 = "MIXED"
)

// PowStatusResponseV1 is the response from V1 PoW status endpoint.
type PowStatusResponseV1 struct {
	Status             PowStateV1 `json:"status"`
	IsModelInitialized bool       `json:"is_model_initialized"`
}

// ProofBatchV1 is the V1 batch format from MLNode callbacks.
type ProofBatchV1 struct {
	PublicKey   string    `json:"public_key"`
	BlockHash   string    `json:"block_hash"`
	BlockHeight int64     `json:"block_height"`
	Nonces      []int64   `json:"nonces"`
	Dist        []float64 `json:"dist"`
	NodeNum     uint64    `json:"node_id"`
}

// ValidatedBatchV1 is the V1 validation result from MLNode callbacks.
type ValidatedBatchV1 struct {
	ProofBatchV1

	ReceivedDist      []float64 `json:"received_dist"`
	RTarget           float64   `json:"r_target"`
	FraudThreshold    float64   `json:"fraud_threshold"`
	NInvalid          int64     `json:"n_invalid"`
	ProbabilityHonest float64   `json:"probability_honest"`
	FraudDetected     bool      `json:"fraud_detected"`
}

// convertChainParamsV1 converts chain PoCModelParams to V1 Params struct.
func convertChainParamsV1(chainParams *types.PoCModelParams) *ParamsV1 {
	if chainParams == nil {
		return nil
	}
	return &ParamsV1{
		Dim:              int(chainParams.Dim),
		NLayers:          int(chainParams.NLayers),
		NHeads:           int(chainParams.NHeads),
		NKVHeads:         int(chainParams.NKvHeads),
		VocabSize:        int(chainParams.VocabSize),
		FFNDimMultiplier: chainParams.FfnDimMultiplier.ToFloat(),
		MultipleOf:       int(chainParams.MultipleOf),
		NormEps:          chainParams.NormEps.ToFloat(),
		RopeTheta:        int(chainParams.RopeTheta),
		UseScaledRope:    chainParams.UseScaledRope,
		SeqLen:           int(chainParams.SeqLen),
	}
}

// getRTargetV1 extracts RTarget from chain params or returns default.
func getRTargetV1(chainParams *types.PoCModelParams) float64 {
	if chainParams != nil && chainParams.RTarget != nil {
		return chainParams.RTarget.ToFloat()
	}
	return DefaultRTarget
}

// BuildInitDtoV1 constructs an InitDtoV1 for V1 PoC operations.
func BuildInitDtoV1(blockHeight int64, pubKey string, totalNodes int64, nodeNum uint64, blockHash, callbackUrl string, chainModelParams *types.PoCModelParams) InitDtoV1 {
	var params *ParamsV1
	if testenv.IsTestNet() {
		params = &TestNetParamsV1
	} else if chainModelParams != nil {
		params = convertChainParamsV1(chainModelParams)
	} else {
		params = &MainNetParamsV1
	}

	return InitDtoV1{
		BlockHeight:    blockHeight,
		BlockHash:      blockHash,
		PublicKey:      pubKey,
		BatchSize:      DefaultBatchSize,
		RTarget:        getRTargetV1(chainModelParams),
		FraudThreshold: DefaultFraudThreshold,
		Params:         params,
		URL:            callbackUrl,
		TotalNodes:     totalNodes,
		NodeNum:        nodeNum,
	}
}

// InitGenerateV1 starts V1 PoC generation on the MLNode.
func (c *Client) InitGenerateV1(ctx context.Context, dto InitDtoV1) error {
	requestUrl, err := url.JoinPath(c.pocUrl, InitGeneratePathV1)
	if err != nil {
		return err
	}

	_, err = utils.SendPostJsonRequest(ctx, &c.client, requestUrl, dto)
	return err
}

// InitValidateV1 starts V1 PoC validation mode on the MLNode.
func (c *Client) InitValidateV1(ctx context.Context, dto InitDtoV1) error {
	requestUrl, err := url.JoinPath(c.pocUrl, InitValidatePathV1)
	if err != nil {
		return err
	}

	_, err = utils.SendPostJsonRequest(ctx, &c.client, requestUrl, dto)
	return err
}

// ValidateBatchV1 sends a batch to the MLNode for V1 validation.
func (c *Client) ValidateBatchV1(ctx context.Context, batch ProofBatchV1) error {
	requestUrl, err := url.JoinPath(c.pocUrl, ValidateBatchPathV1)
	if err != nil {
		return err
	}

	_, err = utils.SendPostJsonRequest(ctx, &c.client, requestUrl, batch)
	return err
}

// GetPowStatusV1 retrieves the V1 PoW status from the MLNode.
func (c *Client) GetPowStatusV1(ctx context.Context) (*PowStatusResponseV1, error) {
	requestUrl, err := url.JoinPath(c.pocUrl, PowStatusPathV1)
	if err != nil {
		return nil, err
	}

	resp, err := utils.SendGetRequest(ctx, &c.client, requestUrl)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GetPowStatusV1: unexpected status code: %d", resp.StatusCode)
	}

	var powResp PowStatusResponseV1
	if err := json.NewDecoder(resp.Body).Decode(&powResp); err != nil {
		return nil, err
	}

	return &powResp, nil
}
