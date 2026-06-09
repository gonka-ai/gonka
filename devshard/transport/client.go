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

	devshardpkg "devshard"
	"devshard/host"
	"devshard/logging"
	"devshard/signing"
	"devshard/types"
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

// DefaultRoutePrefix is the legacy URL prefix dapi mounts the in-process
// HostManager under. Versioned binaries use devshard.VersionedRoutePrefix(...).
const DefaultRoutePrefix = devshardpkg.LegacyRoutePrefix

func transportAddress(baseURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err == nil && parsed != nil && parsed.Host != "" {
		return parsed.Hostname()
	}
	return strings.TrimSpace(baseURL)
}

// ClientConfig holds per-endpoint timeout settings.
type ClientConfig struct {
	InferenceTimeout time.Duration                   // /chat/completions, default 20m
	GossipTimeout    time.Duration                   // gossip/nonce, gossip/txs, default 10s
	VerifyTimeout    time.Duration                   // verify-timeout, default 3m
	QueryTimeout     time.Duration                   // diffs, mempool GETs, default 30s
	StreamCallback   func(nonce uint64, line string) // if set, receives raw SSE data lines during inference
	RoutePrefix      string                          // path prefix for all session routes; default /v1/devshard
	ProtocolVersion  types.ProtocolVersion           // runtime protocol version configured for the escrow
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
	// Implementations decide whether to quarantine based on path kind.
	ObserveTransportFailure(participantKey, path string, err error)
}

type requestAdmissionBodyObserver interface {
	ObserveResultWithBody(participantKey, path string, statusCode int, body string)
}

// ErrSSEStreamTruncated is returned when an SSE inference stream ends (clean EOF)
// before the upstream emitted any terminator -- neither an OpenAI-style `data: [DONE]`
// nor a protocol receipt event was observed. Treat it as truncation,
// not as a successful completion: a typical cause is a peer / middlebox closing the
// HTTP body early (HTTP/1.1 Connection: close, lying Content-Length, premature
// HTTP/2 END_STREAM, idle-timeout on a proxy, etc.) which bufio readers cannot
// distinguish from a normal end-of-response.
var ErrSSEStreamTruncated = errors.New("sse stream ended without [DONE] or devshard_receipt")

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
		InferenceTimeout: 30 * time.Minute,
		GossipTimeout:    10 * time.Second,
		VerifyTimeout:    3 * time.Minute,
		QueryTimeout:     30 * time.Second,
		RoutePrefix:      DefaultRoutePrefix,
	}
}

// HTTPClient implements user.HostClient over HTTP.
type HTTPClient struct {
	baseURL     string
	routePrefix string
	escrowID    string
	signer      signing.Signer
	http        *http.Client
	config      ClientConfig
}

// NewHTTPClient creates an HTTP client for the devshard transport layer.
// Uses shared transport for connection pooling, per-call context timeouts.
func NewHTTPClient(baseURL, escrowID string, signer signing.Signer, cfgs ...ClientConfig) *HTTPClient {
	cfg := DefaultClientConfig()
	if len(cfgs) > 0 {
		cfg = cfgs[0]
	}
	cfg.RoutePrefix = devshardpkg.NormalizeRoutePrefix(cfg.RoutePrefix)
	return &HTTPClient{
		baseURL:     baseURL,
		routePrefix: cfg.RoutePrefix,
		escrowID:    escrowID,
		signer:      signer,
		http: &http.Client{
			Transport: DefaultHostConnectionTracker().WrapRoundTripper(getTransport(baseURL)),
		},
		config: cfg,
	}
}

// WithoutAdmission returns a shallow copy of the client with admission control
// disabled. Used by finalize/signature collection paths that must reach
// quarantined hosts to complete settlement. Returns any so callers across
// package boundaries can duck-type without importing the HostClient interface.
func (c *HTTPClient) WithoutAdmission() any {
	cp := *c
	cp.config.Admission = nil
	return &cp
}

