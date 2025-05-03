package mlnodeclient

import (
	"decentralized-api/utils"
	"net/url"
)

const (
	InitGeneratePath = "/api/v1/pow/init/generate"

	DefaultRTarget        = 1.3971164020989417
	DefaultBatchSize      = 100
	DefaultFraudThreshold = 0.01
)

type InitDto struct {
	BlockHash      string  `json:"block_hash"`
	BlockHeight    int64   `json:"block_height"`
	PublicKey      string  `json:"public_key"`
	BatchSize      int     `json:"batch_size"`
	RTarget        float64 `json:"r_target"`
	FraudThreshold float64 `json:"fraud_threshold"`
	Params         *Params `json:"params"`
	NodeNum        int64   `json:"node_id"`
	TotalNodes     int64   `json:"node_count"`
	URL            string  `json:"url"`
}

func BuildInitDto(blockHeight int64, pubKey string, totalNodes, nodeNum int64, blockHash, callbackUrl string) InitDto {
	return InitDto{
		BlockHeight:    blockHeight,
		BlockHash:      blockHash,
		PublicKey:      pubKey,
		BatchSize:      DefaultBatchSize,
		RTarget:        DefaultRTarget,
		FraudThreshold: DefaultFraudThreshold,
		Params:         &TestNetParams,
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

func (api *Client) InitGenerate(dto InitDto) error {
	requestUrl, err := url.JoinPath(api.pocUrl, InitGeneratePath)
	if err != nil {
		return err
	}

	_, err = utils.SendPostJsonRequest(&api.client, requestUrl, dto)
	if err != nil {
		return err
	}

	return nil
}
