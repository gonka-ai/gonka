package inference

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	commonvalidation "common/validation"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/cmd/inferenced/cmd"
	"github.com/productscience/inference/x/inference/calculations"
	chaintypes "github.com/productscience/inference/x/inference/types"

	devshardpkg "devshard"
	"devshard/bridge"
	"devshard/observability"
)

// errExecutorPayloadFault tags failures that are the executor's responsibility
// (payload HTTP errors, bad signature, hash mismatch). Validator.Validate
// converts tagged errors into Valid:false when the vote-false-on-fetch switch
// is on.
var errExecutorPayloadFault = errors.New("executor payload fault")

func tagExecutorPayloadFault(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, errExecutorPayloadFault) {
		return err
	}
	return fmt.Errorf("%w: %w", errExecutorPayloadFault, err)
}

func signPayloadRequest(
	recorder PayloadAuthClient,
	inferenceID string,
	timestamp int64,
	validatorAddress string,
	epochID uint64,
) (string, error) {
	components := calculations.SignatureComponents{
		Payload:         inferenceID,
		EpochId:         epochID,
		Timestamp:       timestamp,
		TransferAddress: validatorAddress,
		ExecutorAddress: "",
	}

	signerAddress, err := sdk.AccAddressFromBech32(recorder.GetSignerAddress())
	if err != nil {
		return "", err
	}
	accountSigner := &cmd.AccountSigner{
		Addr:    signerAddress,
		Keyring: recorder.GetKeyring(),
	}
	return calculations.Sign(accountSigner, components, calculations.Developer)
}

func resolveExecutorPubKeys(ctx context.Context, recorder PayloadAuthClient, executorAddress string) ([]string, error) {
	qc := recorder.NewInferenceQueryClient()

	grantees, err := qc.GranteesByMessageType(ctx, &chaintypes.QueryGranteesByMessageTypeRequest{
		GranterAddress: executorAddress,
		MessageTypeUrl: "/inference.inference.MsgStartInference",
	})
	if err != nil {
		return nil, fmt.Errorf("query executor grantees: %w", err)
	}
	pubkeys := make([]string, 0, len(grantees.Grantees)+1)
	for _, g := range grantees.Grantees {
		pubkeys = append(pubkeys, g.PubKey)
	}

	participant, err := qc.AccountByAddress(ctx, &chaintypes.QueryAccountByAddressRequest{
		Address: executorAddress,
	})
	if err != nil {
		return nil, fmt.Errorf("query executor participant: %w", err)
	}
	if participant.Pubkey != "" {
		pubkeys = append(pubkeys, participant.Pubkey)
	}
	return pubkeys, nil
}

func fetchPayloadsFromExecutor(
	ctx context.Context,
	br bridge.MainnetBridge,
	recorder PayloadAuthClient,
	req devshardpkg.ValidateRequest,
	inferenceID string,
	epochID uint64,
	requestPath string,
	client *http.Client,
) ([]byte, []byte, error) {
	executorInfo, err := br.GetHostInfo(req.ExecutorAddress)
	if err != nil {
		return nil, nil, fmt.Errorf("get executor info: %w", err)
	}
	if executorInfo.URL == "" {
		return nil, nil, fmt.Errorf("executor has no URL")
	}

	requestURL, err := commonvalidation.BuildPayloadRequestURL(executorInfo.URL, requestPath, inferenceID)
	if err != nil {
		return nil, nil, err
	}

	timestamp := time.Now().UnixNano()
	validatorAddress := recorder.GetAccountAddress()
	signature, err := signPayloadRequest(recorder, inferenceID, timestamp, validatorAddress, epochID)
	if err != nil {
		return nil, nil, fmt.Errorf("sign request: %w", err)
	}

	payloadResp, err := fetchPayloadsHTTPWithRetry(
		ctx, client, requestURL, validatorAddress, timestamp, epochID, signature,
		commonvalidation.PayloadResponseByteLimit(req.OutputTokens),
	)
	if err != nil {
		if errors.Is(err, commonvalidation.ErrPayloadGone) || ctx.Err() != nil {
			return nil, nil, err
		}
		return nil, nil, tagExecutorPayloadFault(err)
	}

	encodedPubKeys, err := resolveExecutorPubKeys(ctx, recorder, req.ExecutorAddress)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve executor pubkeys: %w", err)
	}

	if err := commonvalidation.VerifyExecutorPayloadSignature(
		inferenceID,
		payloadResp.PromptPayload,
		payloadResp.ResponsePayload,
		payloadResp.ExecutorSignature,
		req.ExecutorAddress,
		encodedPubKeys,
	); err != nil {
		return nil, nil, tagExecutorPayloadFault(fmt.Errorf("verify executor signature: %w", err))
	}

	promptHash := sha256.Sum256(payloadResp.PromptPayload)
	if !bytes.Equal(promptHash[:], req.PromptHash) {
		return nil, nil, tagExecutorPayloadFault(fmt.Errorf("%w: prompt expected %x got %x", commonvalidation.ErrHashMismatch, req.PromptHash, promptHash[:]))
	}

	responseHash := sha256.Sum256(payloadResp.ResponsePayload)
	if !bytes.Equal(responseHash[:], req.ResponseHash) {
		return nil, nil, tagExecutorPayloadFault(fmt.Errorf("%w: response expected %x got %x", commonvalidation.ErrHashMismatch, req.ResponseHash, responseHash[:]))
	}

	return payloadResp.PromptPayload, payloadResp.ResponsePayload, nil
}

