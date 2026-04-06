package engineinf

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"common/mlnode"

	"github.com/labstack/echo/v4"
)

// PayloadWriter is the narrow storage interface the executor needs.
type PayloadWriter interface {
	StorePayload(ctx context.Context, inferenceID string, epochID uint64, prompt, response []byte) error
}

// InferenceChain is the narrow chain interface the executor needs.
// Stubbed — will be wired when chain/ is implemented.
type InferenceChain interface {
	FinishInference(ctx context.Context, inferenceID string) error
}

// ModifyRequest forces logprob fields and syncs max_tokens/max_completion_tokens in
// the JSON request body. Returns the input unchanged if it cannot be parsed.
func ModifyRequest(body []byte) ([]byte, error) {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return body, nil
	}

	req["logprobs"] = true
	req["top_logprobs"] = 5

	maxTokens, hasMaxTokens := req["max_tokens"]
	maxCompTokens, hasMaxCompTokens := req["max_completion_tokens"]

	switch {
	case hasMaxCompTokens && !hasMaxTokens:
		req["max_tokens"] = maxCompTokens
	case hasMaxTokens && !hasMaxCompTokens:
		req["max_completion_tokens"] = maxTokens
	}

	out, err := json.Marshal(req)
	if err != nil {
		return body, err
	}
	return out, nil
}

// Executor handles the executor path of a POST /v1/chat/completions request.
// It acquires an ML node, forwards the modified request, proxies the response,
// stores the payload, and submits MsgFinishInference.
type Executor struct {
	Lock    mlnode.NodeLock
	Storage PayloadWriter  // nil-safe: payload storing is skipped if nil
	Chain   InferenceChain // nil-safe: chain submission is skipped if nil
}

// Handle executes the inference on an ML node.
// inferenceID is taken from X-Inference-Id header (guaranteed non-empty by caller).
// rawBody is the original request body bytes.
func (ex *Executor) Handle(c echo.Context, inferenceID string, model string, rawBody []byte) error {
	ctx := c.Request().Context()

	modifiedBody, err := ModifyRequest(rawBody)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to modify request"})
	}

	resp, err := mlnode.DoWithNode(ctx, ex.Lock, model, maxRetries, func(ctx context.Context, endpoint string) (*http.Response, error) {
		url := strings.TrimRight(endpoint, "/") + "/v1/chat/completions"
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(modifiedBody))
		if err != nil {
			return nil, fmt.Errorf("executor: build request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		return http.DefaultClient.Do(req)
	})

	if err != nil {
		return c.JSON(http.StatusBadGateway, map[string]string{"error": err.Error()})
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); ct != "" {
		c.Response().Header().Set("Content-Type", ct)
	}
	c.Response().WriteHeader(http.StatusOK)

	var buf bytes.Buffer
	scanner := bufio.NewScanner(io.TeeReader(resp.Body, &buf))
	w := c.Response()
	for scanner.Scan() {
		line := scanner.Bytes()
		if _, err := w.Write(append(line, '\n')); err != nil {
			slog.Warn("executor: error writing response line", "inference_id", inferenceID, "error", err)
			break
		}
		w.Flush()
	}
	if err := scanner.Err(); err != nil {
		slog.Warn("executor: error reading response body", "inference_id", inferenceID, "error", err)
	}

	if ex.Storage != nil {
		if err := ex.Storage.StorePayload(ctx, inferenceID, 0, rawBody, buf.Bytes()); err != nil {
			slog.Warn("executor: failed to store payload", "inference_id", inferenceID, "error", err)
		}
	}

	if ex.Chain != nil {
		if err := ex.Chain.FinishInference(ctx, inferenceID); err != nil {
			slog.Warn("executor: failed to finish inference on chain", "inference_id", inferenceID, "error", err)
		}
	}

	return nil
}
