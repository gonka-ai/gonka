package poc

import (
	"bytes"
	"decentralized-api/broker"
	"encoding/json"
	"net/http"
	"time"
)

type NodePoCOrchestrator struct {
	pubKey     string
	HTTPClient *http.Client
	nodeBroker *broker.Broker
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
	nodes, err := o.nodeBroker.GetNodes()
	if err != nil {
		// PRTODO: log error
		return
	}

	for _, n := range nodes {
		resp, err := o.sendInitGenerateRequest(n.Node, blockHash)
		if err != nil {
			// PRTODO: log error
			continue
		}
		// PRTODO: analyze response somehow?
		_ = resp
	}
}

func (o *NodePoCOrchestrator) sendInitGenerateRequest(node *broker.InferenceNode, blockHash string) (*http.Response, error) {
	initDto := InitDto{
		ChainHash: blockHash,
		PublicKey: o.pubKey,
		BatchSize: 1,                           // PRTODO: what value are we providing here?
		RTarget:   1,                           // PROTOD: what value are we providing here?
		URL:       "http://hello/v1/generated", // PRTODO:
		Params:    nil,                         // PRTODO: are they necessary
	}

	url := node.Url + "/api/v1/init-generate"

	return sendPostRequest(o.HTTPClient, url, initDto)
}

func (o *NodePoCOrchestrator) stop() {
	nodes, err := o.nodeBroker.GetNodes()
	if err != nil {
		// PRTODO: log error
		return
	}

	for _, n := range nodes {
		resp, err := o.sendStopRequest(n.Node)
		if err != nil {
			// PRTODO: log error
			continue
		}
		_ = resp
	}
}

func (o *NodePoCOrchestrator) sendStopRequest(node *broker.InferenceNode) (*http.Response, error) {
	url := node.Url + "/api/v1/stop"

	return sendPostRequest(o.HTTPClient, url, nil)
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
