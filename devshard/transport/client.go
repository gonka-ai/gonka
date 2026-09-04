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
	"sync/atomic"
	"syscall"
	"time"

	"common/chainoracle/blocks"
	"common/httpguard"
	json "github.com/goccy/go-json"

	devshardpkg "devshard"
	"devshard/heightsync"
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
	// Dial-time SSRF guard: baseURL is a participant-controlled peer URL, so
	// every dial is vetted against the private/internal ranges unless the
	// process explicitly opts out (dev/test). See common/httpguard.
	dialer := httpguard.NewDialer()
	t := &http.Transport{
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     120 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		DialContext:         DefaultHostConnectionTracker().TrackDialContext(dialer.DialContext, fallbackAddress),
	}
	actual, _ := sharedTransports.LoadOrStore(baseURL, t)
	return actual.(*http.Transport)
}

// DefaultRoutePrefix returns the versioned URL prefix clients use when none is configured.
func DefaultRoutePrefix() string {
	return devshardpkg.DefaultRoutePrefix()
}

func transportAddress(baseURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err == nil && parsed != nil && parsed.Host != "" {
		return parsed.Hostname()
	}
	return strings.TrimSpace(baseURL)
}

// DefaultMaxSSEEventBytes is the hard default cap for a single SSE line/event
// read by the gateway transport client (1 MiB). Matches the historical
// bufio.Scanner ceiling and the gateway raceWriter classify attempt cap.
const DefaultMaxSSEEventBytes = 1 << 20

// sseReaderBufferSize is the bufio.Reader size used by parseSSEResponse.
// Oversize aborts after at most DefaultMaxSSEEventBytes + this much unread
// data in the reader buffer (not the full attacker payload).
const sseReaderBufferSize = 64 << 10

// DefaultHeightSeedTimeout is the per-attempt bound for POST /height-sync.
// QueryTimeout (30s) is far too long for a hung seed peer: one silent TCP
// connection would dominate a seed round and chew the 2-minute retry budget.
const DefaultHeightSeedTimeout = 5 * time.Second

// ClientConfig holds per-endpoint timeout settings.
type ClientConfig struct {
	InferenceTimeout time.Duration // /chat/completions, default 20m
	GossipTimeout    time.Duration // gossip/nonce, gossip/txs, default 10s
	VerifyTimeout    time.Duration // verify-timeout, default 3m
	QueryTimeout     time.Duration // diffs, mempool GETs, default 30s
	// HeightSeedTimeout bounds one POST /height-sync. Zero means
	// DefaultHeightSeedTimeout. The session seed loop owns 429/503 retry.
	HeightSeedTimeout time.Duration
	StreamCallback    func(nonce uint64, line string) // if set, receives raw SSE data lines during inference
	RoutePrefix       string                          // path prefix for all session routes; default /devshard/<version>
	// MaxSSEEventBytes caps a single SSE line (including the trailing newline).
	// Zero means DefaultMaxSSEEventBytes. Oversize lines abort with
	// ErrSSEEventTooLarge; they are never silently truncated.
	MaxSSEEventBytes int
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

	// HeightSync enables outbound Anchor sections on inference POST bodies (protobuf envelope).
	HeightSync *heightsync.AnchorScheduler
	// HeightSyncLogOracle optional Latest() for debug logs (local height vs peer).
	HeightSyncLogOracle blocks.BlockOracle
	// HeightSyncPeerTips shares observed peer tips across multiple HTTP clients
	// in the same session (courier mode). When nil and HeightSync is set, the
	// client allocates its own cache.
	HeightSyncPeerTips *HeightSyncPeerTips
	// HeightSyncRequestMutateHook runs after Decide and peer-tip carry-forward,
	// before the request is marshaled. Tests / debug only.
	HeightSyncRequestMutateHook func(sec *heightsync.HeightSyncSection, nonce uint64)
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
	ObserveResultWithBody(participantKey, path string, statusCode int, body, devshardError, routerError string)
}

// ErrSSEStreamTruncated is returned when an SSE inference stream ends (clean EOF)
// before the upstream emitted any terminator -- neither an OpenAI-style `data: [DONE]`
// nor a protocol receipt event was observed. Treat it as truncation,
// not as a successful completion: a typical cause is a peer / middlebox closing the
// HTTP body early (HTTP/1.1 Connection: close, lying Content-Length, premature
// HTTP/2 END_STREAM, idle-timeout on a proxy, etc.) which bufio readers cannot
// distinguish from a normal end-of-response.
var ErrSSEStreamTruncated = errors.New("sse stream ended without [DONE] or devshard_receipt")

