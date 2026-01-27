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

// DefaultTransport provides connection pooling configuration to prevent ephemeral port exhaustion.
// These settings limit the number of idle connections and set appropriate timeouts.
var DefaultTransport = &http.Transport{
	MaxIdleConns:        100,              // Total max idle connections across all hosts
	MaxIdleConnsPerHost: 10,               // Max idle connections per host
	MaxConnsPerHost:     20,               // Max total connections per host
	IdleConnTimeout:     90 * time.Second, // How long to keep idle connections
}

// SharedHTTPClient is a pre-configured client for general use.
// Use this for most HTTP requests to benefit from connection pooling.
var SharedHTTPClient = &http.Client{
	Transport: DefaultTransport,
	Timeout:   30 * time.Second,
}

// SharedNoRedirectHTTPClient is like SharedHTTPClient but doesn't follow redirects.
// Use this when you need to handle redirects manually (e.g., for executor proxying).
var SharedNoRedirectHTTPClient = &http.Client{
	Transport: DefaultTransport,
	Timeout:   30 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// NewHttpClient creates an HTTP client with the specified timeout and connection pooling.
// For most use cases, prefer using SharedHTTPClient instead.
func NewHttpClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: DefaultTransport,
		Timeout:   timeout,
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

func SendDeleteJsonRequest(ctx context.Context, client *http.Client, url string, payload any) (*http.Response, error) {
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

	return client.Do(req)
}
