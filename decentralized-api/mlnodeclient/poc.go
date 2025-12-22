package mlnodeclient

import (
	"context"
	"crypto/sha256"
	"decentralized-api/utils"
	"encoding/binary"
	"fmt"
	"math/rand"
	"net/url"

	"github.com/productscience/inference/testenv"
	"github.com/productscience/inference/x/inference/types"
)

const (
	InitGeneratePath  = "/api/v1/pow/init/generate"
	InitValidatePath  = "/api/v1/pow/init/validate"
	ValidateBatchPath = "/api/v1/pow/validate"

	DefaultRTarget        = 1.398077
	DefaultBatchSize      = 100
	DefaultFraudThreshold = 1e-7
)

type InitDto struct {
	BlockHash      string  `json:"block_hash"`
	BlockHeight    int64   `json:"block_height"`
	PublicKey      string  `json:"public_key"`
	BatchSize      int     `json:"batch_size"`
	RTarget        float64 `json:"r_target"`
	FraudThreshold float64 `json:"fraud_threshold"`
	Params         *Params `json:"params"`
	NodeNum        uint64  `json:"node_id"`
	TotalNodes     int64   `json:"node_count"`
	URL            string  `json:"url"`
}

// getDefaultParams returns the default PoC model params based on environment.
// TODO: use genesis-overrides for TestNet instead of hardcoded values
func getDefaultParams() *Params {
	if testenv.IsTestNet() {
		return &TestNetParams
	}
	return &MainNetParams
}

