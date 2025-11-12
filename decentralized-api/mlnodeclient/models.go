package mlnodeclient

import (
	"context"
	"decentralized-api/utils"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

const (
	modelStatusPath   = "/api/v1/models/status"
	modelDownloadPath = "/api/v1/models/download"
	modelDeletePath   = "/api/v1/models"
	modelListPath     = "/api/v1/models/list"
	modelSpacePath    = "/api/v1/models/space"
)

// CheckModelStatus checks if a model exists in cache with verification.
// Returns the current status of the model: DOWNLOADED, DOWNLOADING, NOT_FOUND, or PARTIAL.
// Returns ErrAPINotImplemented if the ML node doesn't support this endpoint.
func (api *Client) CheckModelStatus(ctx context.Context, model Model) (*ModelStatusResponse, error) {
	requestURL, err := url.JoinPath(api.pocUrl, modelStatusPath)
	if err != nil {
		return nil, err
	}

	resp, err := utils.SendPostJsonRequestWithAuth(ctx, &api.client, requestURL, model, api.authToken)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return nil, NewAPINotImplementedError(modelStatusPath, resp.StatusCode)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var statusResp ModelStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&statusResp); err != nil {
		return nil, err
	}

	return &statusResp, nil
}

// DownloadModel starts downloading a model asynchronously.
// The download runs in the background and can be tracked using CheckModelStatus.
// Returns 409 Conflict if model is already downloading.
// Returns 429 Too Many Requests if concurrent download limit (3) is reached.
// Returns ErrAPINotImplemented if the ML node doesn't support this endpoint.
func (api *Client) DownloadModel(ctx context.Context, model Model) (*DownloadStartResponse, error) {
	requestURL, err := url.JoinPath(api.pocUrl, modelDownloadPath)
	if err != nil {
		return nil, err
	}

	resp, err := utils.SendPostJsonRequestWithAuth(ctx, &api.client, requestURL, model, api.authToken)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return nil, NewAPINotImplementedError(modelDownloadPath, resp.StatusCode)
	}

	if resp.StatusCode == http.StatusConflict {
		return nil, fmt.Errorf("model is already downloading")
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("maximum concurrent downloads reached")
	}

	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var downloadResp DownloadStartResponse
	if err := json.NewDecoder(resp.Body).Decode(&downloadResp); err != nil {
		return nil, err
	}

	return &downloadResp, nil
}

// DeleteModel deletes a model from cache or cancels an ongoing download.
// If hf_commit is provided, only that specific revision is deleted.
// If hf_commit is nil, all versions of the model are deleted.
// Returns 404 if model is not found.
// Returns ErrAPINotImplemented if the ML node doesn't support this endpoint.
func (api *Client) DeleteModel(ctx context.Context, model Model) (*DeleteResponse, error) {
	requestURL, err := url.JoinPath(api.pocUrl, modelDeletePath)
	if err != nil {
		return nil, err
	}

	resp, err := utils.SendDeleteJsonRequestWithAuth(ctx, &api.client, requestURL, model, api.authToken)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return nil, NewAPINotImplementedError(modelDeletePath, resp.StatusCode)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var deleteResp DeleteResponse
	if err := json.NewDecoder(resp.Body).Decode(&deleteResp); err != nil {
		return nil, err
	}

	return &deleteResp, nil
}

// ListModels lists all models currently in the HuggingFace cache.
// Returns all model revisions found in the cache directory with their status.
// Returns ErrAPINotImplemented if the ML node doesn't support this endpoint.
func (api *Client) ListModels(ctx context.Context) (*ModelListResponse, error) {
	requestURL, err := url.JoinPath(api.pocUrl, modelListPath)
	if err != nil {
		return nil, err
	}

	resp, err := utils.SendGetRequestWithAuth(ctx, &api.client, requestURL, api.authToken)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return nil, NewAPINotImplementedError(modelListPath, resp.StatusCode)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var listResp ModelListResponse
	if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
		return nil, err
	}

	return &listResp, nil
}

// GetDiskSpace retrieves information about disk space usage and availability.
// Returns cache size, available disk space, and cache directory path.
// Returns ErrAPINotImplemented if the ML node doesn't support this endpoint.
func (api *Client) GetDiskSpace(ctx context.Context) (*DiskSpaceInfo, error) {
	requestURL, err := url.JoinPath(api.pocUrl, modelSpacePath)
	if err != nil {
		return nil, err
	}

	resp, err := utils.SendGetRequestWithAuth(ctx, &api.client, requestURL, api.authToken)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return nil, NewAPINotImplementedError(modelSpacePath, resp.StatusCode)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var spaceInfo DiskSpaceInfo
	if err := json.NewDecoder(resp.Body).Decode(&spaceInfo); err != nil {
		return nil, err
	}

	return &spaceInfo, nil
}
