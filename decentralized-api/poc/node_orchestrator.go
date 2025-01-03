package poc

import (
	"bytes"
	"context"
	"decentralized-api/broker"
	cosmos_client "decentralized-api/cosmosclient"
	"encoding/json"
	"fmt"
	"github.com/productscience/inference/x/inference/proofofcompute"
	"github.com/productscience/inference/x/inference/types"
	"log/slog"
	"net/http"
	"net/url"
	"time"
)

const (
	InitGeneratePath  = "/api/v1/pow/init/generate"
	InitValidatePath  = "/api/v1/pow/init/validate"
	ValidateBatchPath = "/api/v1/pow/validate"
	StopPath          = "/api/v1/pow/stop"
	InferenceUpPath   = "/api/v1/inference/up"

	DefaultRTarget        = 1.390051443
	DefaultBatchSize      = 8000
	DefaultFraudThreshold = 0.01
)

type NodePoCOrchestrator struct {
	pubKey       string
	HTTPClient   *http.Client
	nodeBroker   *broker.Broker
	callbackHost string
	chainNodeUrl string
	cosmosClient *cosmos_client.InferenceCosmosClient
	noOp         bool
}

func NewNodePoCOrchestrator(pubKey string, nodeBroker *broker.Broker, callbackHost string, chainNodeUrl string, cosmosClient *cosmos_client.InferenceCosmosClient) *NodePoCOrchestrator {
	return &NodePoCOrchestrator{
		pubKey: pubKey,
		HTTPClient: &http.Client{
			Timeout: 60 * time.Second,
		},
		nodeBroker:   nodeBroker,
		callbackHost: callbackHost,
		chainNodeUrl: chainNodeUrl,
		cosmosClient: cosmosClient,
		noOp:         false,
	}
}

func (o *NodePoCOrchestrator) getPocBatchesCallbackUrl() string {
	return fmt.Sprintf("https://%s/v1/poc-batches", o.callbackHost)
}

