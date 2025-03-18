package broker

import (
	"decentralized-api/logging"
	"decentralized-api/utils"
	"errors"
	"github.com/productscience/inference/x/inference/types"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const (
	trainStartPath  = "/api/v1/train/start"
	trainStatusPath = "/api/v1/train/status"
	stopPath        = "/api/v1/stop"
)

type InferenceNodeClient struct {
	node   *Node
	client http.Client
}

func NewNodeClient(node *Node) (*InferenceNodeClient, error) {
	if node == nil {
		return nil, errors.New("node is nil")
	}
	return &InferenceNodeClient{
		node: node,
		client: http.Client{
			Timeout: 5 * time.Minute,
		},
	}, nil
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

func (api *InferenceNodeClient) StartTraining(masterNodeAddr string, rank int, worldSize int) error {
	requestUrl, err := url.JoinPath(api.node.PoCUrl(), trainStartPath)
	if err != nil {
		return err
	}

	trainEnv := TrainEnv{
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
	_, err = utils.SendPostJsonRequest(&api.client, requestUrl, body)
	if err != nil {
		return err
	}

	return nil
}

func (api *InferenceNodeClient) GetTrainingStatus() error {
	requestUrl, err := url.JoinPath(api.node.PoCUrl(), trainStartPath)
	if err != nil {
		return err
	}

	_, err = utils.SendGetRequest(&api.client, requestUrl)
	if err != nil {
		return err
	}

	return nil
}

func (api *InferenceNodeClient) Stop() error {
	requestUrl, err := url.JoinPath(api.node.PoCUrl(), stopPath)
	if err != nil {
		return err
	}

	_, err = utils.SendPostJsonRequest(&api.client, requestUrl, nil)
	if err != nil {
		return err
	}

	return nil
}
