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
