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

// ErrPayloadGone indicates the executor returned 404 for a payload retrieval
// request. The payload has been pruned (e.g. by per-inference Tier A pruning
// after the inference reached a terminal status, or by epoch sweep). Callers
// should propagate this sentinel so the validator skips silently rather than
// surfacing the retrieval failure as a validation error.
var ErrPayloadGone = errors.New("payload no longer available on executor")

// ErrPayloadTooLarge indicates the executor served more than the read cap
// (per-inference PayloadResponseByteLimit, or MaxPayloadResponseBytes when
// the caller did not pass one). Retrying is pointless: the body is
// deterministic from the validator's point of view and each attempt costs
// the full transfer.
var ErrPayloadTooLarge = errors.New("executor payload response too large")

const (
	// maxPromptPayloadBytes matches the gateway chat-request body cap
	// (devshardctl MaxChatRequestBodySize). The prompt is stored as JSON, not
	// as a token stream, so the body cap — not inputTokens — bounds this side.
	maxPromptPayloadBytes = 10 << 20

	// maxSSEBytesPerOutputToken ceilings one streamed token with wide
	// logprobs.top_logprobs (~330 B at 3 alternatives; low single-digit KiB at 20).
	maxSSEBytesPerOutputToken = 8 << 10

	// MaxPayloadResponseBytes is the default read cap when the caller does not
	// pass a per-inference limit (unknown token counts).
	MaxPayloadResponseBytes = 64 << 20

	// maxPayloadResponseBytesHard is the last-resort memory bound on a derived
	// per-inference cap. A 200k-token generation with wide logprobs can exceed
	// this; operators raising request_max_tokens_cap that far need a larger
	// process limit, but unbounded derived caps would let a claimed token count
	// OOM the validator.
	maxPayloadResponseBytesHard = 512 << 20

	// maxPayloadErrorBodyBytes caps how much of a non-200 body is quoted into
	// the returned error. That text reaches validation logs and, for
	// executor-fault verdicts, the published vote details, so keep it to the
	// leading span that identifies the failure (status line, proxy error page
	// title) rather than the whole document.
	maxPayloadErrorBodyBytes = 512

	// maxPayloadErrorQuotedBytes bounds the snippet after quoting. Escaping
	// binary expands up to 4x, so the raw cap alone is not a bound on the
	// logged length.
	maxPayloadErrorQuotedBytes = 512
)

// PayloadResponseByteLimit is the read cap for one payload GET. The response
// side is derived from the output-token count the executor committed to on
// chain, so the cap tracks gateway request_max_tokens_cap instead of
// desynchronising from it the way a single constant does.
//
// inputTokens is deliberately not a parameter: the prompt is stored as a JSON
// document and bounded by the gateway body cap, not by a token count, so the
// prompt allowance is flat.
func PayloadResponseByteLimit(outputTokens uint64) int64 {
	// Past this many tokens the response term alone exceeds the hard bound, so
	// clip before multiplying. Without this guard a large claimed token count
	// overflows int64 and yields a *smaller* cap than a modest one.
	const maxDerivableOutputTokens = maxPayloadResponseBytesHard / maxSSEBytesPerOutputToken
	if outputTokens > maxDerivableOutputTokens {
		return maxPayloadResponseBytesHard
	}
	promptWire := int64(maxPromptPayloadBytes) * 4 / 3
	respWire := int64(outputTokens) * maxSSEBytesPerOutputToken * 4 / 3
	total := promptWire + respWire + 64<<10
	if total > maxPayloadResponseBytesHard {
		return maxPayloadResponseBytesHard
	}
	return total
}

func payloadReadLimit(maxBytes int64) int64 {
	if maxBytes <= 0 {
		return MaxPayloadResponseBytes
	}
	if maxBytes > maxPayloadResponseBytesHard {
		return maxPayloadResponseBytesHard
	}
	return maxBytes
}

// PayloadRetrievalClient is the default HTTP client for payload retrieval.
var PayloadRetrievalClient = &http.Client{
	Timeout: 30 * time.Second,
}

// cappedReader fails with ErrPayloadTooLarge once more than remaining bytes are
// read, so json.Decoder surfaces the cap as a distinguishable error instead of
// an ambiguous unexpected EOF.
type cappedReader struct {
	r         io.Reader
	remaining int64
}

// quotedSnippet renders an executor-supplied body for an error message: quoted
// so it cannot forge log lines, and bounded so it cannot flood them.
func quotedSnippet(body []byte) string {
	quoted := strconv.Quote(string(body))
	if len(quoted) <= maxPayloadErrorQuotedBytes {
		return quoted
	}
	return quoted[:maxPayloadErrorQuotedBytes] + `..."`
}

func (c *cappedReader) Read(p []byte) (int, error) {
	if c.remaining <= 0 {
		return 0, ErrPayloadTooLarge
	}
	if int64(len(p)) > c.remaining {
		p = p[:c.remaining]
	}
	n, err := c.r.Read(p)
	c.remaining -= int64(n)
	return n, err
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
	client *http.Client,
	requestUrl string,
	validatorAddress string,
	timestamp int64,
	epochId uint64,
	signature string,
	maxBytes int64,
) (_ *PayloadResponse, retErr error) {
	ctx, op := payloadFetchObserver.StartPayloadFetch(ctx, requestUrl, validatorAddress, int64(epochId))
	defer op.FinishErr(&retErr)

	if client == nil {
		client = PayloadRetrievalClient
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestUrl, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set(utils.XValidatorAddressHeader, validatorAddress)
	req.Header.Set(utils.XTimestampHeader, strconv.FormatInt(timestamp, 10))
	req.Header.Set(utils.XEpochIdHeader, strconv.FormatUint(epochId, 10))
	req.Header.Set(utils.AuthorizationHeader, signature)
	payloadFetchObserver.InjectRequestContext(ctx, req.Header)
	payloadFetchObserver.AttachRequestID(req)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("payload not found on executor: %w", ErrPayloadGone)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxPayloadErrorBodyBytes))
		return nil, fmt.Errorf("executor returned status %d: %s", resp.StatusCode, quotedSnippet(body))
	}

	var payloadResp PayloadResponse
	limit := payloadReadLimit(maxBytes)
	body := &cappedReader{r: resp.Body, remaining: limit}
	if err := json.NewDecoder(body).Decode(&payloadResp); err != nil {
		if errors.Is(err, ErrPayloadTooLarge) {
			return nil, fmt.Errorf("%w: over %d bytes", ErrPayloadTooLarge, limit)
		}
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
