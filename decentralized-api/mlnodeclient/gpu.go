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
	gpuDevicesPath = "/api/v1/gpu/devices"
	gpuDriverPath  = "/api/v1/gpu/driver"
)

// GetGPUDevices retrieves information about all CUDA devices on the ML node.
// Returns empty list if no GPUs are present or NVML is not initialized.
// Returns ErrAPINotImplemented if the ML node doesn't support this endpoint.
func (api *Client) GetGPUDevices(ctx context.Context) (*GPUDevicesResponse, error) {
	requestURL, err := url.JoinPath(api.pocUrl, gpuDevicesPath)
	if err != nil {
		return nil, err
	}

	resp, err := utils.SendGetRequestWithAuth(ctx, &api.client, requestURL, api.authToken)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return nil, NewAPINotImplementedError(gpuDevicesPath, resp.StatusCode)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var devicesResp GPUDevicesResponse
	if err := json.NewDecoder(resp.Body).Decode(&devicesResp); err != nil {
		return nil, err
	}

	return &devicesResp, nil
}

// GetGPUDriver retrieves CUDA driver version information from NVML.
// Returns ErrAPINotImplemented if the ML node doesn't support this endpoint.
// Returns error if NVML is not initialized or driver info cannot be retrieved.
func (api *Client) GetGPUDriver(ctx context.Context) (*DriverInfo, error) {
	requestURL, err := url.JoinPath(api.pocUrl, gpuDriverPath)
	if err != nil {
		return nil, err
	}

	resp, err := utils.SendGetRequestWithAuth(ctx, &api.client, requestURL, api.authToken)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return nil, NewAPINotImplementedError(gpuDriverPath, resp.StatusCode)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var driverInfo DriverInfo
	if err := json.NewDecoder(resp.Body).Decode(&driverInfo); err != nil {
		return nil, err
	}

	return &driverInfo, nil
}
