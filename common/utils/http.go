package utils

import (
	"bytes"
	"common/logging"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/productscience/inference/x/inference/types"
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
		// Marshal the payload to JSON. Note: assign with = (not :=) so a
		// marshalling or request-construction failure is not swallowed by a
		// shadowed err, which previously could return (nil, nil).
		var jsonData []byte
		jsonData, err = json.Marshal(payload)
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
		return nil, fmt.Errorf("failed to create POST request for %s", url)
	}
	// Declare the body we are actually sending. This is not fixing a live
	// failure: FastAPI falls back to JSON parsing when no content-type header is
	// present at all (routing.py, the `if not content_type_value` branch), which
	// is why these calls have worked without it. Send it because it is correct,
	// it matches what SendDeleteJsonRequest already does, and it is what keeps
	// these calls working if strict content-type checking is ever enabled
	// upstream (a proxy, or a stricter framework default).
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
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
