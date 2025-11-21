package utils

import (
	"bytes"
	"context"
	"decentralized-api/logging"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/productscience/inference/x/inference/types"
)

func NewHttpClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
	}
}

func SendPostJsonRequest(ctx context.Context, client *http.Client, url string, payload any) (*http.Response, error) {
	return SendPostJsonRequestWithAuth(ctx, client, url, payload, "")
}

// SendPostJsonRequestWithAuth sends a POST request with JSON payload and adds Authorization header if authToken is set
func SendPostJsonRequestWithAuth(ctx context.Context, client *http.Client, url string, payload any, authToken string) (*http.Response, error) {
	var req *http.Request
	var err error

	if payload == nil {
		// Create a POST request with no body if payload is nil.
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	} else {
		// Marshal the payload to JSON.
		var jsonData []byte
		jsonData, err = json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBuffer(jsonData))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
	}

	if err != nil {
		return nil, err
	}
	if req == nil {
		logging.Error("SendPostJsonRequestWithAuth. Failed to create HTTP request", types.Server, "url", url, "payload", payload)
		return nil, err
	}

	if strings.TrimSpace(authToken) != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", strings.TrimSpace(authToken)))
	}

	return client.Do(req)
}

func SendGetRequest(ctx context.Context, client *http.Client, url string) (*http.Response, error) {
	return SendGetRequestWithAuth(ctx, client, url, "")
}

// SendGetRequestWithAuth sends a GET request and adds Authorization header if authToken is set
func SendGetRequestWithAuth(ctx context.Context, client *http.Client, url string, authToken string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	if strings.TrimSpace(authToken) != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", strings.TrimSpace(authToken)))
	}

	return client.Do(req)
}

func SendDeleteJsonRequest(ctx context.Context, client *http.Client, url string, payload any) (*http.Response, error) {
	return SendDeleteJsonRequestWithAuth(ctx, client, url, payload, "")
}

// SendDeleteJsonRequestWithAuth sends a DELETE request with JSON payload and adds Authorization header if authToken is set
func SendDeleteJsonRequestWithAuth(ctx context.Context, client *http.Client, url string, payload any, authToken string) (*http.Response, error) {
	var req *http.Request
	var err error

	if payload == nil {
		// Create a DELETE request with no body if payload is nil.
		req, err = http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	} else {
		// Marshal the payload to JSON.
		var jsonData []byte
		jsonData, err = json.Marshal(payload)
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
		logging.Error("SendDeleteJsonRequestWithAuth. Failed to create HTTP request", types.Server, "url", url, "payload", payload)
		return nil, err
	}

	if strings.TrimSpace(authToken) != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", strings.TrimSpace(authToken)))
	}

	return client.Do(req)
}