// convertChainParams converts chain PoCModelParams to the local Params struct.
func convertChainParams(chainParams *types.PoCModelParams) *Params {
	if chainParams == nil {
		return nil
	}
	return &Params{
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

// getRTarget extracts RTarget from chain params or returns default.
func getRTarget(chainParams *types.PoCModelParams) float64 {
	if chainParams != nil && chainParams.RTarget != nil {
		return chainParams.RTarget.ToFloat()
	}
	return DefaultRTarget
}

func BuildInitDto(blockHeight int64, pubKey string, totalNodes int64, nodeNum uint64, blockHash, callbackUrl string, chainModelParams *types.PoCModelParams) InitDto {
	var params *Params
	if testenv.IsTestNet() {
		// TODO: use genesis-overrides for TestNet instead of hardcoded values
		params = &TestNetParams
	} else if chainModelParams != nil {
		params = convertChainParams(chainModelParams)
	} else {
		params = &MainNetParams // fallback
	}

	return InitDto{
		BlockHeight:    blockHeight,
		BlockHash:      blockHash,
		PublicKey:      pubKey,
		BatchSize:      DefaultBatchSize,
		RTarget:        getRTarget(chainModelParams),
		FraudThreshold: DefaultFraudThreshold,
		Params:         params,
		URL:            callbackUrl,
		TotalNodes:     totalNodes,
		NodeNum:        nodeNum,
	}
}

type Params struct {
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

var DefaultParams = Params{
	Dim:              512,
	NLayers:          64,
	NHeads:           128,
	NKVHeads:         128,
	VocabSize:        8192,
	FFNDimMultiplier: 16.0,
	MultipleOf:       1024,
	NormEps:          1e-05,
	RopeTheta:        500000.0,
	UseScaledRope:    true,
	SeqLen:           4,
}

var DevTestParams = Params{
	Dim:              512,
	NLayers:          16,
	NHeads:           16,
	NKVHeads:         16,
	VocabSize:        8192,
	FFNDimMultiplier: 1.3,
	MultipleOf:       1024,
	NormEps:          1e-05,
	RopeTheta:        500000.0,
	UseScaledRope:    true,
	SeqLen:           4,
}

var TestNetParams = Params{
	Dim:              1024,
	NLayers:          32,
	NHeads:           32,
	NKVHeads:         32,
	VocabSize:        8196,
	FFNDimMultiplier: 10.0,
	MultipleOf:       2048, // 8*256
	NormEps:          1e-5,
	RopeTheta:        10000.0,
	UseScaledRope:    false,
	SeqLen:           128,
}

var MainNetParams = Params{
	Dim:              1792,
	NLayers:          64,
	NHeads:           64,
	NKVHeads:         64,
	VocabSize:        8196,
	FFNDimMultiplier: 10.0,
	MultipleOf:       4 * 2048,
	NormEps:          1e-5,
	RopeTheta:        10000.0,
	UseScaledRope:    false,
	SeqLen:           256,
}

type ProofBatch struct {
	PublicKey   string    `json:"public_key"`
	BlockHash   string    `json:"block_hash"`
	BlockHeight int64     `json:"block_height"`
	Nonces      []int64   `json:"nonces"`
	Dist        []float64 `json:"dist"`
	NodeNum     uint64    `json:"node_id"`
}

type ValidatedBatch struct {
	ProofBatch // Inherits from ProofBatch

	// New fields
	ReceivedDist      []float64 `json:"received_dist"`
	RTarget           float64   `json:"r_target"`
	FraudThreshold    float64   `json:"fraud_threshold"`
	NInvalid          int64     `json:"n_invalid"`
	ProbabilityHonest float64   `json:"probability_honest"`
	FraudDetected     bool      `json:"fraud_detected"`
}

// This sample doesn't have to be cryptographically secure as it's only used for sampling nonces to validate.
// If it can't be reproduced on another machine, it's also not causing any harm as it's not validated on-chain.
func (pb ProofBatch) SampleNoncesToValidate(
	validatorPublicKey string,
	nNonces int64,
) ProofBatch {
	totalNonces := int64(len(pb.Nonces))
	if nNonces >= totalNonces {
		return pb
	}

	nonceIndexes := deterministicSampleIndices(
		validatorPublicKey,
		pb.BlockHash,
		pb.BlockHeight,
		nNonces,
		totalNonces,
	)

	sampledNonces := make([]int64, nNonces)
	sampledDist := make([]float64, nNonces)

	for i, idx := range nonceIndexes {
		sampledNonces[i] = pb.Nonces[idx]
		sampledDist[i] = pb.Dist[idx]
	}

	return ProofBatch{
		PublicKey:   pb.PublicKey,
		BlockHash:   pb.BlockHash,
		BlockHeight: pb.BlockHeight,
		Nonces:      sampledNonces,
		Dist:        sampledDist,
	}
}

func deterministicSampleIndices(
	validatorPublicKey string,
	blockHash string,
	blockHeight int64,
	nSamples int64,
	totalItems int64,
) []int {
	if nSamples >= totalItems {
		indices := make([]int, totalItems)
		for i := int64(0); i < totalItems; i++ {
			indices[i] = int(i)
		}
		return indices
	}

	seedInput := fmt.Sprintf("%s:%s:%d", validatorPublicKey, blockHash, blockHeight)
	hash := sha256.Sum256([]byte(seedInput))
	seed := int64(binary.BigEndian.Uint64(hash[:8]))

	source := rand.NewSource(seed)
	rng := rand.New(source)
	indices := rng.Perm(int(totalItems))[:nSamples]

	return indices
}

func (api *Client) InitGenerate(context context.Context, dto InitDto) error {
	requestUrl, err := url.JoinPath(api.pocUrl, InitGeneratePath)
	if err != nil {
		return err
	}

	_, err = utils.SendPostJsonRequest(context, &api.client, requestUrl, dto)
	if err != nil {
		return err
	}

	return nil
}

func (api *Client) InitValidate(context context.Context, dto InitDto) error {
	requestUrl, err := url.JoinPath(api.pocUrl, InitValidatePath)
	if err != nil {
		return err
	}

	_, err = utils.SendPostJsonRequest(context, &api.client, requestUrl, dto)
	if err != nil {
		return err
	}

	return nil
}

func (api *Client) ValidateBatch(context context.Context, batch ProofBatch) error {
	requestUrl, err := url.JoinPath(api.pocUrl, ValidateBatchPath)
	if err != nil {
		return err
	}

	_, err = utils.SendPostJsonRequest(context, &api.client, requestUrl, batch)
	if err != nil {
		return err
	}

	return nil
}
