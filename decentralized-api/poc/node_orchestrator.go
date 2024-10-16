package poc

import (
	"bytes"
	"encoding/json"
	"net/http"
	"time"
)

type NodePoCOrchestrator struct {
	pubKey     string
	HTTPClient *http.Client
}

func NewNodePoCOrchestrator(pubKey string) *NodePoCOrchestrator {
	return &NodePoCOrchestrator{
		pubKey: pubKey,
		HTTPClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// Payload represents the main JSON structure
type InitDto struct {
	ChainHash string  `json:"chain_hash"`
	PublicKey string  `json:"public_key"`
	BatchSize int     `json:"batch_size"`
	RTarget   int     `json:"r_target"`
	Params    *Params `json:"params"`
	URL       string  `json:"url"`
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

func (o *NodePoCOrchestrator) start(blockHeight int64, blockHash string) {
	initDto := InitDto{
		ChainHash: blockHash,
		PublicKey: o.pubKey,
		BatchSize: 1,                           // PRTODO: what value are we providing here?
		RTarget:   1,                           // PROTOD: what value are we providing here?
		URL:       "http://hello/v1/generated", // PRTODO:
		Params:    nil,                         // PRTODO: are they necessary
	}

	// PRTODO: use node url
	resp, err := sendPostRequest(o.HTTPClient, "http://localhost:8080/api/v1/init-generate", initDto)
	if err != nil {
		// PRTODO: log error
		return
	}

	_ = resp
}

func (o *NodePoCOrchestrator) stop() {
	resp, err := sendPostRequest(o.HTTPClient, "http://localhost:8080/api/v1/stop", nil)

	if err != nil {
		// PRTODO: log error
		return
	}

	_ = resp
}

func sendPostRequest(client *http.Client, url string, payload any) (*http.Response, error) {
	var req *http.Request
	var err error

	if payload == nil {
		// Create a POST request with no body if payload is nil.
		req, err = http.NewRequest(http.MethodPost, url, nil)
	} else {
		// Marshal the payload to JSON.
		jsonData, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		req, err = http.NewRequest(http.MethodPost, url, bytes.NewBuffer(jsonData))
	}

	if err != nil {
		return nil, err
	}

	return client.Do(req)
}
