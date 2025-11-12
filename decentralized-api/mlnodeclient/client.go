package mlnodeclient

import (
	"context"
	"decentralized-api/logging"
	"decentralized-api/utils"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/productscience/inference/x/inference/training"
	"github.com/productscience/inference/x/inference/types"
)

const (
	trainStartPath  = "/api/v1/train/start"
	trainStatusPath = "/api/v1/train/status"
	stopPath        = "/api/v1/stop"
	nodeStatePath   = "/api/v1/state"
	powStatusPath   = "/api/v1/pow/status"
	inferenceUpPath = "/api/v1/inference/up"
)

type Client struct {
	pocUrl                string
	inferenceUrl          string
	baseURL               string
	client                http.Client
	mlGrpcCallbackAddress string
	authToken             string
}

func NewNodeClient(pocUrl string, inferenceUrl string, authToken string, baseURL string) *Client {
	return &Client{
		pocUrl:       pocUrl,
		inferenceUrl: inferenceUrl,
		baseURL:      baseURL,
		client: http.Client{
			Timeout: 15 * time.Minute,
		},
		mlGrpcCallbackAddress: "api-private:9300", // TODO: PRTODO: make this configurable
		authToken:             authToken,
	}
}

type StartTraining struct {
	TrainConfig TrainConfig `json:"train_config"`
	TrainEnv    TrainEnv    `json:"train_env"`
}

