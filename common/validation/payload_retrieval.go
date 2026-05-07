package validation

import (
	"common/completionapi"
	"common/logging"
	"common/utils"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"
)

// ErrHashMismatch indicates executor served payload with valid signature but hash doesn't match on-chain commitment.
// This should trigger immediate invalidation (no retry).
var ErrHashMismatch = errors.New("hash mismatch: executor served wrong payload with valid signature")

// ErrEpochStale indicates inference epoch is too old (currentEpoch >= inferenceEpoch + 2).
// Validation is no longer useful - abort without invalidation.
var ErrEpochStale = errors.New("inference epoch too old, validation no longer useful")

const defaultPayloadRetrievalTimeout = 30 * time.Second

var defaultPayloadRetrievalClient = newPayloadRetrievalClient(defaultPayloadRetrievalTimeout)

func newPayloadRetrievalClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// PayloadResponse matches the executor endpoint response.
// Used by both chain validation and devshard validation paths.
type PayloadResponse struct {
	InferenceId       string `json:"inference_id"`
	PromptPayload     []byte `json:"prompt_payload"`
	ResponsePayload   []byte `json:"response_payload"`
	ExecutorSignature string `json:"executor_signature"`
}

// FetchPayloadsHTTP makes a GET request to retrieve payloads from an executor.
// This is a low-level helper that handles only the HTTP request/response.
// Caller is responsible for URL construction, request signing, and response verification.
func FetchPayloadsHTTP(
	ctx context.Context,
	requestUrl string,
	validatorAddress string,
	timestamp int64,
	epochId uint64,
	signature string,
) (*PayloadResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestUrl, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set(utils.XValidatorAddressHeader, validatorAddress)
	req.Header.Set(utils.XTimestampHeader, strconv.FormatInt(timestamp, 10))
	req.Header.Set(utils.XEpochIdHeader, strconv.FormatUint(epochId, 10))
	req.Header.Set(utils.AuthorizationHeader, signature)

	resp, err := defaultPayloadRetrievalClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("payload not found on executor")
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("executor returned status %d: %s", resp.StatusCode, string(body))
	}

	var payloadResp PayloadResponse
	if err := json.NewDecoder(resp.Body).Decode(&payloadResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &payloadResp, nil
}

// computePromptHash computes the hash of a prompt payload.
// Matches getPromptHash in post_chat_handler.go.
func computePromptHash(promptPayload []byte) (string, error) {
	canonical, err := utils.CanonicalizeJSON(promptPayload)
	if err != nil {
		return "", err
	}
	return utils.GenerateSHA256Hash(canonical), nil
}

// computeResponseHash computes the hash of a response payload.
func computeResponseHash(responsePayload []byte) (string, error) {
	resp, err := completionapi.NewCompletionResponseFromLinesFromResponsePayload(responsePayload)
	if err != nil {
		return "", err
	}
	return resp.GetHash()
}

// VerifyPayloadHashes checks that the actual payloads match the expected hashes.
// Returns ErrHashMismatch if any hash doesn't match.
// Empty expected hashes are skipped (backward compatibility).
func VerifyPayloadHashes(
	promptPayload []byte,
	responsePayload []byte,
	expectedPromptHash string,
	expectedResponseHash string,
	inferenceId string,
) error {
	if expectedPromptHash != "" {
		actualPromptHash, err := computePromptHash(promptPayload)
		if err != nil {
			logging.Error("Failed to compute prompt hash, executor served malformed payload", types.Validation,
				"inferenceId", inferenceId, "error", err)
			return ErrHashMismatch
		}
		if actualPromptHash != expectedPromptHash {
			logging.Error("Prompt hash mismatch, executor served wrong payload", types.Validation,
				"inferenceId", inferenceId,
				"expectedHash", expectedPromptHash,
				"actualHash", actualPromptHash)
			return ErrHashMismatch
		}
	}

	if expectedResponseHash != "" {
		actualResponseHash, err := computeResponseHash(responsePayload)
		if err != nil {
			logging.Error("Failed to compute response hash, executor served malformed payload", types.Validation,
				"inferenceId", inferenceId, "error", err)
			return ErrHashMismatch
		}
		if actualResponseHash != expectedResponseHash {
			logging.Error("Response hash mismatch, executor served wrong payload", types.Validation,
				"inferenceId", inferenceId,
				"expectedHash", expectedResponseHash,
				"actualHash", actualResponseHash)
			return ErrHashMismatch
		}
	}

	return nil
}

// BuildPayloadRequestURL constructs the URL for payload retrieval.
func BuildPayloadRequestURL(baseUrl string, path string, inferenceId string) (string, error) {
	fullUrl, err := url.JoinPath(baseUrl, path)
	if err != nil {
		return "", fmt.Errorf("failed to build base URL: %w", err)
	}
	parsedUrl, err := url.Parse(fullUrl)
	if err != nil {
		return "", fmt.Errorf("failed to parse base URL: %w", err)
	}
	query := parsedUrl.Query()
	query.Set("inference_id", inferenceId)
	parsedUrl.RawQuery = query.Encode()
	return parsedUrl.String(), nil
}

// VerifyExecutorPayloadSignature verifies the executor's signature on the payload response.
// This provides non-repudiation: if executor serves wrong payload, validator has cryptographic proof.
// Executor signs: inferenceId + promptHash + responseHash (with timestamp=0)
func VerifyExecutorPayloadSignature(
	inferenceId string,
	promptPayload []byte,
	responsePayload []byte,
	signature string,
	executorAddress string,
	executorPubkeys []string,
) error {
	if signature == "" {
		return fmt.Errorf("executor signature is empty")
	}

	promptHash := utils.GenerateSHA256HashBytes(promptPayload)
	responseHash := utils.GenerateSHA256HashBytes(responsePayload)
	payload := inferenceId + promptHash + responseHash

	components := calculations.SignatureComponents{
		Payload:         payload,
		Timestamp:       0, // Executor uses timestamp=0 for non-repudiation signatures
		TransferAddress: executorAddress,
		ExecutorAddress: "",
	}

	return calculations.ValidateSignatureWithGrantees(components, calculations.Developer, executorPubkeys, signature)
}