// ErrSSEEventTooLarge is returned when an SSE line exceeds the configured
// MaxSSEEventBytes before a newline arrives. The oversize payload is discarded;
// callers should treat this as a transport failure and escalate to another host.
var ErrSSEEventTooLarge = errors.New("sse event exceeds size limit")

// MaxJSONResponseBytes bounds the legacy non-stream JSON inference body. It
// matches the gateway's own per-request wire-body ceiling, so a body above it
// could not be served downstream anyway.
const MaxJSONResponseBytes = 16 << 20

// ErrResponseBodyTooLarge is returned when a non-stream response body exceeds
// MaxJSONResponseBytes.
var ErrResponseBodyTooLarge = errors.New("response body exceeds size limit")

// readBoundedResponseBody reads at most max bytes, failing rather than
// truncating when the peer keeps writing past the limit.
func readBoundedResponseBody(body io.Reader, maximum int64) ([]byte, error) {
	if body == nil {
		return nil, nil
	}
	read, err := io.ReadAll(io.LimitReader(body, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(read)) > maximum {
		return nil, fmt.Errorf("%w: %d byte limit", ErrResponseBodyTooLarge, maximum)
	}
	return read, nil
}

// maxErrorBodyBytes bounds the body kept from a failed response. It reaches an error string, a
// metric label and a log line, none of which a host's error page should be free to size.
const maxErrorBodyBytes = 64 << 10

func readErrorBody(body io.Reader) string {
	if body == nil {
		return ""
	}
	read, _ := io.ReadAll(io.LimitReader(body, maxErrorBodyBytes))
	return string(read)
}

type UpstreamStatusError struct {
	Path          string
	StatusCode    int
	Body          string
	DevshardError string
	RouterError   string
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

// IsSessionNotFound returns true if err is an UpstreamStatusError from a host that does not hold the
// escrow at all, as opposed to holding it and disagreeing about a nonce.
func IsSessionNotFound(err error) bool {
	var ue *UpstreamStatusError
	if !errors.As(err, &ue) {
		return false
	}
	return ue.StatusCode == http.StatusNotFound && strings.Contains(ue.Body, "session not found")
}

// IsTransientWriteError reports a failure the peer never answered, whether or not it saw the request.
func IsTransientWriteError(err error) bool {
	if err == nil {
		return false
	}
	var status *UpstreamStatusError
	if errors.As(err, &status) {
		return false
	}
	return errors.Is(err, net.ErrClosed) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNREFUSED)
}

// IsUpstreamEscrowSettled returns true if err is an UpstreamStatusError whose
// body indicates the host refused to serve an escrow already settled on chain.
func IsUpstreamEscrowSettled(err error) bool {
	var ue *UpstreamStatusError
	if !errors.As(err, &ue) {
		return false
	}
	if ue.DevshardError == DevshardErrorEscrowSettled {
		return true
	}
	return ue.StatusCode == http.StatusConflict &&
		strings.Contains(ue.Body, "escrow already settled")
}

func DefaultClientConfig() ClientConfig {
	return ClientConfig{
		InferenceTimeout:  30 * time.Minute,
		GossipTimeout:     10 * time.Second,
		VerifyTimeout:     3 * time.Minute,
		QueryTimeout:      30 * time.Second,
		HeightSeedTimeout: DefaultHeightSeedTimeout,
		RoutePrefix:       DefaultRoutePrefix(),
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

	heightSync          *heightsync.AnchorScheduler
	heightSyncAudit     *heightsync.AuditRing
	heightSyncLogOracle blocks.BlockOracle
	heightSyncPeerTips  *HeightSyncPeerTips
	heightSyncVerifier  signing.Verifier

	// oneShot is shared by every clone of this client so the hook fires exactly
	// once across all of them, and so no clone copies the mutex guarding it.
	oneShot *oneShotHooks

	// admissionOff disables admission control without mutating config, which is
	// read unsynchronized on every request. Disabling is monotonic and per
	// client, so a flag is enough and clones start from the parent's value.
	admissionOff atomic.Bool
}

// oneShotHooks holds hook state that must not be duplicated when a client is
// cloned: a hook consumed through one clone is consumed for all of them.
type oneShotHooks struct {
	mu               sync.Mutex
	heightSyncMutate func(*heightsync.HeightSyncSection, uint64)
}

// CatalogHealthzURL is GET /{version}/healthz at this client's host base
// (the versiond-router catalog probe). Empty when the client has no
// versioned prefix or base URL. The GET is unsigned and is not an
// inference request.
func (c *HTTPClient) CatalogHealthzURL() string {
	if c == nil {
		return ""
	}
	version, err := devshardpkg.VersionForRoutePrefix(c.routePrefix)
	if err != nil || strings.TrimSpace(version) == "" {
		return ""
	}
	base := strings.TrimRight(strings.TrimSpace(c.baseURL), "/")
	if base == "" {
		return ""
	}
	return base + devshardpkg.RouterCatalogHealthzPath(version)
}

// NewHTTPClient creates an HTTP client for the devshard transport layer.
// Uses shared transport for connection pooling, per-call context timeouts.
func NewHTTPClient(baseURL, escrowID string, signer signing.Signer, cfgs ...ClientConfig) *HTTPClient {
	cfg := DefaultClientConfig()
	if len(cfgs) > 0 {
		cfg = cfgs[0]
	}
	if cfg.RoutePrefix == "" {
		cfg.RoutePrefix = DefaultRoutePrefix()
	}
	hc := &HTTPClient{
		baseURL:     baseURL,
		routePrefix: cfg.RoutePrefix,
		escrowID:    escrowID,
		signer:      signer,
		http: &http.Client{
			Transport: DefaultHostConnectionTracker().WrapRoundTripper(getTransport(baseURL)),
		},
		config:              cfg,
		heightSync:          cfg.HeightSync,
		heightSyncLogOracle: cfg.HeightSyncLogOracle,
		heightSyncVerifier:  signing.NewSecp256k1Verifier(),
		oneShot:             &oneShotHooks{},
	}
	if cfg.HeightSync != nil {
		hc.heightSyncAudit = heightsync.NewAuditRing(0)
	}
	if cfg.HeightSyncPeerTips != nil {
		hc.heightSyncPeerTips = cfg.HeightSyncPeerTips
	} else if cfg.HeightSync != nil {
		hc.heightSyncPeerTips = NewHeightSyncPeerTips()
	}
	return hc
}

// cloneSharing returns a copy that shares this client's HTTP transport,
// height-sync collaborators, and one-shot hook state. Fields are copied
// explicitly rather than with *c because HTTPClient carries a mutex (via
// oneShot) and an atomic that must not be copied by value.
func (c *HTTPClient) cloneSharing() *HTTPClient {
	cp := &HTTPClient{
		baseURL:             c.baseURL,
		routePrefix:         c.routePrefix,
		escrowID:            c.escrowID,
		signer:              c.signer,
		http:                c.http,
		config:              c.config,
		heightSync:          c.heightSync,
		heightSyncAudit:     c.heightSyncAudit,
		heightSyncLogOracle: c.heightSyncLogOracle,
		heightSyncPeerTips:  c.heightSyncPeerTips,
		heightSyncVerifier:  c.heightSyncVerifier,
		oneShot:             c.oneShot,
	}
	cp.admissionOff.Store(c.admissionOff.Load())
	return cp
}

// WithoutAdmission returns a shallow copy of the client with admission control
// disabled. Used by finalize/signature collection paths that must reach
// quarantined hosts to complete settlement. Returns any so callers across
// package boundaries can duck-type without importing the HostClient interface.
func (c *HTTPClient) WithoutAdmission() any {
	if c == nil {
		return (*HTTPClient)(nil)
	}
	cp := c.cloneSharing()
	cp.admissionOff.Store(true)
	return cp
}

// ClearAdmission disables admission control on this client in-place.
func (c *HTTPClient) ClearAdmission() {
	if c == nil {
		return
	}
	c.admissionOff.Store(true)
}

// admissionController is the active controller, or nil when admission is off.
func (c *HTTPClient) admissionController() RequestAdmissionController {
	if c == nil || c.admissionOff.Load() {
		return nil
	}
	return c.config.Admission
}

func (c *HTTPClient) signatureHeader() string {
	return HeaderSignature
}

func (c *HTTPClient) timestampHeader() string {
	return HeaderTimestamp
}

func (c *HTTPClient) cloneWithSigner(signer signing.Signer, timeout time.Duration) *HTTPClient {
	cfg := c.config
	cfg.Admission = nil
	if timeout > 0 {
		cfg.QueryTimeout = timeout
		cfg.GossipTimeout = timeout
	}
	return NewHTTPClient(c.baseURL, c.escrowID, signer, cfg)
}

// post sends a signed POST request, marshaling req to JSON and unmarshaling into resp.
// If resp is nil, the response body is discarded.
func (c *HTTPClient) post(ctx context.Context, path string, timeout time.Duration, req, resp any) error {
	return c.postJSON(ctx, path, timeout, req, resp, false)
}

// postOnce is post without the non-inference 429/503 retry. SeedHeightSync
// owns its own retry loop and must not nest inside doPostRaw's 5s budget.
func (c *HTTPClient) postOnce(ctx context.Context, path string, timeout time.Duration, req, resp any) error {
	return c.postJSON(ctx, path, timeout, req, resp, true)
}

func (c *HTTPClient) postJSON(ctx context.Context, path string, timeout time.Duration, req, resp any, once bool) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	var respBody []byte
	if once {
		respBody, err = c.doPostOnce(ctx, path, body)
	} else {
		respBody, err = c.doPost(ctx, path, body)
	}
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
	requestURL := fmt.Sprintf("%s%s%s", c.baseURL, c.routePrefix, path)
	body, err := c.doGet(ctx, requestURL)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, resp)
}

