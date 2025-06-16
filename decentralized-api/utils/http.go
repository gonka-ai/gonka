package utils

import (
	"bytes"
	"context"
	"decentralized-api/logging"
	"encoding/json"
	"github.com/productscience/inference/x/inference/types"
	"net/http"
	"time"
)

func NewHttpClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
	}
}

func SendPostJsonRequest(ctx context.Context, client *http.Client, url string, payload any) (*http.Response, error) {
	var req *http.Request
	var err error

	if payload == nil {
		// Create a POST request with no body if payload is nil.
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	} else {
		// Marshal the payload to JSON.
		jsonData, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBuffer(jsonData))
	}

	if err != nil {
		return nil, err
	}
	if req == nil {
		logging.Error("SendPostJsonRequest. Failed to create HTTP request", types.Server, "url", url, "payload", payload)
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
