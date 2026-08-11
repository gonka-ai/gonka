package mlnodeclient

import (
	"common/logging"
	"common/utils"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/productscience/inference/x/inference/types"
)

const (
	stopPath        = "/api/v1/stop"
	nodeStatePath   = "/api/v1/state"
	powStatusPath   = "/api/v1/pow/status"
	inferenceUpPath = "/api/v1/inference/up"
)

type Client struct {
	pocUrl                string
	inferenceUrl          string
	client                http.Client
	mlGrpcCallbackAddress string
}

func NewNodeClient(pocUrl string, inferenceUrl string) *Client {
	return &Client{
		pocUrl:       pocUrl,
		inferenceUrl: inferenceUrl,
		client: http.Client{
			Timeout: 15 * time.Minute,
		},
		mlGrpcCallbackAddress: "api-private:9300", // TODO: PRTODO: make this configurable
	}
}

func (api *Client) Stop(ctx context.Context) error {
	requestUrl, err := url.JoinPath(api.pocUrl, stopPath)
	if err != nil {
		return err
	}

	_, err = utils.SendPostJsonRequest(ctx, &api.client, requestUrl, nil)
	if err != nil {
		return err
	}

	return nil
}

type MLNodeState string

const (
	MlNodeState_POW       MLNodeState = "POW"
	MlNodeState_INFERENCE MLNodeState = "INFERENCE"
	MlNodeState_STOPPED   MLNodeState = "STOPPED"
)

type StateResponse struct {
	State                  MLNodeState `json:"state"`
	Version                string      `json:"version"`
	PoCValidationInference bool        `json:"poc_validation_inference"`
}

func (api *Client) NodeState(ctx context.Context) (*StateResponse, error) {
	requestURL, err := url.JoinPath(api.pocUrl, nodeStatePath)
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

	resp, err := utils.SendGetRequest(ctx, &api.client, requestURL)
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
	requestURL, err := url.JoinPath(api.inferenceUrl, "/health")
	if err != nil {
		return false, err
	}

	resp, err := utils.SendGetRequest(ctx, &api.client, requestURL)
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
		Dtype: "auto",
		Args:  args,
	}

	logging.Info("Sending inference/up request to node", types.PoC, "inferenceUpUrl", inferenceUpUrl, "body", dto)

	_, err = utils.SendPostJsonRequest(ctx, &api.client, inferenceUpUrl, dto)
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