// Send implements user.HostClient.
func (c *HTTPClient) Send(ctx context.Context, req host.HostRequest, stream io.Writer, receiptHandler func(*host.HostResponse)) (*host.HostResponse, error) {
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

	body, contentType, outboundHS, err := c.wrapInferenceRequest(ctx, req, ir)
	if err != nil {
		return nil, err
	}

	resp, err := c.doPostRaw(ctx, "/sessions/"+c.escrowID+"/chat/completions", body, contentType)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if outboundHS != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		c.markHeightSyncPropagated(outboundHS)
	}

	respContentType := resp.Header.Get("Content-Type")
	if strings.HasPrefix(respContentType, "text/event-stream") {
		cr := &countingReader{r: resp.Body}
		result, err := c.parseSSEResponse(ctx, cr, stream, receiptHandler)
		if result != nil {
			result.StreamBytesRead = cr.n
		}
		if err != nil && result != nil {
			return result, err
		}
		return result, err
	}

	// Backward compat: JSON response. Bounded too, otherwise the SSE event cap is
	// bypassable by a host that answers the same request with a non-stream
	// content type and then never stops writing.
	respBody, err := readBoundedResponseBody(resp.Body, MaxJSONResponseBytes)
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
// The host writes OpenAI events (including a terminal error envelope and
// [DONE]) first, then tails the body with devshard_meta carrying the mempool
// (MsgFinishInference). This reader never stops at [DONE] or a stream-write
// error: it keeps consuming until EOF so Finish is on the response before
// Send returns. A cancelled context that wins the race with a complete meta
// tail still returns that mempool rather than dropping the artifact.
//
// Uses bufio.Reader (not bufio.Scanner) so a clean EOF that arrives *after* a
// terminator ([DONE] or devshard_receipt) can be told apart from a clean EOF
// that arrives *before* one: bufio.Scanner squashes io.EOF into a nil error, so
// the caller could not distinguish a successful completion from a peer /
// middlebox closing the body early.
//
// Line size is hard-capped by MaxSSEEventBytes (default 1 MiB). A malicious
// executor can otherwise open `data: ` and stream bytes without ever sending a
// newline; the old unbounded ReadBytes('\n') grew the returned slice for the
// whole inference deadline. Oversize aborts with ErrSSEEventTooLarge instead of
// growing unbounded or silently truncating the way Scanner would (truncating
// would mis-parse protocol events).
func (c *HTTPClient) parseSSEResponse(ctx context.Context, r io.Reader, stream io.Writer, receiptHandler func(*host.HostResponse)) (*host.HostResponse, error) {
	br := bufio.NewReaderSize(r, sseReaderBufferSize)
	maxLine := c.maxSSEEventBytes()
	var result host.HostResponse
	var writeErrLogged bool
	var unexpectedLineLogged bool
	var sawTerminator bool // true once we observe [DONE] or a devshard_receipt event
	var sawMeta bool       // true once we observe a devshard_meta tail

	for {
		raw, readErr := readBoundedSSELine(br, maxLine)
		if len(raw) > 0 {
			line := string(bytes.TrimRight(raw, "\r\n"))
			c.handleSSELine(line, stream, receiptHandler, &result, &writeErrLogged, &unexpectedLineLogged, &sawTerminator, &sawMeta)
		}
		if readErr != nil {
			if errors.Is(readErr, ErrSSEEventTooLarge) {
				// Returning aborts Send, whose deferred Close cancels the body so
				// the host cannot keep streaming into a discarded buffer.
				logging.Warn("sse_event_too_large", "subsystem", "transport", "escrow", c.escrowID, "limit_bytes", maxLine)
				return &result, readErr
			}
			if errors.Is(readErr, io.EOF) {
				// A cancelled context (client disconnect, race resolved, drain)
				// can surface as a clean EOF once the peer closes the body after
				// we abort the request. The receipt event sets sawTerminator
				// early, so without this check a cancelled stream that never
				// carried content would be reported as a successful empty
				// response and wrongly scored against the host. Report the
				// cancellation as the error it is — unless the host already
				// tailed devshard_meta: that mempool is the signed Finish
				// artifact and must not be dropped.
				if ctxErr := ctx.Err(); ctxErr != nil && !sawMeta {
					return &result, fmt.Errorf("read SSE stream: %w", ctxErr)
				}
				if !sawTerminator {
					return &result, ErrSSEStreamTruncated
				}
				return &result, nil
			}
			return &result, fmt.Errorf("read SSE stream: %w", readErr)
		}
	}
}

