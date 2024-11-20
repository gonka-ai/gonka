package poc

import (
	"bytes"
	"decentralized-api/broker"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"
)

const (
	InitGeneratePath = "/api/v1/pow/init/generate"
	InitValidatePath = "/api/v1/pow/init/validate"
	StopPath         = "/api/v1/pow/stop"

	DefaultRTarget        = 1.390051443
	DefaultBatchSize      = 8000
	DefaultFraudThreshold = 0.01
)

type NodePoCOrchestrator struct {
	pubKey       string
	HTTPClient   *http.Client
	nodeBroker   *broker.Broker
	callbackHost string
}

func NewNodePoCOrchestrator(pubKey string, nodeBroker *broker.Broker, callbackHost string) *NodePoCOrchestrator {
	return &NodePoCOrchestrator{
		pubKey: pubKey,
		HTTPClient: &http.Client{
			Timeout: 60 * time.Second,
		},
		nodeBroker:   nodeBroker,
		callbackHost: callbackHost,
	}
}

func (o *NodePoCOrchestrator) getPocBatchesCallbackUrl() string {
	return fmt.Sprintf("https://%s/v1/poc-batches", o.callbackHost)
}

func (o *NodePoCOrchestrator) getPocValidateCallbackUrl() string {
	// PRTODO: This is a placeholder. Replace with actual URL.
	return fmt.Sprintf("https://%s/v1/poc-validate-results", o.callbackHost)
}

type InitDto struct {
	ChainHash      string  `json:"chain_hash"`
	ChainHeight    int64   `json:"chain_height"`
	PublicKey      string  `json:"public_key"`
	BatchSize      int     `json:"batch_size"`
	RTarget        float64 `json:"r_target"`
	FraudThreshold float64 `json:"fraud_threshold"`
	Params         *Params `json:"params"`
	URL            string  `json:"url"`
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

func (o *NodePoCOrchestrator) Start(blockHeight int64, blockHash string) {
	slog.Info("Starting PoC on nodes")
	nodes, err := o.nodeBroker.GetNodes()
	if err != nil {
		// PRTODO: log error
		return
	}

	for _, n := range nodes {
		resp, err := o.sendInitGenerateRequest(n.Node, blockHeight, blockHash)
		if err != nil {
			slog.Error("Failed to send init-generate request to node", "node", n.Node.Url, "error", err)
			continue
		}

		// PRTODO: analyze response somehow?
		_ = resp
	}
}

func (o *NodePoCOrchestrator) sendInitGenerateRequest(node *broker.InferenceNode, blockHeight int64, blockHash string) (*http.Response, error) {
	initDto := InitDto{
		ChainHeight:    blockHeight,
		ChainHash:      blockHash,
		PublicKey:      o.pubKey,
		BatchSize:      DefaultBatchSize,
		RTarget:        DefaultRTarget,
		FraudThreshold: DefaultFraudThreshold,
		Params:         &DefaultParams,
		URL:            o.getPocBatchesCallbackUrl(),
	}

	initUrl, err := url.JoinPath(node.Url, InitGeneratePath)
	if err != nil {
		return nil, err
	}

	slog.Info("Sending init-generate request to node.", "url", initUrl, "initDto", initDto)

	return sendPostRequest(o.HTTPClient, initUrl, initDto)
}

func (o *NodePoCOrchestrator) Stop() {
	nodes, err := o.nodeBroker.GetNodes()
	if err != nil {
		// PRTODO: log error
		return
	}

	for _, n := range nodes {
		resp, err := o.sendStopRequest(n.Node)
		if err != nil {
			slog.Error("Failed to send stop request to node", "node", n.Node.Url, "error", err)
			continue
		}
		_ = resp
	}
}

func (o *NodePoCOrchestrator) sendStopRequest(node *broker.InferenceNode) (*http.Response, error) {
	stopUrl, err := url.JoinPath(node.Url, StopPath)
	if err != nil {
		return nil, err
	}

	slog.Info("Sending stop request to node", "stopUrl", stopUrl)

	return sendPostRequest(o.HTTPClient, stopUrl, nil)
}

func (o *NodePoCOrchestrator) sendInitValidateRequest(node *broker.InferenceNode, blockHash string) (*http.Response, error) {
	initDto := InitDto{
		ChainHash: blockHash,
		PublicKey: o.pubKey,
		BatchSize: DefaultBatchSize,
		RTarget:   DefaultRTarget,
		URL:       o.getPocValidateCallbackUrl(),
		Params:    &DefaultParams,
	}

	initUrl, err := url.JoinPath(node.Url, InitValidatePath)
	if err != nil {
		return nil, err
	}

	return sendPostRequest(o.HTTPClient, initUrl, initDto)
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

func (o *NodePoCOrchestrator) MoveToValidationStage(currentBlockHeight int64) {
	// PRTODO: figure out original start blockHeight
	startBlockHeight := int64(0)
	// PRTODO: figure out original blockHash
	blockHash := "asa"

	slog.Info("Starting PoC Validation on nodes")
	nodes, err := o.nodeBroker.GetNodes()
	if err != nil {
		// PRTODO: log error
		return
	}

	for _, n := range nodes {
		resp, err := o.sendInitValidateRequest(n.Node, startBlockHeight, blockHash)
		if err != nil {
			slog.Error("Failed to send init-generate request to node", "node", n.Node.Url, "error", err)
			continue
		}

		// PRTODO: analyze response somehow?
		_ = resp
	}
}

func (o *NodePoCOrchestrator) ValidateReceivedBatches(currentBlockHeight int64) {

}