func (o *NodePoCOrchestrator) getPocValidateCallbackUrl() string {
	// For now the URl is the same, the node inference server appends "/validated" to the URL
	//  or "/generated" (in case of init-generate)
	return fmt.Sprintf("https://%s/v1/poc-batches", o.callbackHost)
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

func (o *NodePoCOrchestrator) Start(blockHeight int64, blockHash string) {
	if o.noOp {
		slog.Info("NodePoCOrchestrator.Start. NoOp is set. Skipping start.")
		return
	}

	slog.Info("Starting PoC on nodes", "blockHeight", blockHeight, "blockHash", blockHash)
	nodes, err := o.nodeBroker.GetNodes()
	if err != nil {
		slog.Error("NodePoCOrchestrator.Start. Failed to get nodes", "error", err)
		return
	}

	for _, n := range nodes {
		resp, err := o.sendInitGenerateRequest(n.Node, blockHeight, blockHash)
		if err != nil {
			slog.Error("Failed to send init-generate request to node", "node", n.Node.Host, "error", err)
			continue
		}

		// PRTODO: analyze response somehow?
		_ = resp
	}
}

func (o *NodePoCOrchestrator) sendInitGenerateRequest(node *broker.InferenceNode, blockHeight int64, blockHash string) (*http.Response, error) {
	initDto := o.buildInitDto(blockHeight, blockHash, o.getPocBatchesCallbackUrl())

	initUrl, err := url.JoinPath(node.PoCUrl(), InitGeneratePath)
	if err != nil {
		return nil, err
	}

	slog.Info("Sending init-generate request to node.", "url", initUrl, "initDto", initDto)

	return sendPostRequest(o.HTTPClient, initUrl, initDto)
}

func (o *NodePoCOrchestrator) buildInitDto(blockHeight int64, blockHash string, callbackUrl string) InitDto {
	return InitDto{
		ChainHeight:    blockHeight,
		ChainHash:      blockHash,
		PublicKey:      o.pubKey,
		BatchSize:      DefaultBatchSize,
		RTarget:        DefaultRTarget,
		FraudThreshold: DefaultFraudThreshold,
		Params:         &DevTestParams,
		URL:            callbackUrl,
	}
}

func (o *NodePoCOrchestrator) Stop() {
	if o.noOp {
		slog.Info("NodePoCOrchestrator.Stop. NoOp is set. Skipping stop.")
		return
	}

	nodes, err := o.nodeBroker.GetNodes()
	if err != nil {
		// PRTODO: log error
		return
	}

	for _, n := range nodes {
		respStop, err := o.sendStopRequest(n.Node)
		if err != nil {
			slog.Error("Failed to send stop request to node", "node", n.Node.Host, "error", err)
			continue
		}
		_ = respStop

		respUp, err := o.sendInferenceUpRequest(n.Node)
		if err != nil {
			slog.Error("Failed to send inference/up request to node", "node", n.Node.Host, "error", err)
			continue
		}
		_ = respUp
	}
}

func (o *NodePoCOrchestrator) sendStopRequest(node *broker.InferenceNode) (*http.Response, error) {
	stopUrl, err := url.JoinPath(node.PoCUrl(), StopPath)
	if err != nil {
		return nil, err
	}

	slog.Info("Sending stop request to node", "stopUrl", stopUrl)

	return sendPostRequest(o.HTTPClient, stopUrl, nil)
}

type InferenceUpDto struct {
	Model string   `json:"model"`
	Dtype string   `json:"dtype"`
	Args  []string `json:"additional_args"`
}

func (o *NodePoCOrchestrator) sendInferenceUpRequest(node *broker.InferenceNode) (*http.Response, error) {
	inferenceUpUrl, err := url.JoinPath(node.PoCUrl(), InferenceUpPath)
	if err != nil {
		return nil, err
	}

	model := node.Models[0]
	inferenceUpDto := InferenceUpDto{
		Model: model,
		Dtype: "float16",
		Args:  []string{"--enforce-eager"},
	}

	slog.Info("Sending inference/up request to node", "inferenceUpUrl", inferenceUpUrl, "inferenceUpDto", inferenceUpDto)

	return sendPostRequest(o.HTTPClient, inferenceUpUrl, inferenceUpDto)
}

func (o *NodePoCOrchestrator) sendInitValidateRequest(node *broker.InferenceNode, blockHeight int64, blockHash string) (*http.Response, error) {
	initDto := o.buildInitDto(blockHeight, blockHash, o.getPocValidateCallbackUrl())

	initUrl, err := url.JoinPath(node.PoCUrl(), InitValidatePath)
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

func (o *NodePoCOrchestrator) MoveToValidationStage(encOfPoCBlockHeight int64) {
	if o.noOp {
		slog.Info("NodePoCOrchestrator.MoveToValidationStage. NoOp is set. Skipping move to validation stage.")
		return
	}

	startOfPoCBlockHeight := proofofcompute.GetStartBlockHeightFromEndOfPocStage(encOfPoCBlockHeight)
	blockHash, err := o.getBlockHash(startOfPoCBlockHeight)
	if err != nil {
		slog.Error("MoveToValidationStage. Failed to get block hash", "error", err)
		return
	}

	slog.Info("Moving to PoC Validation Stage", "startOfPoCBlockHeight", startOfPoCBlockHeight, "blockHash", blockHash)

	slog.Info("Starting PoC Validation on nodes")
	nodes, err := o.nodeBroker.GetNodes()
	if err != nil {
		// PRTODO: log error
		return
	}

	for _, n := range nodes {
		resp, err := o.sendInitValidateRequest(n.Node, startOfPoCBlockHeight, blockHash)
		if err != nil {
			slog.Error("Failed to send init-generate request to node", "node", n.Node.Host, "error", err)
			continue
		}

		// PRTODO: analyze response somehow?
		_ = resp
	}
}

func (o *NodePoCOrchestrator) ValidateReceivedBatches(startOfValStageHeight int64) {
	if o.noOp {
		slog.Info("NodePoCOrchestrator.ValidateReceivedBatches. NoOp is set. Skipping validation.")
		return
	}

	startOfPoCBlockHeight := proofofcompute.GetStartBlockHeightFromStartOfValStage(startOfValStageHeight)
	blockHash, err := o.getBlockHash(startOfPoCBlockHeight)
	if err != nil {
		slog.Error("ValidateReceivedBatches. Failed to get block hash", "error", err)
		return
	}

	// 1. GET ALL SUBMITTED BATCHES!
	// batches, err := o.cosmosClient.GetPoCBatchesByStage(startOfPoCBlockHeight)
	// FIXME: might be too long of a transaction, paging might be needed
	queryClient := o.cosmosClient.NewInferenceQueryClient()
	batches, err := queryClient.PocBatchesForStage(o.cosmosClient.Context, &types.QueryPocBatchesForStageRequest{BlockHeight: startOfPoCBlockHeight})
	if err != nil {
		slog.Error("Failed to get PoC batches", "error", err)
		return
	}

	nodes, err := o.nodeBroker.GetNodes()
	if err != nil {
		slog.Error("Failed to get nodes", "error", err)
		return
	}

	if len(nodes) == 0 {
		slog.Error("No nodes available to validate PoC batches")
		return
	}

	for i, batch := range batches.PocBatch {
		_ = batch

		joinedBatch := ProofBatch{
			PublicKey:   batch.HexPubKey,
			ChainHash:   blockHash,
			ChainHeight: startOfPoCBlockHeight,
			Nonces:      nil,
			Dist:        nil,
		}

		for _, b := range batch.PocBatch {
			joinedBatch.Dist = append(joinedBatch.Dist, b.Dist...)
			joinedBatch.Nonces = append(joinedBatch.Nonces, b.Nonces...)
		}

		node := nodes[i%len(nodes)]

		slog.Debug("ValidateReceivedBatches. pubKey", "pubKey", batch.HexPubKey)
		slog.Debug("ValidateReceivedBatches. sending batch", "node", node.Node.Host, "batch", joinedBatch)
		resp, err := o.sendValidateBatchRequest(node.Node, joinedBatch)
		if err != nil {
			slog.Error("Failed to send validate batch request to node", "node", node.Node.Host, "error", err)
			continue
		}

		_ = resp
	}
}

// FIXME: copying ;( doesn't look good for large PoCBatch structures
func (o *NodePoCOrchestrator) sendValidateBatchRequest(node *broker.InferenceNode, batch ProofBatch) (*http.Response, error) {
	validateBatchUrl, err := url.JoinPath(node.PoCUrl(), ValidateBatchPath)
	if err != nil {
		return nil, err
	}

	return sendPostRequest(o.HTTPClient, validateBatchUrl, batch)
}

func (o *NodePoCOrchestrator) getBlockHash(height int64) (string, error) {
	client, err := cosmos_client.NewRpcClient(o.chainNodeUrl)
	if err != nil {
		return "", err
	}

	block, err := client.Block(context.Background(), &height)
	if err != nil {
		return "", err
	}

	return block.Block.Hash().String(), err
}
