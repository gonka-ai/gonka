package utils

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/productscience/inference/x/inference/types"

	"common/logging"
)

func NewHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
	}
}

func SendPostJSONRequest(ctx context.Context, client *http.Client, url string, payload any) (*http.Response, error) {
	var req *http.Request
	var err error

	if payload == nil {
		// Create a POST request with no body if payload is nil.
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	} else {
		// Marshal the payload to JSON.
		jsonData, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			return nil, marshalErr
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBuffer(jsonData))
	}

	if err != nil {
		return nil, err
	}
	if req == nil {
		logging.Error("SendPostJSONRequest. Failed to create HTTP request", types.Server, "url", url, "payload", payload)
		return nil, err
	}

	return client.Do(req)
}

func SendGetRequest(ctx context.Context, client *http.Client, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	return client.Do(req)
}

func SendDeleteJSONRequest(ctx context.Context, client *http.Client, url string, payload any) (*http.Response, error) {
	var req *http.Request
	var err error

	if payload == nil {
		// Create a DELETE request with no body if payload is nil.
		req, err = http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	} else {
		// Marshal the payload to JSON.
		jsonData, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodDelete, url, bytes.NewBuffer(jsonData))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
	}

	if err != nil {
		return nil, err
	}
	if req == nil {
		logging.Error("SendDeleteJSONRequest. Failed to create HTTP request", types.Server, "url", url, "payload", payload)
		return nil, err
	}

	return client.Do(req)
}
