package utils

import (
	"bytes"
	"context"
	"decentralized-api/logging"
	"encoding/json"
	"net/http"
	"time"

	"github.com/productscience/inference/x/inference/types"
)

func NewHttpClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
	}
}

// SetBearerAuth sets the Authorization header to a bearer token, but only when
// the token is non-empty. This is the single place the "Bearer " prefix and the
// only-when-present rule live.
func SetBearerAuth(req *http.Request, token string) {
	if token != "" {
		req.Header.Set(AuthorizationHeader, "Bearer "+token)
	}
}

func SendPostJsonRequest(ctx context.Context, client *http.Client, url string, payload any) (*http.Response, error) {
	return SendPostJsonRequestWithAuth(ctx, client, url, payload, "")
}

func SendPostJsonRequestWithAuth(ctx context.Context, client *http.Client, url string, payload any, authToken string) (*http.Response, error) {
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
	SetBearerAuth(req, authToken)

	return client.Do(req)
}

func SendGetRequest(ctx context.Context, client *http.Client, url string) (*http.Response, error) {
	return SendGetRequestWithAuth(ctx, client, url, "")
}

func SendGetRequestWithAuth(ctx context.Context, client *http.Client, url string, authToken string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	SetBearerAuth(req, authToken)

	return client.Do(req)
}

func SendDeleteJsonRequest(ctx context.Context, client *http.Client, url string, payload any) (*http.Response, error) {
	return SendDeleteJsonRequestWithAuth(ctx, client, url, payload, "")
}

func SendDeleteJsonRequestWithAuth(ctx context.Context, client *http.Client, url string, payload any, authToken string) (*http.Response, error) {
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
		logging.Error("SendDeleteJsonRequest. Failed to create HTTP request", types.Server, "url", url, "payload", payload)
		return nil, err
	}
	SetBearerAuth(req, authToken)

	return client.Do(req)
}
