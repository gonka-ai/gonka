package validationinf

import (
	"context"
	"fmt"
	"net/http"
)

// PayloadRetriever fetches and verifies inference payloads from executor instances.
type PayloadRetriever struct{}

// PayloadResult holds the verified prompt and response payloads.
type PayloadResult struct {
	InferenceID       string
	PromptPayload     []byte
	ResponsePayload   []byte
	ExecutorSignature string
}

// Fetch retrieves the payload for inferenceID from the executor at baseURL.
// It verifies the executor's signature and checks payload hashes against on-chain commitments.
// Stub: real signature verification and hash checking not yet implemented.
func (r *PayloadRetriever) Fetch(ctx context.Context, baseURL, inferenceID string, epochID uint64) (*PayloadResult, error) {
	url := fmt.Sprintf("%s/v1/inference/payloads?inference_id=%s", baseURL, inferenceID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("payload: build request: %w", err)
	}

	// TODO: set required headers (X-Validator-Address, X-Timestamp, X-Epoch-Id, Authorization)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("payload: fetch from %s: %w", baseURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("payload: executor returned %d", resp.StatusCode)
	}

	// TODO: decode response body, verify signature, verify hashes
	return nil, fmt.Errorf("payload: decoding not yet implemented")
}