// ClearAdmission disables admission control on this client in-place.
func (c *HTTPClient) ClearAdmission() {
	c.config.Admission = nil
}

func (c *HTTPClient) signatureHeader() string {
	return HeaderSignature
}

func (c *HTTPClient) timestampHeader() string {
	return HeaderTimestamp
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
	url := fmt.Sprintf("%s%s%s", c.baseURL, c.routePrefix, path)
	body, err := c.doGet(ctx, url)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, resp)
}

// Send implements user.HostClient.
func (c *HTTPClient) Send(ctx context.Context, req host.HostRequest, stream io.Writer, receiptHandler func()) (*host.HostResponse, error) {
	timeout := c.config.InferenceTimeout
	if req.Payload == nil {
		// Finalize/catch-up sends only exchange protocol state, so a dead host
		// should not hold the caller for the full model inference deadline.
		timeout = c.config.QueryTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
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
		cr := &countingReader{r: resp.Body}
		result, err := c.parseSSEResponse(cr, stream, receiptHandler)
		if result != nil {
			result.StreamBytesRead = cr.n
		}
		if err != nil && result != nil {
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

// parseSSEResponse reads an SSE stream and extracts protocol receipt/meta events.
// Non-protocol data lines are forwarded to stream if configured.
//
// Uses bufio.Reader (not bufio.Scanner) for two reasons:
//  1. bufio.Scanner imposes a hard token-size cap (we previously raised it to 1MB);
//     a single oversized SSE line -- e.g. a large devshard_meta with a base64 mempool,
//     or a non-streaming server inlining a giant JSON on one line -- would trip
//     bufio.ErrTooLong and silently truncate. ReadBytes is bounded only by memory.
//  2. We need to distinguish a clean EOF that arrives *after* a terminator
//     ([DONE] or devshard_receipt) from a clean EOF that arrives *before* one.
//     bufio.Scanner squashes io.EOF into a nil error, so the caller cannot tell
//     a successful completion from a peer / middlebox closing the body early.
func (c *HTTPClient) parseSSEResponse(r io.Reader, stream io.Writer, receiptHandler func()) (*host.HostResponse, error) {
	br := bufio.NewReaderSize(r, 64<<10)
	var result host.HostResponse
	var writeErrLogged bool
	var unexpectedLineLogged bool
	var sawTerminator bool // true once we observe [DONE] or a devshard_receipt event

	for {
		raw, readErr := br.ReadBytes('\n')
		if len(raw) > 0 {
			line := string(bytes.TrimRight(raw, "\r\n"))
			c.handleSSELine(line, stream, receiptHandler, &result, &writeErrLogged, &unexpectedLineLogged, &sawTerminator)
		}
		if readErr != nil {
			if readErr == io.EOF {
				if !sawTerminator {
					return &result, ErrSSEStreamTruncated
				}
				return &result, nil
			}
			return &result, fmt.Errorf("read SSE stream: %w", readErr)
		}
	}
}

// handleSSELine processes a single SSE line (terminator already stripped).
// Mutates result / flags in place; never returns an error -- read errors are
// the caller's job to detect via the underlying reader.
func (c *HTTPClient) handleSSELine(
	line string,
	stream io.Writer,
	receiptHandler func(),
	result *host.HostResponse,
	writeErrLogged, unexpectedLineLogged, sawTerminator *bool,
) {
	if !strings.HasPrefix(line, "data: ") {
		if line != "" && !strings.HasPrefix(line, ":") && !*unexpectedLineLogged {
			lineLen, lineHex := sseLineBytesForLog(line)
			if strings.HasPrefix(line, "data:") {
				logging.Warn("sse_data_line_missing_space", "subsystem", "transport", "escrow", c.escrowID, "line_prefix", truncate(line, 120), "line_len", lineLen, "line_hex", lineHex)
			} else if strings.HasPrefix(line, "event:") || strings.HasPrefix(line, "id:") || strings.HasPrefix(line, "retry:") {
				// Standard SSE fields we intentionally skip.
			} else {
				*unexpectedLineLogged = true
				logging.Warn("sse_unexpected_line", "subsystem", "transport", "escrow", c.escrowID, "line_prefix", truncate(line, 120), "line_len", lineLen, "line_hex", lineHex)
			}
		}
		return
	}
	data := strings.TrimPrefix(line, "data: ")
	if data == "[DONE]" {
		*sawTerminator = true
		if err := writeSSELine(stream, line); err != nil && !*writeErrLogged {
			*writeErrLogged = true
			logging.Warn("sse_write_failed", "subsystem", "transport", "escrow", c.escrowID, "event", "[DONE]", "error", err)
		}
		return
	}

	// Try to parse as devshard protocol envelope.
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(data), &envelope); err != nil {
		// Not JSON -- forward as-is.
		if werr := writeSSELine(stream, line); werr != nil && !*writeErrLogged {
			*writeErrLogged = true
			logging.Warn("sse_write_failed", "subsystem", "transport", "escrow", c.escrowID, "event", "data", "error", werr)
		}
		return
	}

	if raw, key, ok := c.protocolEnvelope(envelope, "receipt"); ok {
		*sawTerminator = true
		var receipt DevshardReceiptEvent
		if err := json.Unmarshal(raw, &receipt); err != nil {
			logging.Warn("sse_receipt_unmarshal_failed", "subsystem", "transport", "escrow", c.escrowID, "event_key", key, "error", err)
		} else {
			hostLabel := c.config.ParticipantKey
			if len(hostLabel) > 8 {
				hostLabel = hostLabel[len(hostLabel)-8:]
			}
			logging.Info("sse_devshard_receipt",
				"subsystem", "transport",
				"escrow", c.escrowID,
				"host", hostLabel,
				"event_key", key,
				"nonce", receipt.Nonce,
				"has_state_sig", len(receipt.StateSig) > 0,
				"state_sig_bytes", len(receipt.StateSig),
				"has_state_hash", len(receipt.StateHash) > 0,
				"state_hash_bytes", len(receipt.StateHash),
				"has_executor_receipt", len(receipt.Receipt) > 0,
				"executor_receipt_bytes", len(receipt.Receipt),
				"confirmed_at", receipt.ConfirmedAt,
			)
			result.StateSig = receipt.StateSig
			result.StateHash = receipt.StateHash
			result.Nonce = receipt.Nonce
			result.Receipt = receipt.Receipt
			result.ConfirmedAt = receipt.ConfirmedAt
		}
		if receiptHandler != nil {
			receiptHandler()
		}
		return
	}

	if raw, key, ok := c.protocolEnvelope(envelope, "meta"); ok {
		var meta DevshardMetaEvent
		if err := json.Unmarshal(raw, &meta); err != nil {
			logging.Warn("sse_meta_unmarshal_failed", "subsystem", "transport", "escrow", c.escrowID, "event_key", key, "error", err)
		} else {
			txs, txErr := DevshardTxsFromBytes(meta.Mempool)
			if txErr != nil {
				logging.Warn("sse_meta_tx_decode_failed", "subsystem", "transport", "escrow", c.escrowID, "mempool_len", len(meta.Mempool), "error", txErr)
			} else {
				result.Mempool = txs
			}
		}
		return
	}

	// Inference data line -- forward to callback.
	if err := writeSSELine(stream, line); err != nil && !*writeErrLogged {
		*writeErrLogged = true
		logging.Warn("sse_write_failed", "subsystem", "transport", "escrow", c.escrowID, "event", "data", "error", err)
	}
}

func (c *HTTPClient) protocolEnvelope(envelope map[string]json.RawMessage, suffix string) (json.RawMessage, string, bool) {
	keys := []string{"devshard_" + suffix}
	for _, key := range keys {
		if raw, ok := envelope[key]; ok {
			return raw, key, true
		}
	}
	return nil, "", false
}

func writeSSELine(w io.Writer, line string) error {
	if w == nil {
		return nil
	}
	if _, err := fmt.Fprintf(w, "%s\n\n", line); err != nil {
		return err
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}

type countingReader struct {
	r io.Reader
	n int64
}

func (cr *countingReader) Read(p []byte) (int, error) {
	n, err := cr.r.Read(p)
	cr.n += int64(n)
	return n, err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func sseLineBytesForLog(line string) (int, string) {
	b := []byte(line)
	return len(b), hex.EncodeToString(b)
}

// GossipNonce sends a nonce notification to a peer.
func (c *HTTPClient) GossipNonce(ctx context.Context, nonce uint64, stateHash, stateSig []byte, slotID uint32) error {
	return c.post(ctx, "/sessions/"+c.escrowID+"/gossip/nonce", c.config.GossipTimeout,
		GossipNonceRequest{Nonce: nonce, StateHash: stateHash, StateSig: stateSig, SlotID: slotID}, nil)
}

// GossipTxs sends transactions to a peer.
func (c *HTTPClient) GossipTxs(ctx context.Context, txs []*types.DevshardTx) error {
	txBytes, err := DevshardTxsToBytes(txs)
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
func (c *HTTPClient) GetMempool(ctx context.Context) ([]*types.DevshardTx, error) {
	var result struct {
		Txs [][]byte `json:"txs"`
	}
	path := fmt.Sprintf("/sessions/%s/mempool", c.escrowID)
	if err := c.get(ctx, path, c.config.QueryTimeout, &result); err != nil {
		return nil, fmt.Errorf("get mempool: %w", err)
	}
	return DevshardTxsFromBytes(result.Txs)
}

// doPostRaw sends a signed POST request and returns the raw http.Response.
// Caller is responsible for closing resp.Body.
func (c *HTTPClient) doPostRaw(ctx context.Context, path string, body []byte) (*http.Response, error) {
	url := c.baseURL + c.routePrefix + path
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
	req.Header.Set(c.signatureHeader(), hex.EncodeToString(sig))
	req.Header.Set(c.timestampHeader(), strconv.FormatInt(ts, 10))

	resp, err := c.http.Do(req)
	if err != nil {
		c.observeTransportFailure(path, err)
		return nil, fmt.Errorf("POST %s: %w", url, err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		c.observeResultWithBody(path, resp.StatusCode, string(respBody))
		return nil, &UpstreamStatusError{
			Path:       path,
			StatusCode: resp.StatusCode,
			Body:       string(respBody),
		}
	}
	c.observeResult(path, resp.StatusCode)

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
		c.observeTransportFailure(url, err)
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		c.observeResultWithBody(url, resp.StatusCode, string(respBody))
		return nil, &UpstreamStatusError{
			Path:       url,
			StatusCode: resp.StatusCode,
			Body:       string(respBody),
		}
	}
	c.observeResult(url, resp.StatusCode)

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

func (c *HTTPClient) observeResultWithBody(path string, statusCode int, body string) {
	if c == nil || c.config.Admission == nil || strings.TrimSpace(c.config.ParticipantKey) == "" {
		return
	}
	if observer, ok := c.config.Admission.(requestAdmissionBodyObserver); ok {
		observer.ObserveResultWithBody(c.config.ParticipantKey, path, statusCode, body)
		return
	}
	c.config.Admission.ObserveResult(c.config.ParticipantKey, path, statusCode)
}

func (c *HTTPClient) observeTransportFailure(path string, err error) {
	if c == nil || c.config.Admission == nil || strings.TrimSpace(c.config.ParticipantKey) == "" {
		return
	}
	c.config.Admission.ObserveTransportFailure(c.config.ParticipantKey, path, err)
}
