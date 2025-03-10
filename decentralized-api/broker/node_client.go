package broker

import (
	"decentralized-api/apiconfig"
	"decentralized-api/utils"
	"errors"
	"net/http"
	"net/url"
	"time"
)

const (
	trainStartPath  = "/api/v1/train/start"
	trainStatusPath = "/api/v1/train/status"
	stopPath        = "/api/v1/stop"
)

type InferenceNodeClient struct {
	node   *apiconfig.InferenceNode
	client http.Client
}

func NewNodeApi(node *apiconfig.InferenceNode) (*InferenceNodeClient, error) {
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
	GlobalRank      int    `json:"GLOBAL_RANK"`
	GlobalUniqueID  int    `json:"GLOBAL_UNIQUE_ID"`
	GlobalWorldSize int    `json:"GLOBAL_WORLD_SIZE"`
	BasePort        int    `json:"BASE_PORT"`
}

func (api *InferenceNodeClient) StartTraining() error {
	requestUrl, err := url.JoinPath(api.node.PoCUrl(), trainStartPath)
	if err != nil {
		return err
	}

	_, err = utils.SendPostJsonRequest(&api.client, requestUrl, nil)
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
