package validation

import (
	"bytes"
	"context"
	"decentralized-api/logging"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/productscience/inference/x/inference/types"
)

type ValidatorClient struct {
	baseURL      string
	httpClient   *http.Client
	currentModel string
}

func NewValidatorClient(baseURL string) *ValidatorClient {
	return &ValidatorClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Minute,
		},
	}
}

type ValidatorInferenceResponse struct {
	Status string                 `json:"status"`
	Result map[string]interface{} `json:"result,omitempty"`
	Error  string                 `json:"error,omitempty"`
}

type DownloadModelRequest struct {
	RepoID   string  `json:"repo_id"`
	Revision *string `json:"revision,omitempty"`
}

type DownloadModelResponse struct {
	Status string `json:"status"`
	RepoID string `json:"repo_id"`
	Path   string `json:"path"`
}

type SetModelRequest struct {
	Model          string   `json:"model"`
	Dtype          string   `json:"dtype"`
	AdditionalArgs []string `json:"additional_args"`
}

type SetModelResponse struct {
	Status string `json:"status"`
	Model  string `json:"model"`
}

type StartVLLMResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

type StopVLLMResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

type HealthResponse struct {
	Status        string `json:"status"`
	VllmStatus    string `json:"vllm_status"`
	VllmAvailable bool   `json:"vllm_available"`
	QueueSize     int    `json:"queue_size"`
	IsProcessing  bool   `json:"is_processing"`
}

func (vc *ValidatorClient) DownloadModel(ctx context.Context, repoID string, revision *string) error {
	req := DownloadModelRequest{
		RepoID:   repoID,
		Revision: revision,
	}

	reqBody, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to marshal download request: %w", err)
	}

	downloadURL := fmt.Sprintf("%s/api/v1/models/download", vc.baseURL)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", downloadURL, bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("failed to create download request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := vc.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("failed to download model: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("download returned status %d: %s", resp.StatusCode, string(body))
	}

	var downloadResp DownloadModelResponse
	if err := json.NewDecoder(resp.Body).Decode(&downloadResp); err != nil {
		return fmt.Errorf("failed to decode download response: %w", err)
	}

	logging.Info("Model download initiated", types.Validation, "repo_id", repoID, "path", downloadResp.Path)
	return nil
}

func (vc *ValidatorClient) SetModel(ctx context.Context, model string, dtype string, additionalArgs []string) error {
	req := SetModelRequest{
		Model:          model,
		Dtype:          dtype,
		AdditionalArgs: additionalArgs,
	}

	reqBody, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to marshal set model request: %w", err)
	}

	setModelURL := fmt.Sprintf("%s/api/v1/models/set", vc.baseURL)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", setModelURL, bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("failed to create set model request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := vc.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("failed to set model: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("set model returned status %d: %s", resp.StatusCode, string(body))
	}

	var setModelResp SetModelResponse
	if err := json.NewDecoder(resp.Body).Decode(&setModelResp); err != nil {
		return fmt.Errorf("failed to decode set model response: %w", err)
	}

	vc.currentModel = model
	logging.Info("Model set successfully", types.Validation, "model", model)
	return nil
}

func (vc *ValidatorClient) StartVLLM(ctx context.Context) error {
	startURL := fmt.Sprintf("%s/api/v1/vllm/start", vc.baseURL)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", startURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create start request: %w", err)
	}

	resp, err := vc.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("failed to start vLLM: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("start vLLM returned status %d: %s", resp.StatusCode, string(body))
	}

	var startResp StartVLLMResponse
	if err := json.NewDecoder(resp.Body).Decode(&startResp); err != nil {
		return fmt.Errorf("failed to decode start response: %w", err)
	}

	logging.Info("vLLM started successfully", types.Validation, "message", startResp.Message)
	return nil
}