func (c *HTTPClient) maxSSEEventBytes() int {
	if c != nil && c.config.MaxSSEEventBytes > 0 {
		return c.config.MaxSSEEventBytes
	}
	return DefaultMaxSSEEventBytes
}

// readBoundedSSELine reads up to and including the next '\n', aborting as soon
// as the accumulated line would exceed max bytes. On oversize it returns
// ErrSSEEventTooLarge and drops the partial buffer rather than retaining it.
func readBoundedSSELine(br *bufio.Reader, maximum int) ([]byte, error) {
	if maximum <= 0 {
		maximum = DefaultMaxSSEEventBytes
	}
	var buf []byte
	for {
		// ReadSlice returns a view into the reader's own buffer, valid only until
		// the next read, so every fragment is copied out before looping.
		fragment, err := br.ReadSlice('\n')
		if len(fragment) > 0 {
			if len(buf)+len(fragment) > maximum {
				return nil, fmt.Errorf("%w: %d byte limit", ErrSSEEventTooLarge, maximum)
			}
			buf = append(buf, fragment...)
		}
		switch {
		case err == nil:
			return buf, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			if len(buf) == 0 {
				return nil, io.EOF
			}
			return buf, io.EOF
		default:
			return nil, err
		}
	}
}

// handleSSELine processes a single SSE line (terminator already stripped).
// Mutates result / flags in place; never returns an error -- read errors are
// the caller's job to detect via the underlying reader. A stream-write failure
// (race loser, client gone) is logged and ignored so the body is still drained
// through devshard_meta.
func (c *HTTPClient) handleSSELine(
	line string,
	stream io.Writer,
	receiptHandler func(*host.HostResponse),
	result *host.HostResponse,
	writeErrLogged, unexpectedLineLogged, sawTerminator, sawMeta *bool,
) {
	if !strings.HasPrefix(line, "data: ") {
		if line != "" && !strings.HasPrefix(line, ":") && !*unexpectedLineLogged {
			lineLen, lineHex := sseLineBytesForLog(line)
			switch {
			case strings.HasPrefix(line, "data:"):
				logging.Warn("sse_data_line_missing_space", "subsystem", "transport", "escrow", c.escrowID, "line_prefix", truncate(line, 120), "line_len", lineLen, "line_hex", lineHex)
			case strings.HasPrefix(line, "event:"), strings.HasPrefix(line, "id:"), strings.HasPrefix(line, "retry:"):
				// Standard SSE fields we intentionally skip.
			default:
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
			result.ObservedHeight = receipt.ObservedHeight
			result.ObservedBlockHash = receipt.ObservedBlockHash
		}
		if rawHS, ok := envelope["height_sync"]; ok && string(rawHS) != "null" {
			var hs heightsync.HeightSyncSection
			if err := json.Unmarshal(rawHS, &hs); err == nil {
				hs.Direction = "response"
				c.ingestResponseHeightSync(&hs, result.Nonce, "SSE devshard_receipt line")
				result.HasEnvelope = true
				if hs.MainnetHeight > 0 {
					result.EnvelopeHeight = uint64(hs.MainnetHeight)
				}
			}
		}
		if receiptHandler != nil {
			receiptHandler(result)
		}
		return
	}

	if raw, key, ok := c.protocolEnvelope(envelope, "meta"); ok {
		if sawMeta != nil {
			*sawMeta = true
		}
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

func (c *HTTPClient) SendVerifyErrorMiss(ctx context.Context, req VerifyErrorMissRequest) (*VerifyErrorMissResponse, error) {
	var resp VerifyErrorMissResponse
	if err := c.post(ctx, "/sessions/"+c.escrowID+"/verify-error-miss", c.config.VerifyTimeout, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ChallengeReceipt forwards diffs + payload to the executor and returns the
// receipt plus a snapshot of the executor mempool (recovery txs).
func (c *HTTPClient) ChallengeReceipt(ctx context.Context, inferenceID uint64, payload *host.InferencePayload, diffs []types.Diff) ([]byte, []*types.DevshardTx, error) {
	ctx, cancel := context.WithTimeout(ctx, c.config.VerifyTimeout)
	defer cancel()

	djList := make([]DiffJSON, len(diffs))
	for i, d := range diffs {
		dj, err := DiffToJSON(d)
		if err != nil {
			return nil, nil, fmt.Errorf("encode diff %d: %w", i, err)
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
		return nil, nil, fmt.Errorf("marshal: %w", err)
	}
	respBody, err := c.doPost(ctx, "/sessions/"+c.escrowID+"/challenge-receipt", body)
	if err != nil {
		return nil, nil, err
	}
	var resp ChallengeReceiptResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, nil, fmt.Errorf("unmarshal: %w", err)
	}
	mempool, err := DevshardTxsFromBytes(resp.Mempool)
	if err != nil {
		return nil, nil, fmt.Errorf("decode mempool: %w", err)
	}
	return resp.Receipt, mempool, nil
}

// VerifyTimeout implements user.TimeoutVerifier over HTTP.
func (c *HTTPClient) VerifyTimeout(ctx context.Context, inferenceID uint64, reason types.TimeoutReason, payload *host.InferencePayload, diffs []types.Diff, artifacts host.TimeoutArtifacts) (bool, []byte, uint32, []*types.DevshardTx, string, error) {
	var djList []DiffJSON
	if len(diffs) > 0 {
		djList = make([]DiffJSON, len(diffs))
		for i, d := range diffs {
			dj, err := DiffToJSON(d)
			if err != nil {
				return false, nil, 0, nil, "", fmt.Errorf("encode diff %d: %w", i, err)
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
		return false, nil, 0, nil, "", err
	}
	mempool, err := DevshardTxsFromBytes(resp.Mempool)
	if err != nil {
		return false, nil, 0, nil, "", fmt.Errorf("decode mempool: %w", err)
	}
	return resp.Accept, resp.Signature, resp.VoterSlot, mempool, resp.RejectCause, nil
}

func (c *HTTPClient) VerifyErrorMiss(ctx context.Context, inferenceID uint64, diffs []types.Diff, artifacts host.TimeoutArtifacts) (bool, []byte, uint32, []*types.DevshardTx, string, error) {
	var djList []DiffJSON
	if len(diffs) > 0 {
		djList = make([]DiffJSON, len(diffs))
		for i, d := range diffs {
			dj, err := DiffToJSON(d)
			if err != nil {
				return false, nil, 0, nil, "", fmt.Errorf("encode diff %d: %w", i, err)
			}
			djList[i] = dj
		}
	}
	resp, err := c.SendVerifyErrorMiss(ctx, VerifyErrorMissRequest{
		InferenceID:     inferenceID,
		Diffs:           djList,
		FinishTx:        artifacts.FinishTx,
		ResponsePayload: artifacts.ResponsePayload,
	})
	if err != nil {
		return false, nil, 0, nil, "", err
	}
	mempool, err := DevshardTxsFromBytes(resp.Mempool)
	if err != nil {
		return false, nil, 0, nil, "", fmt.Errorf("decode mempool: %w", err)
	}
	return resp.Accept, resp.Signature, resp.VoterSlot, mempool, resp.RejectCause, nil
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
// Caller is responsible for closing resp.Body. Non-inference 429/503 and
// transient dial failures retry with exponential delay up to 5s; admission
// sees only the last attempt. A router undeclared-version 503 on a
// non-inference path is not observed (SkipCatalogQuarantine).
func (c *HTTPClient) doPostRaw(ctx context.Context, path string, body []byte, contentType ...string) (*http.Response, error) {
	ct := "application/json"
	if len(contentType) > 0 && contentType[0] != "" {
		ct = contentType[0]
	}
	if isInferencePath(path) {
		return c.doPostRawOnce(ctx, path, body, ct, true)
	}
	// Admission is per logical request, not per attempt: checking it inside the
	// loop would burn one slot per retry. A rejection here is our own limiter
	// decision, so it must not reach observeNonInferenceFinal -- reporting it as
	// a host transport fault would quarantine the host we just throttled.
	if err := c.allowRequest(path); err != nil {
		return nil, err
	}
	deadline := nonInferenceRetryDeadline(ctx)
	delay := nonInferenceRetryInitial
	var lastRetryable error
	for {
		resp, err := c.postRawAttempt(ctx, path, body, ct, false)
		if err == nil {
			c.observeResult(path, http.StatusOK)
			return resp, nil
		}
		if !IsRetryableNonInference(err) {
			if lastRetryable != nil && isContextFinished(err) {
				c.observeNonInferenceFinal(path, lastRetryable)
				return nil, lastRetryable
			}
			c.observeNonInferenceFinal(path, err)
			return nil, err
		}
		lastRetryable = err
		remaining := time.Until(deadline)
		if remaining <= 0 {
			c.observeNonInferenceFinal(path, err)
			return nil, err
		}
		sleep := min(delay, remaining)
		logging.Debug("non_inference_retry", "path", path, "error", err.Error(), "sleep", sleep.String())
		if sleepErr := sleepContext(ctx, sleep); sleepErr != nil {
			c.observeNonInferenceFinal(path, lastRetryable)
			return nil, lastRetryable
		}
		if delay < nonInferenceRetryBudget {
			delay *= 2
		}
	}
}

func (c *HTTPClient) doPostRawOnce(ctx context.Context, path string, body []byte, contentType string, observe bool) (*http.Response, error) {
	if err := c.allowRequest(path); err != nil {
		return nil, err
	}
	return c.postRawAttempt(ctx, path, body, contentType, observe)
}

// postRawAttempt is one signed POST with no admission check. Callers that may
// retry admit once and then loop on this.
func (c *HTTPClient) postRawAttempt(ctx context.Context, path string, body []byte, contentType string, observe bool) (*http.Response, error) {
	requestURL := c.baseURL + c.routePrefix + path

	ts := time.Now().Unix()
	sig, err := SignRequest(c.signer, c.escrowID, body, ts)
	if err != nil {
		return nil, fmt.Errorf("sign request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set(c.signatureHeader(), hex.EncodeToString(sig))
	req.Header.Set(c.timestampHeader(), strconv.FormatInt(ts, 10))

	resp, err := c.http.Do(req)
	if err != nil {
		if observe {
			c.observeTransportFailure(path, err)
		}
		return nil, fmt.Errorf("POST %s: %w", requestURL, err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody := readErrorBody(resp.Body)
		resp.Body.Close()
		devshardError := resp.Header.Get(HeaderDevshardError)
		routerError := resp.Header.Get(HeaderDevshardRouterError)
		if observe && shouldObserveUpstreamStatus(path, resp.StatusCode, respBody, devshardError, routerError) {
			c.observeResultWithBody(path, resp.StatusCode, respBody, devshardError, routerError)
		}
		return nil, &UpstreamStatusError{
			Path:          path,
			StatusCode:    resp.StatusCode,
			Body:          respBody,
			DevshardError: devshardError,
			RouterError:   routerError,
		}
	}
	if observe {
		c.observeResult(path, resp.StatusCode)
	}
	return resp, nil
}

func (c *HTTPClient) observeNonInferenceFinal(path string, err error) {
	var status *UpstreamStatusError
	if errors.As(err, &status) {
		if shouldObserveUpstreamStatus(path, status.StatusCode, status.Body, status.DevshardError, status.RouterError) {
			c.observeResultWithBody(path, status.StatusCode, status.Body, status.DevshardError, status.RouterError)
		}
		return
	}
	c.observeTransportFailure(path, err)
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

func (c *HTTPClient) doPostOnce(ctx context.Context, path string, body []byte) ([]byte, error) {
	resp, err := c.doPostRawOnce(ctx, path, body, "application/json", true)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// doGet sends a GET request and returns the response body.
// No auth signing -- GET endpoints skip auth on the server side for now.
func (c *HTTPClient) doGet(ctx context.Context, requestURL string) ([]byte, error) {
	if isInferencePath(requestURL) {
		return c.doGetOnce(ctx, requestURL, true)
	}
	// See doPostRaw: admit once per logical request, and never attribute a local
	// limiter rejection to the host.
	if err := c.allowRequest(requestURL); err != nil {
		return nil, err
	}
	deadline := nonInferenceRetryDeadline(ctx)
	delay := nonInferenceRetryInitial
	var lastRetryable error
	for {
		body, err := c.getAttempt(ctx, requestURL, false)
		if err == nil {
			c.observeResult(requestURL, http.StatusOK)
			return body, nil
		}
		if !IsRetryableNonInference(err) {
			if lastRetryable != nil && isContextFinished(err) {
				c.observeNonInferenceFinal(requestURL, lastRetryable)
				return nil, lastRetryable
			}
			c.observeNonInferenceFinal(requestURL, err)
			return nil, err
		}
		lastRetryable = err
		remaining := time.Until(deadline)
		if remaining <= 0 {
			c.observeNonInferenceFinal(requestURL, err)
			return nil, err
		}
		sleep := min(delay, remaining)
		logging.Debug("non_inference_retry", "path", requestURL, "error", err.Error(), "sleep", sleep.String())
		if sleepErr := sleepContext(ctx, sleep); sleepErr != nil {
			c.observeNonInferenceFinal(requestURL, lastRetryable)
			return nil, lastRetryable
		}
		if delay < nonInferenceRetryBudget {
			delay *= 2
		}
	}
}

func (c *HTTPClient) doGetOnce(ctx context.Context, requestURL string, observe bool) ([]byte, error) {
	if err := c.allowRequest(requestURL); err != nil {
		return nil, err
	}
	return c.getAttempt(ctx, requestURL, observe)
}

// getAttempt is one GET with no admission check. See postRawAttempt.
func (c *HTTPClient) getAttempt(ctx context.Context, requestURL string, observe bool) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		if observe {
			c.observeTransportFailure(requestURL, err)
		}
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody := readErrorBody(resp.Body)
		devshardError := resp.Header.Get(HeaderDevshardError)
		routerError := resp.Header.Get(HeaderDevshardRouterError)
		if observe && shouldObserveUpstreamStatus(requestURL, resp.StatusCode, respBody, devshardError, routerError) {
			c.observeResultWithBody(requestURL, resp.StatusCode, respBody, devshardError, routerError)
		}
		return nil, &UpstreamStatusError{
			Path:          requestURL,
			StatusCode:    resp.StatusCode,
			Body:          respBody,
			DevshardError: devshardError,
			RouterError:   routerError,
		}
	}
	if observe {
		c.observeResult(requestURL, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func (c *HTTPClient) allowRequest(path string) error {
	admission := c.admissionController()
	if admission == nil || strings.TrimSpace(c.config.ParticipantKey) == "" {
		return nil
	}
	return admission.AllowRequest(c.config.ParticipantKey, path)
}

func (c *HTTPClient) observeResult(path string, statusCode int) {
	admission := c.admissionController()
	if admission == nil || strings.TrimSpace(c.config.ParticipantKey) == "" {
		return
	}
	admission.ObserveResult(c.config.ParticipantKey, path, statusCode)
}

func (c *HTTPClient) observeResultWithBody(path string, statusCode int, body, devshardError, routerError string) {
	admission := c.admissionController()
	if admission == nil || strings.TrimSpace(c.config.ParticipantKey) == "" {
		return
	}
	if observer, ok := admission.(requestAdmissionBodyObserver); ok {
		observer.ObserveResultWithBody(c.config.ParticipantKey, path, statusCode, body, devshardError, routerError)
		return
	}
	admission.ObserveResult(c.config.ParticipantKey, path, statusCode)
}

func (c *HTTPClient) observeTransportFailure(path string, err error) {
	admission := c.admissionController()
	if admission == nil || strings.TrimSpace(c.config.ParticipantKey) == "" {
		return
	}
	// A cancelled request context is our own signal (client disconnect, race
	// resolved, drain), not a fault of the host. Attributing it as a transport
	// failure would quarantine a healthy host, so skip it.
	if errors.Is(err, context.Canceled) {
		return
	}
	admission.ObserveTransportFailure(c.config.ParticipantKey, path, err)
}
