package mlnodeclient

import (
	"common/logging"
	"common/utils"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
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

	resp, err := utils.SendPostJsonRequest(ctx, &api.client, requestUrl, nil)
	if err != nil {
		return err
	}
	defer drainAndClose(resp)

	// The MLnode only answers 200 for /stop; anything else means the node did
	// not stop. Reporting success on a 4xx/5xx let callers (broker redeploy,
	// node-status reconciliation) proceed against a node still running a model.
	if resp.StatusCode != http.StatusOK {
		return &StatusError{Op: "stop", StatusCode: resp.StatusCode, Body: readErrorBody(resp)}
	}

	return nil
}

// maxErrorBodyBytes caps how much of an error response body is read into an
// error message, so a misbehaving or non-MLnode endpoint cannot make us buffer
// an unbounded response.
const maxErrorBodyBytes = 4 << 10

// readErrorBody returns a bounded, single-line excerpt of resp.Body for use in
// error messages. Never returns an error: a body we cannot read simply yields
// an empty excerpt.
func readErrorBody(resp *http.Response) string {
	if resp == nil || resp.Body == nil {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(body))
}

// drainAndClose closes resp.Body after discarding any remainder, so the
// underlying connection can be reused instead of being torn down.
func drainAndClose(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBodyBytes))
	_ = resp.Body.Close()
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

	resp, err := utils.SendPostJsonRequest(ctx, &api.client, inferenceUpUrl, dto)
	if err != nil {
		logging.Error("Failed to send inference/up request", types.PoC, "error", err, "inferenceUpUrl", inferenceUpUrl, "inferenceUpDto", dto)
		return err
	}
	defer drainAndClose(resp)

	// The MLnode answers 409 when vLLM is already running or still starting, and
	// 500 when startup failed. Treating those as success — the old behavior, where
	// only transport errors were reported — meant callers believed a model was
	// loaded when it was not, so the broker marked a failed launch healthy.
	if resp.StatusCode != http.StatusOK {
		statusErr := &StatusError{Op: "inference/up", StatusCode: resp.StatusCode, Body: readErrorBody(resp)}
		logging.Error("inference/up returned a non-OK status", types.PoC, "error", statusErr, "inferenceUpUrl", inferenceUpUrl, "inferenceUpDto", dto)
		return statusErr
	}
	return nil
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
