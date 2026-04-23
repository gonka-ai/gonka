package transport

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	json "github.com/goccy/go-json"

	"subnet/host"
	"subnet/signing"
	"subnet/types"
)

var sharedTransports sync.Map // baseURL -> *http.Transport

func getTransport(baseURL string) *http.Transport {
	if t, ok := sharedTransports.Load(baseURL); ok {
		return t.(*http.Transport)
	}
	fallbackAddress := transportAddress(baseURL)
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	t := &http.Transport{
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     120 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		DialContext:         DefaultHostConnectionTracker().TrackDialContext(dialer.DialContext, fallbackAddress),
	}
	actual, _ := sharedTransports.LoadOrStore(baseURL, t)
	return actual.(*http.Transport)
}

func transportAddress(baseURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err == nil && parsed != nil && parsed.Host != "" {
		return parsed.Hostname()
	}
	return strings.TrimSpace(baseURL)
}

// ClientConfig holds per-endpoint timeout settings.
type ClientConfig struct {
	InferenceTimeout time.Duration // /chat/completions, default 20m
	GossipTimeout    time.Duration // gossip/nonce, gossip/txs, default 10s
	VerifyTimeout    time.Duration // verify-timeout, default 3m
	QueryTimeout     time.Duration // diffs, mempool GETs, default 30s
	// ParticipantKey is the canonical participant identifier passed to
	// the admission controller for both AllowRequest and ObserveResult.
	// Callers MUST use the participant's gonka validator address
	// (bech32, e.g. "gonka1abc..."); this is the same key used by
	// chain-side state (CapacityState weights, PoC preservation,
	// escrow membership) and by the higher-level
	// ParticipantRequestLimiter. Using anything else (URL host:port,
	// IP, hostname, etc.) silently breaks throttle/recovery because
	// the admission controller's bucket map will not align with the
	// keys those other subsystems use.
	ParticipantKey string
	Admission      RequestAdmissionController
}

// RequestAdmissionController can reject participant-bound transport
// requests before they are sent to the remote host. The
// participantKey it receives is the gonka validator address as
// configured on ClientConfig.ParticipantKey.
type RequestAdmissionController interface {
	AllowRequest(participantKey, path string) error
	ObserveResult(participantKey, path string, statusCode int)
	// ObserveTransportFailure is called when the request never
	// received an HTTP response (dial error, connection reset, etc.).
	// Implementations apply a short cooldown; no-op is allowed.
	ObserveTransportFailure(participantKey, path string)
}

type UpstreamStatusError struct {
	Path       string
	StatusCode int
	Body       string
}

func (e *UpstreamStatusError) Error() string {
	if e == nil {
		return "upstream status error"
	}
	if e.Body == "" {
		return fmt.Sprintf("http %s: status %d", e.Path, e.StatusCode)
	}
	return fmt.Sprintf("http %s: status %d: %s", e.Path, e.StatusCode, e.Body)
}

// IsUpstreamEscrowNotFound returns true if err is an UpstreamStatusError
// whose body indicates the host could not find the escrow on chain.
func IsUpstreamEscrowNotFound(err error) bool {
	var ue *UpstreamStatusError
	if !errors.As(err, &ue) {
		return false
	}
	return ue.StatusCode == http.StatusInternalServerError &&
		strings.Contains(ue.Body, "escrow not found")
}

func DefaultClientConfig() ClientConfig {
	return ClientConfig{
		InferenceTimeout: 20 * time.Minute,
		GossipTimeout:    10 * time.Second,
		VerifyTimeout:    3 * time.Minute,
		QueryTimeout:     30 * time.Second,
	}
}

// HTTPClient implements user.HostClient over HTTP.
type HTTPClient struct {
	baseURL  string
	escrowID string
	signer   signing.Signer
	http     *http.Client
	config   ClientConfig
}