const payloadFetchAttempts = 2

// payloadFetchTimeout bounds the whole GET including body transfer. A streamed
// or large payload can still complete after headers; shrinking this to the
// TTFB budget would clip honest work.
const payloadFetchTimeout = 30 * time.Second

// payloadFetchHeaderTimeout is time-to-first-byte: dial, TLS, and response
// headers. A silent executor fails here instead of occupying a worker for the
// full body timeout on each attempt. Overridable in tests.
var payloadFetchHeaderTimeout = 10 * time.Second

var payloadFetchRetryBackoff = 500 * time.Millisecond

func newPayloadFetchClient() *http.Client {
	transport := cloneHTTPTransport()
	transport.ResponseHeaderTimeout = payloadFetchHeaderTimeout
	transport.TLSHandshakeTimeout = payloadFetchHeaderTimeout
	transport.DialContext = (&net.Dialer{
		Timeout:   payloadFetchHeaderTimeout,
		KeepAlive: 30 * time.Second,
	}).DialContext

	// net/http ignores ResponseHeaderTimeout on HTTP/2, which would silently
	// give a TLS executor the full body timeout to send headers. A payload GET
	// is one small JSON document, so pin HTTP/1.1 to keep the TTFB bound.
	transport.ForceAttemptHTTP2 = false
	transport.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}

	return &http.Client{
		Timeout:   payloadFetchTimeout,
		Transport: ttfbRoundTripper{base: transport},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func cloneHTTPTransport() *http.Transport {
	if t, ok := http.DefaultTransport.(*http.Transport); ok && t != nil {
		return t.Clone()
	}
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

// ttfbRoundTripper records time-to-first-byte for a payload GET. RoundTrip
// returns once the response headers are parsed and before the body is read, so
// its duration is exactly TTFB.
//
// Only successful round trips are recorded. A blackholing executor would
// otherwise fill the histogram with samples pinned at the header timeout,
// inflating the very p99 the timeout is meant to be sized from.
type ttfbRoundTripper struct {
	base http.RoundTripper
}

func (t ttfbRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	observability.ObservePayloadFetchTTFB(time.Since(start))
	return resp, nil
}

func fetchPayloadsHTTPWithRetry(
	ctx context.Context,
	client *http.Client,
	requestURL, validatorAddress string,
	timestamp int64,
	epochID uint64,
	signature string,
	maxBytes int64,
) (*commonvalidation.PayloadResponse, error) {
	var lastErr error
	for attempt := 1; attempt <= payloadFetchAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		payloadResp, err := commonvalidation.FetchPayloadsHTTP(
			ctx, client, requestURL, validatorAddress, timestamp, epochID, signature, maxBytes,
		)
		if err == nil {
			return payloadResp, nil
		}
		if errors.Is(err, commonvalidation.ErrPayloadGone) || errors.Is(err, commonvalidation.ErrPayloadTooLarge) {
			return nil, err
		}
		lastErr = err
		if attempt == payloadFetchAttempts {
			break
		}
		timer := time.NewTimer(payloadFetchRetryBackoff)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, lastErr
}

func classifyExecuteValidationErr(err error) error {
	if err == nil {
		return nil
	}
	var classified *observability.ClassifiedError
	if errors.As(err, &classified) {
		return err
	}
	msg := err.Error()
	switch {
	case errors.Is(err, io.EOF) || strings.Contains(msg, "read"):
		return observability.Classify(observability.ReasonValidationReadErr, observability.WhereRuntimeValidate, err)
	case strings.Contains(msg, "unmarshal") || strings.Contains(msg, "parse validation"):
		return observability.Classify(observability.ReasonValidationParseErr, observability.WhereRuntimeValidate, err)
	default:
		return observability.Classify(observability.ReasonValidateErr, observability.WhereRuntimeValidate, err)
	}
}