type TrainConfig struct {
	Project     string       `json:"project"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Group       string       `json:"group"`
	Tags        []string     `json:"tags"`
	Train       TrainParams  `json:"train"`
	Data        DataConfig   `json:"data"`
	Optim       OptimConfig  `json:"optim"`
	Diloco      DilocoConfig `json:"diloco"`
	Ckpt        Checkpoint   `json:"ckpt"`
}

type TrainParams struct {
	MicroBatchSize int `json:"micro_bs"`
	EvalInterval   int `json:"eval_interval"`
}

type DataConfig struct {
	SeqLength int `json:"seq_length"`
}

type OptimConfig struct {
	SchedType    string  `json:"sched_type"`
	BatchSize    int     `json:"batch_size"`
	WarmupSteps  int     `json:"warmup_steps"`
	TotalSteps   int     `json:"total_steps"`
	AdamBetas1   float64 `json:"adam_betas1"`
	AdamBetas2   float64 `json:"adam_betas2"`
	WeightDecay  float64 `json:"weight_decay"`
	LearningRate float64 `json:"lr"`
}

type DilocoConfig struct {
	InnerSteps int `json:"inner_steps"`
}

type Checkpoint struct {
	Interval int    `json:"interval"`
	TopK     int    `json:"topk"`
	Path     string `json:"path"`
}

type TrainEnv struct {
	TaskId          string `json:"TASK_ID"`
	NodeId          string `json:"NODE_ID"`
	StoreApiUrl     string `json:"STORE_API_URL"`
	GlobalAddr      string `json:"GLOBAL_ADDR"`
	GlobalPort      string `json:"GLOBAL_PORT"`
	GlobalRank      string `json:"GLOBAL_RANK"`
	GlobalUniqueID  string `json:"GLOBAL_UNIQUE_ID"`
	GlobalWorldSize string `json:"GLOBAL_WORLD_SIZE"`
	BasePort        string `json:"BASE_PORT"`
}

var devTrainConfig = TrainConfig{
	Project:     "1B-ft-xlam",
	Name:        "refactor test 3090",
	Description: "3090 micro bs2",
	Group:       "base",
	Tags:        []string{"1x1", "no-diloco"},
	Train: TrainParams{
		MicroBatchSize: 2,
		EvalInterval:   50,
	},
	Data: DataConfig{
		SeqLength: 1024,
	},
	Optim: OptimConfig{
		SchedType:    "cosine",
		BatchSize:    32,
		WarmupSteps:  50,
		TotalSteps:   6000,
		AdamBetas1:   0.9,
		AdamBetas2:   0.95,
		WeightDecay:  0.1,
		LearningRate: 5e-6,
	},
	Diloco: DilocoConfig{
		InnerSteps: 50,
	},
	Ckpt: Checkpoint{
		Interval: 1000,
		TopK:     6,
		Path:     "outputs/1B_4x1-lr",
	},
}

const (
	defaultGlobalTrainingPort = "5565"
	defaultTrainingBasePort   = "10001"
)

func (api *Client) StartTraining(ctx context.Context, taskId uint64, participant string, nodeId string, masterNodeAddr string, rank int, worldSize int) error {
	requestUrl, err := url.JoinPath(api.pocUrl, trainStartPath)
	if err != nil {
		return err
	}

	globalNodeId := training.GlobalNodeId{
		Participant: participant,
		LocalNodeId: nodeId,
	}
	trainEnv := TrainEnv{
		TaskId:          strconv.FormatUint(taskId, 10),
		NodeId:          globalNodeId.ToString(),
		StoreApiUrl:     api.mlGrpcCallbackAddress,
		GlobalAddr:      masterNodeAddr,
		GlobalPort:      defaultGlobalTrainingPort,
		GlobalRank:      strconv.Itoa(rank),
		GlobalUniqueID:  strconv.Itoa(rank),
		GlobalWorldSize: strconv.Itoa(worldSize),
		BasePort:        defaultTrainingBasePort,
	}
	body := StartTraining{
		TrainConfig: devTrainConfig,
		TrainEnv:    trainEnv,
	}

	logging.Info("Starting training with", types.Training, "trainEnv", trainEnv)
	_, err = utils.SendPostJsonRequestWithAuth(ctx, &api.client, requestUrl, body, api.authToken)
	if err != nil {
		return err
	}

	return nil
}

func (api *Client) GetTrainingStatus(ctx context.Context) error {
	requestUrl, err := url.JoinPath(api.pocUrl, trainStartPath)
	if err != nil {
		return err
	}

	_, err = utils.SendGetRequestWithAuth(ctx, &api.client, requestUrl, api.authToken)
	if err != nil {
		return err
	}

	return nil
}

func (api *Client) Stop(ctx context.Context) error {
	requestUrl, err := url.JoinPath(api.pocUrl, stopPath)
	if err != nil {
		return err
	}

	_, err = utils.SendPostJsonRequestWithAuth(ctx, &api.client, requestUrl, nil, api.authToken)
	if err != nil {
		return err
	}

	return nil
}

type MLNodeState string

const (
	MlNodeState_POW       MLNodeState = "POW"
	MlNodeState_INFERENCE MLNodeState = "INFERENCE"
	MlNodeState_TRAIN     MLNodeState = "TRAIN"
	MlNodeState_STOPPED   MLNodeState = "STOPPED"
)

type StateResponse struct {
	State MLNodeState `json:"state"`
}

func (api *Client) NodeState(ctx context.Context) (*StateResponse, error) {
	requestURL, err := url.JoinPath(api.pocUrl, nodeStatePath)
	if err != nil {
		return nil, err
	}

	resp, err := utils.SendGetRequestWithAuth(ctx, &api.client, requestURL, api.authToken)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var stateResp StateResponse
	if err := json.NewDecoder(resp.Body).Decode(&stateResp); err != nil {
		return nil, err
	}

	return &stateResp, nil
}

type PowState string

const (
	POW_IDLE          PowState = "IDLE"
	POW_NO_CONTROLLER PowState = "NOT_LOADED"
	POW_LOADING       PowState = "LOADING"
	POW_GENERATING    PowState = "GENERATING"
	POW_VALIDATING    PowState = "VALIDATING"
	POW_STOPPED       PowState = "STOPPED"
	POW_MIXED         PowState = "MIXED"
)

type PowStatusResponse struct {
	Status             PowState `json:"status"`
	IsModelInitialized bool     `json:"is_model_initialized"`
}

func (api *Client) GetPowStatus(ctx context.Context) (*PowStatusResponse, error) {
	requestURL, err := url.JoinPath(api.pocUrl, powStatusPath)
	if err != nil {
		return nil, err
	}

	resp, err := utils.SendGetRequestWithAuth(ctx, &api.client, requestURL, api.authToken)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var powResp PowStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&powResp); err != nil {
		return nil, err
	}

	return &powResp, nil
}

func (api *Client) InferenceHealth(ctx context.Context) (bool, error) {
	var requestURL string
	var err error

	// Determine health endpoint based on registration method
	if api.baseURL != "" {
		// New registration (baseURL): Check <baseURL>/readyz
		requestURL, err = url.JoinPath(api.baseURL, "/readyz")
	} else {
		// Legacy registration (Host/Port/Segment): Check http://<host>:<inference_port>/health
		requestURL, err = url.JoinPath(api.inferenceUrl, "/health")
	}

	if err != nil {
		return false, err
	}

	resp, err := utils.SendGetRequestWithAuth(ctx, &api.client, requestURL, api.authToken)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	return true, nil
}

type inferenceUpDto struct {
	Model string   `json:"model"`
	Dtype string   `json:"dtype"`
	Args  []string `json:"additional_args"`
}

func (api *Client) InferenceUp(ctx context.Context, model string, args []string) error {
	inferenceUpUrl, err := url.JoinPath(api.pocUrl, inferenceUpPath)
	if err != nil {
		return err
	}

	dto := inferenceUpDto{
		Model: model,
		Dtype: "float16",
		Args:  args,
	}

	logging.Info("Sending inference/up request to node", types.PoC, "inferenceUpUrl", inferenceUpUrl, "body", dto)

	_, err = utils.SendPostJsonRequestWithAuth(ctx, &api.client, inferenceUpUrl, dto, api.authToken)
	if err != nil {
		logging.Error("Failed to send inference/up request", types.PoC, "error", err, "inferenceUpUrl", inferenceUpUrl, "inferenceUpDto", dto)
	}
	return err
}

// vLLMModelsResponse represents the OpenAI-compatible /v1/models response from vLLM
type vLLMModelsResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

// GetLoadedModels queries the vLLM /v1/models endpoint to get the currently loaded model(s).
// Returns a list of model IDs that are currently loaded.
func (api *Client) GetLoadedModels(ctx context.Context) ([]string, error) {
	requestURL, err := url.JoinPath(api.inferenceUrl, "/v1/models")
	if err != nil {
		return nil, err
	}

	resp, err := utils.SendGetRequest(ctx, &api.client, requestURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var modelsResp vLLMModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&modelsResp); err != nil {
		return nil, err
	}

	var modelIds []string
	for _, model := range modelsResp.Data {
		modelIds = append(modelIds, model.ID)
	}
	return modelIds, nil
}