// NewHTTPClient creates an HTTP client for the subnet transport layer.
// Uses shared transport for connection pooling, per-call context timeouts.
func NewHTTPClient(baseURL, escrowID string, signer signing.Signer, cfgs ...ClientConfig) *HTTPClient {
	cfg := DefaultClientConfig()
	if len(cfgs) > 0 {
		cfg = cfgs[0]
	}
	return &HTTPClient{
		baseURL:  baseURL,
		escrowID: escrowID,
		signer:   signer,
		http: &http.Client{
			Transport: DefaultHostConnectionTracker().WrapRoundTripper(getTransport(baseURL)),
		},
		config: cfg,
	}
}

// post sends a signed POST request, marshaling req to JSON and unmarshaling into resp.
// If resp is nil, the response body is discarded.
func (c *HTTPClient) post(ctx context.Context, path string, timeout time.Duration, req, resp any) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	respBody, err := c.doPost(ctx, path, body)
	if err != nil {
		return err
	}
	if resp != nil {
		return json.Unmarshal(respBody, resp)
	}
	return nil
}

// get sends a GET request and unmarshals the response into resp.
func (c *HTTPClient) get(ctx context.Context, path string, timeout time.Duration, resp any) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	url := fmt.Sprintf("%s/v1/subnet%s", c.baseURL, path)
	body, err := c.doGet(ctx, url)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, resp)
}

// Send implements user.HostClient.
func (c *HTTPClient) Send(ctx context.Context, req host.HostRequest, stream io.Writer, receiptHandler func()) (*host.HostResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, c.config.InferenceTimeout)
	defer cancel()

	ir, err := HostRequestToJSON(req)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}

	body, err := json.Marshal(ir)
	if err != nil {
		return nil, fmt.Errorf("marshal json: %w", err)
	}

	resp, err := c.doPostRaw(ctx, "/sessions/"+c.escrowID+"/chat/completions", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	contentType := resp.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "text/event-stream") {
		result, err := c.parseSSEResponse(resp.Body, stream, receiptHandler)
		if err != nil && result != nil {
			// Partial result: return both so caller can extract receipt from broken stream.
			return result, err
		}
		return result, err
	}

	// Backward compat: JSON response.
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var respJSON InferenceResponse
	if err := json.Unmarshal(respBody, &respJSON); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}
	return HostResponseFromJSON(respJSON)
}

// parseSSEResponse reads an SSE stream and extracts subnet_receipt and subnet_meta events.
// Non-protocol data lines are forwarded to stream if configured.
func (c *HTTPClient) parseSSEResponse(r io.Reader, stream io.Writer, receiptHandler func()) (*host.HostResponse, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1<<20), 1<<20) // 1MB max line -- default 64KB breaks on long SSE responses
	var result host.HostResponse

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			writeSSELine(stream, line)
			continue
		}

		// Try to parse as subnet protocol envelope.
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal([]byte(data), &envelope); err != nil {
			// Not JSON -- forward as-is.
			writeSSELine(stream, line)
			continue
		}

		if raw, ok := envelope["subnet_receipt"]; ok {
			var receipt SubnetReceiptEvent
			if err := json.Unmarshal(raw, &receipt); err == nil {
				result.StateSig = receipt.StateSig
				result.StateHash = receipt.StateHash
				result.Nonce = receipt.Nonce
				result.Receipt = receipt.Receipt
				result.ConfirmedAt = receipt.ConfirmedAt
			}
			if receiptHandler != nil {
				receiptHandler()
			}
			continue
		}

		if raw, ok := envelope["subnet_meta"]; ok {
			var meta SubnetMetaEvent
			if err := json.Unmarshal(raw, &meta); err == nil {
				txs, txErr := SubnetTxsFromBytes(meta.Mempool)
				if txErr == nil {
					result.Mempool = txs
				}
			}
			continue
		}

		// Inference data line -- forward to callback.
		writeSSELine(stream, line)
	}
	if err := scanner.Err(); err != nil {
		return &result, fmt.Errorf("read SSE stream: %w", err)
	}
	return &result, nil
}