func (vc *ValidatorClient) StopVLLM(ctx context.Context) error {
	stopURL := fmt.Sprintf("%s/api/v1/vllm/stop", vc.baseURL)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", stopURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create stop request: %w", err)
	}

	resp, err := vc.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("failed to stop vLLM: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("stop vLLM returned status %d: %s", resp.StatusCode, string(body))
	}

	var stopResp StopVLLMResponse
	if err := json.NewDecoder(resp.Body).Decode(&stopResp); err != nil {
		return fmt.Errorf("failed to decode stop response: %w", err)
	}

	logging.Info("vLLM stopped successfully", types.Validation, "message", stopResp.Message)
	return nil
}

func (vc *ValidatorClient) GetHealth(ctx context.Context) (*HealthResponse, error) {
	healthURL := fmt.Sprintf("%s/api/v1/health", vc.baseURL)
	httpReq, err := http.NewRequestWithContext(ctx, "GET", healthURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create health request: %w", err)
	}

	resp, err := vc.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to get health: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("health check returned status %d: %s", resp.StatusCode, string(body))
	}

	var healthResp HealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&healthResp); err != nil {
		return nil, fmt.Errorf("failed to decode health response: %w", err)
	}

	return &healthResp, nil
}

func (vc *ValidatorClient) EnsureVLLMReady(ctx context.Context, modelConfig SetModelRequest) error {
	health, err := vc.GetHealth(ctx)
	if err != nil {
		return fmt.Errorf("failed to get health status: %w", err)
	}

	if health.VllmAvailable && vc.currentModel == modelConfig.Model {
		logging.Debug("vLLM already running with correct model", types.Validation, "model", modelConfig.Model)
		return nil
	}

	if health.VllmAvailable && vc.currentModel != modelConfig.Model {
		logging.Info("vLLM running with different model, stopping", types.Validation, "current", vc.currentModel, "required", modelConfig.Model)
		if err := vc.StopVLLM(ctx); err != nil {
			return fmt.Errorf("failed to stop vLLM: %w", err)
		}
		time.Sleep(5 * time.Second)
	}

	logging.Info("Setting up vLLM for model", types.Validation, "model", modelConfig.Model, "dtype", modelConfig.Dtype, "additional_args", modelConfig.AdditionalArgs)

	if err := vc.SetModel(ctx, modelConfig.Model, modelConfig.Dtype, modelConfig.AdditionalArgs); err != nil {
		return fmt.Errorf("failed to set model: %w", err)
	}

	if err := vc.StartVLLM(ctx); err != nil {
		return fmt.Errorf("failed to start vLLM: %w", err)
	}

	maxRetries := 30
	for i := 0; i < maxRetries; i++ {
		time.Sleep(2 * time.Second)
		health, err := vc.GetHealth(ctx)
		if err == nil && health.VllmAvailable {
			logging.Info("vLLM is ready", types.Validation, "model", modelConfig.Model)
			return nil
		}
		logging.Debug("Waiting for vLLM to be ready", types.Validation, "attempt", i+1, "max", maxRetries)
	}

	return fmt.Errorf("vLLM failed to become ready after %d attempts", maxRetries)
}

func (vc *ValidatorClient) SubmitInference(ctx context.Context, requestMap map[string]interface{}) (*ValidatorInferenceResponse, error) {
	reqBody, err := json.Marshal(requestMap)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	logging.Debug("Submitting inference to validator", types.Validation, "request", requestMap, "body", string(reqBody))

	submitURL := fmt.Sprintf("%s/api/v1/chat/completions", vc.baseURL)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", submitURL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := vc.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to submit inference: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("validator returned status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	logging.Debug("Validator response", types.Validation, "body", string(body))

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &ValidatorInferenceResponse{
		Status: "completed",
		Result: result,
	}, nil
}

func (vc *ValidatorClient) PerformInferenceWithSetup(ctx context.Context, modelConfig SetModelRequest, requestMap map[string]interface{}) (*ValidatorInferenceResponse, error) {
	if err := vc.EnsureVLLMReady(ctx, modelConfig); err != nil {
		return nil, fmt.Errorf("failed to ensure vLLM is ready: %w", err)
	}

	statusResp, err := vc.SubmitInference(ctx, requestMap)
	if err != nil {
		return nil, fmt.Errorf("failed to submit inference: %w", err)
	}

	logging.Debug("Completed inference with validator", types.Validation)

	return statusResp, nil
}