func writeSSELine(w io.Writer, line string) {
	if w == nil {
		return
	}
	fmt.Fprintf(w, "%s\n\n", line)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// GossipNonce sends a nonce notification to a peer.
func (c *HTTPClient) GossipNonce(ctx context.Context, nonce uint64, stateHash, stateSig []byte, slotID uint32) error {
	return c.post(ctx, "/sessions/"+c.escrowID+"/gossip/nonce", c.config.GossipTimeout,
		GossipNonceRequest{Nonce: nonce, StateHash: stateHash, StateSig: stateSig, SlotID: slotID}, nil)
}

// GossipTxs sends transactions to a peer.
func (c *HTTPClient) GossipTxs(ctx context.Context, txs []*types.SubnetTx) error {
	txBytes, err := SubnetTxsToBytes(txs)
	if err != nil {
		return fmt.Errorf("encode txs: %w", err)
	}
	return c.post(ctx, "/sessions/"+c.escrowID+"/gossip/txs", c.config.GossipTimeout,
		GossipTxsRequest{Txs: txBytes}, nil)
}

// SendVerifyTimeout asks a peer to verify a timeout (raw transport).
func (c *HTTPClient) SendVerifyTimeout(ctx context.Context, req VerifyTimeoutRequest) (*VerifyTimeoutResponse, error) {
	var resp VerifyTimeoutResponse
	if err := c.post(ctx, "/sessions/"+c.escrowID+"/verify-timeout", c.config.VerifyTimeout, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ChallengeReceipt forwards diffs + payload to the executor and returns the receipt.
func (c *HTTPClient) ChallengeReceipt(ctx context.Context, inferenceID uint64, payload *host.InferencePayload, diffs []types.Diff) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, c.config.VerifyTimeout)
	defer cancel()

	djList := make([]DiffJSON, len(diffs))
	for i, d := range diffs {
		dj, err := DiffToJSON(d)
		if err != nil {
			return nil, fmt.Errorf("encode diff %d: %w", i, err)
		}
		djList[i] = dj
	}

	req := ChallengeReceiptRequest{
		InferenceID: inferenceID,
		Payload:     PayloadToJSON(payload),
		Diffs:       djList,
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	respBody, err := c.doPost(ctx, "/sessions/"+c.escrowID+"/challenge-receipt", body)
	if err != nil {
		return nil, err
	}
	var resp ChallengeReceiptResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return resp.Receipt, nil
}

// VerifyTimeout implements user.TimeoutVerifier over HTTP.
func (c *HTTPClient) VerifyTimeout(ctx context.Context, inferenceID uint64, reason types.TimeoutReason, payload *host.InferencePayload, diffs []types.Diff) (bool, []byte, uint32, error) {
	var djList []DiffJSON
	if len(diffs) > 0 {
		djList = make([]DiffJSON, len(diffs))
		for i, d := range diffs {
			dj, err := DiffToJSON(d)
			if err != nil {
				return false, nil, 0, fmt.Errorf("encode diff %d: %w", i, err)
			}
			djList[i] = dj
		}
	}
	resp, err := c.SendVerifyTimeout(ctx, VerifyTimeoutRequest{
		InferenceID: inferenceID,
		Reason:      TimeoutReasonToString(reason),
		Payload:     PayloadToJSON(payload),
		Diffs:       djList,
	})
	if err != nil {
		return false, nil, 0, err
	}
	return resp.Accept, resp.Signature, resp.VoterSlot, nil
}

// GetDiffs fetches stored diffs from a peer.
func (c *HTTPClient) GetDiffs(ctx context.Context, from, to uint64) ([]types.Diff, error) {
	type diffRecordJSON struct {
		DiffJSON  `json:"diff"`
		StateHash []byte `json:"state_hash"`
	}
	var records []diffRecordJSON
	path := fmt.Sprintf("/sessions/%s/diffs?from=%d&to=%d", c.escrowID, from, to)
	if err := c.get(ctx, path, c.config.QueryTimeout, &records); err != nil {
		return nil, fmt.Errorf("get diffs: %w", err)
	}

	diffs := make([]types.Diff, len(records))
	for i, rec := range records {
		d, err := DiffFromJSON(rec.DiffJSON)
		if err != nil {
			return nil, fmt.Errorf("decode diff %d: %w", i, err)
		}
		diffs[i] = d
	}
	return diffs, nil
}

// GetSignatures fetches accumulated signatures for a nonce from a host.
func (c *HTTPClient) GetSignatures(ctx context.Context, nonce uint64) (map[uint32][]byte, error) {
	var resp SignaturesResponse
	path := fmt.Sprintf("/sessions/%s/signatures?nonce=%d", c.escrowID, nonce)
	if err := c.get(ctx, path, c.config.QueryTimeout, &resp); err != nil {
		return nil, fmt.Errorf("get signatures: %w", err)
	}
	return resp.Signatures, nil
}

// GetMempool fetches the host's current mempool.
func (c *HTTPClient) GetMempool(ctx context.Context) ([]*types.SubnetTx, error) {
	var result struct {
		Txs [][]byte `json:"txs"`
	}
	path := fmt.Sprintf("/sessions/%s/mempool", c.escrowID)
	if err := c.get(ctx, path, c.config.QueryTimeout, &result); err != nil {
		return nil, fmt.Errorf("get mempool: %w", err)
	}
	return SubnetTxsFromBytes(result.Txs)
}

// doPostRaw sends a signed POST request and returns the raw http.Response.
// Caller is responsible for closing resp.Body.
func (c *HTTPClient) doPostRaw(ctx context.Context, path string, body []byte) (*http.Response, error) {
	url := c.baseURL + "/v1/subnet" + path
	if err := c.allowRequest(path); err != nil {
		return nil, err
	}

	ts := time.Now().Unix()
	sig, err := SignRequest(c.signer, c.escrowID, body, ts)
	if err != nil {
		return nil, fmt.Errorf("sign request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(HeaderSignature, hex.EncodeToString(sig))
	req.Header.Set(HeaderTimestamp, strconv.FormatInt(ts, 10))

	resp, err := c.http.Do(req)
	if err != nil {
		c.observeTransportFailure(path)
		return nil, fmt.Errorf("http post %s: %w", path, err)
	}
	c.observeResult(path, resp.StatusCode)

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, &UpstreamStatusError{
			Path:       path,
			StatusCode: resp.StatusCode,
			Body:       string(respBody),
		}
	}

	return resp, nil
}

// doPost sends a signed POST request and returns the response body.
func (c *HTTPClient) doPost(ctx context.Context, path string, body []byte) ([]byte, error) {
	resp, err := c.doPostRaw(ctx, path, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// doGet sends a GET request and returns the response body.
// No auth signing -- GET endpoints skip auth on the server side for now.
func (c *HTTPClient) doGet(ctx context.Context, url string) ([]byte, error) {
	if err := c.allowRequest(url); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		c.observeTransportFailure(url)
		return nil, err
	}
	defer resp.Body.Close()
	c.observeResult(url, resp.StatusCode)

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, &UpstreamStatusError{
			Path:       url,
			StatusCode: resp.StatusCode,
			Body:       string(respBody),
		}
	}

	return io.ReadAll(resp.Body)
}

func (c *HTTPClient) allowRequest(path string) error {
	if c == nil || c.config.Admission == nil || strings.TrimSpace(c.config.ParticipantKey) == "" {
		return nil
	}
	return c.config.Admission.AllowRequest(c.config.ParticipantKey, path)
}

func (c *HTTPClient) observeResult(path string, statusCode int) {
	if c == nil || c.config.Admission == nil || strings.TrimSpace(c.config.ParticipantKey) == "" {
		return
	}
	c.config.Admission.ObserveResult(c.config.ParticipantKey, path, statusCode)
}

func (c *HTTPClient) observeTransportFailure(path string) {
	if c == nil || c.config.Admission == nil || strings.TrimSpace(c.config.ParticipantKey) == "" {
		return
	}
	c.config.Admission.ObserveTransportFailure(c.config.ParticipantKey, path)
}
