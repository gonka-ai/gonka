package transport

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	json "github.com/goccy/go-json"

	devshardpkg "devshard"
	"devshard/blockoracle"
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
	t := &http.Transport{
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     120 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	actual, _ := sharedTransports.LoadOrStore(baseURL, t)
	return actual.(*http.Transport)
}

// DefaultRoutePrefix is the legacy URL prefix dapi mounts the in-process
// HostManager under. Versioned binaries use devshard.VersionedRoutePrefix(...).
const DefaultRoutePrefix = devshardpkg.LegacyRoutePrefix

// ClientConfig holds per-endpoint timeout settings.
type ClientConfig struct {
	InferenceTimeout time.Duration                   // /chat/completions, default 20m
	GossipTimeout    time.Duration                   // gossip/nonce, gossip/txs, default 10s
	VerifyTimeout    time.Duration                   // verify-timeout, default 3m
	QueryTimeout     time.Duration                   // diffs, mempool GETs, default 30s
	StreamCallback   func(nonce uint64, line string) // if set, receives raw SSE data lines during inference
	RoutePrefix      string                          // path prefix for all session routes; default /v1/devshard

	// HeightSync enables outbound Anchor sections on inference POST bodies (protobuf envelope).
	// Nil = legacy JSON body only (backwards compatible).
	HeightSync *heightsync.AnchorScheduler
	// HeightSyncLogOracle optional Latest() for debug logs (local height vs peer).
	HeightSyncLogOracle blockoracle.BlockOracle
	// HeightSyncPeerTips shares observed peer tips across multiple HTTP clients
	// in the same user session so a higher tip learned from one host can be
	// carried forward to subsequent requests sent to other hosts.
	HeightSyncPeerTips *HeightSyncPeerTips
	// HeightSyncRequestMutateHook runs after Decide and peer-tip carry-forward,
	// immediately before marshaling the protobuf envelope. Tests use it to inject
	// a bogus mainnet_block_hash while keeping scheduler height; production
	// clients must leave this nil.
	HeightSyncRequestMutateHook func(sec *heightsync.HeightSyncSection, nonce uint64)
	// HeightSyncConfirmation is a shared confirmation index across session HTTP
	// clients (courier user aggregates host response attestations).
	HeightSyncConfirmation *heightsync.ConfirmationIndex
}

func DefaultClientConfig() ClientConfig {
	return ClientConfig{
		InferenceTimeout: 20 * time.Minute,
		GossipTimeout:    10 * time.Second,
		VerifyTimeout:    3 * time.Minute,
		QueryTimeout:     30 * time.Second,
		RoutePrefix:      DefaultRoutePrefix,
	}
}

// HTTPClient implements user.HostClient over HTTP.
type HTTPClient struct {
	baseURL             string
	routePrefix         string
	escrowID            string
	signer              signing.Signer
	http                *http.Client
	config              ClientConfig
	heightSync          *heightsync.AnchorScheduler
	heightSyncAudit     *heightsync.AuditRing
	heightSyncLogOracle blockoracle.BlockOracle
	heightSyncPeerTips    *HeightSyncPeerTips
	heightSyncVerifier    signing.Verifier

	oneShotMu                sync.Mutex
	oneShotHeightSyncMutate func(*heightsync.HeightSyncSection, uint64)
}

// NewHTTPClient creates an HTTP client for the devshard transport layer.
// Uses shared transport for connection pooling, per-call context timeouts.
func NewHTTPClient(baseURL, escrowID string, signer signing.Signer, cfgs ...ClientConfig) *HTTPClient {
	cfg := DefaultClientConfig()
	if len(cfgs) > 0 {
		cfg = cfgs[0]
	}
	cfg.RoutePrefix = devshardpkg.NormalizeRoutePrefix(cfg.RoutePrefix)
	hc := &HTTPClient{
		baseURL:             baseURL,
		routePrefix:         cfg.RoutePrefix,
		escrowID:            escrowID,
		signer:              signer,
		http:                &http.Client{Transport: getTransport(baseURL)},
		config:              cfg,
		heightSync:          cfg.HeightSync,
		heightSyncLogOracle: cfg.HeightSyncLogOracle,
	}
	if cfg.HeightSync != nil {
		hc.heightSyncAudit = heightsync.NewAuditRing(0)
		if cfg.HeightSyncConfirmation != nil {
			hc.heightSyncAudit.AttachConfirmation(cfg.HeightSyncConfirmation)
		}
	}
	if cfg.HeightSyncPeerTips != nil {
		hc.heightSyncPeerTips = cfg.HeightSyncPeerTips
	} else if cfg.HeightSync != nil {
		hc.heightSyncPeerTips = NewHeightSyncPeerTips()
	}
	if hc.heightSyncPeerTips != nil {
		hc.heightSyncPeerTips.RequireVerifiedBlob = true
		hc.heightSyncVerifier = signing.NewSecp256k1Verifier()
	}
	return hc
}

// SetOneShotHeightSyncRequestMutateHook registers a hook that runs once on the next
// outbound inference request that carries a non-nil height-sync section (after the
// optional config HeightSyncRequestMutateHook). Used by testenv devshardctl debug only.
func (c *HTTPClient) SetOneShotHeightSyncRequestMutateHook(fn func(*heightsync.HeightSyncSection, uint64)) {
	if c == nil {
		return
	}
	c.oneShotMu.Lock()
	defer c.oneShotMu.Unlock()
	c.oneShotHeightSyncMutate = fn
}

func (c *HTTPClient) updateObservedPeerTip(hs *heightsync.HeightSyncSection) {
	if c.heightSyncPeerTips == nil {
		return
	}
	c.heightSyncPeerTips.RecordOrigin(hs)
}

// HeightSyncEvidenceFor returns the verified signed origin blob for dispute exculpation (Step 8).
func (c *HTTPClient) HeightSyncEvidenceFor(originator string, height int64) (blob, sig []byte, ok bool) {
	if c == nil || c.heightSyncPeerTips == nil {
		return nil, nil, false
	}
	return c.heightSyncPeerTips.OriginSignedBlobFor(originator, height)
}

func (c *HTTPClient) ingestResponseHeightSync(hs *heightsync.HeightSyncSection, nonce uint64, source string) {
	if hs == nil || !heightsync.IsAnchorSection(hs) {
		return
	}
	var blobOK bool
	if c.heightSyncPeerTips != nil && c.heightSyncVerifier != nil {
		if err := heightsync.VerifyOrigin(c.heightSyncVerifier, hs, hs.SenderSignature); err != nil {
			heightsync.IncOriginSigInvalid()
			logging.Warn("heightsync: origin_sig_invalid",
				heightsync.LogFieldSubsystem, "heightsync",
				heightsync.LogFieldDirection, "response",
				heightsync.LogFieldNonce, nonce,
				"error", err.Error())
			return
		}
		blob, err := heightsync.CanonicalOriginBytes(hs)
		if err != nil {
			heightsync.IncOriginSigInvalid()
			return
		}
		c.heightSyncPeerTips.RecordOriginWithBlob(hs, blob, hs.SenderSignature)
		blobOK = true
	} else if c.heightSyncPeerTips != nil {
		c.updateObservedPeerTip(hs)
	}
	c.logPeerHeightSyncFromSSE(hs, nonce)
	c.recordHostInboundAnchorIfAnchor(hs, source, blobOK)
}

func (c *HTTPClient) carryForwardPeerTip(sec *heightsync.HeightSyncSection) {
	c.heightSyncPeerTips.Carry(sec)
}

func (c *HTTPClient) markHeightSyncPropagated(sec *heightsync.HeightSyncSection) {
	if c.heightSyncPeerTips == nil || sec == nil || sec.MainnetHeight <= 0 {
		return
	}
	c.heightSyncPeerTips.MarkPropagated(c.baseURL, uint64(sec.MainnetHeight))
}

// HeightSyncAuditRing returns the audit ring when height sync is enabled, or nil.
func (c *HTTPClient) HeightSyncAuditRing() *heightsync.AuditRing {
	return c.heightSyncAudit
}

// ConfirmationView returns the shared confirmation index when height sync is enabled.
func (c *HTTPClient) ConfirmationView() heightsync.ConfirmationView {
	if c == nil || c.heightSyncAudit == nil {
		return nil
	}
	return c.heightSyncAudit.ConfirmationView()
}

// HeightSyncPeerTips returns the shared peer-tip cache when height sync is enabled, or nil.
func (c *HTTPClient) HeightSyncPeerTips() *HeightSyncPeerTips {
	return c.heightSyncPeerTips
}

// ObservedHeightNow returns the highest fresh mainnet height in the courier peer-tip
// cache (plan §3.7). The bool is false when height sync is off or no fresh tip exists.
func (c *HTTPClient) ObservedHeightNow() (uint64, bool) {
	if c == nil || c.heightSyncPeerTips == nil {
		return 0, false
	}
	tip := c.heightSyncPeerTips.MaxFresh(time.Now(), c.heightSyncPeerTips.freshness())
	if tip == nil || tip.MainnetHeight <= 0 {
		return 0, false
	}
	return uint64(tip.MainnetHeight), true
}

// SeedHeightSync calls the optional host cold-start RPC and records the returned
// Anchor in the peer-tip cache. ok is false when height sync is disabled, the RPC
// is unavailable (404), or the host omitted the section (oracle miss).
func (c *HTTPClient) SeedHeightSync(ctx context.Context) (ok bool, err error) {
	if c == nil || c.heightSyncPeerTips == nil {
		return false, nil
	}
	var out heightSyncSeedResponse
	err = c.post(ctx, "/sessions/"+c.escrowID+"/height-sync", c.config.QueryTimeout, struct{}{}, &out)
	if err != nil {
		return false, err
	}
	if out.HeightSync == nil || !heightsync.IsAnchorSection(out.HeightSync) {
		return false, nil
	}
	out.HeightSync.Direction = "response"
	c.ingestResponseHeightSync(out.HeightSync, 0, "POST /height-sync")
	_, _, ok = c.heightSyncPeerTips.OriginSignedBlobFor(
		strings.TrimSpace(out.HeightSync.OriginatorSenderID),
		out.HeightSync.MainnetHeight,
	)
	return ok, nil
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
func (c *HTTPClient) Send(ctx context.Context, req host.HostRequest) (*host.HostResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, c.config.InferenceTimeout)
	defer cancel()

	ir, err := HostRequestToJSON(req)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}

	contentType := "application/json"
	var body []byte
	var outboundHS *heightsync.HeightSyncSection
	if c.heightSync != nil {
		h := heightsync.DecideHints{
			Nonce:       req.Nonce,
			ForceAnchor: req.ForceHeightSyncAnchor,
			Escrow:      req.HeightSyncEscrow,
			Recipient:   c.baseURL,
		}
		if c.heightSyncPeerTips != nil {
			h.Propagator = c.heightSyncPeerTips
		} else {
			// Host / user-with-follower: local oracle emission is originator-signed.
			h.OriginatorSenderID = c.signer.Address()
		}
		if h.Escrow != nil {
			h.ForceAnchor = false
		}
		sec, dErr, oracleMiss := c.heightSync.Decide(ctx, h)
		if oracleMiss {
			heightsync.IncOracleFailure(c.signer.Address())
		}
		if dErr != nil {
			logging.Debug("heightsync: outbound anchor error",
				heightsync.LogFieldSubsystem, "heightsync",
				heightsync.LogFieldDirection, "request",
				heightsync.LogFieldNonce, req.Nonce,
				"error", dErr.Error())
		}
		if sec != nil {
			outboundHS = sec
			sec.Direction = "request"
			c.carryForwardPeerTip(sec)
			if hook := c.config.HeightSyncRequestMutateHook; hook != nil {
				hook(sec, req.Nonce)
			}
			c.oneShotMu.Lock()
			fn := c.oneShotHeightSyncMutate
			if fn != nil {
				c.oneShotHeightSyncMutate = nil
			}
			c.oneShotMu.Unlock()
			if fn != nil {
				fn(sec, req.Nonce)
			}
			body, err = MarshalWrappedInferenceRequest(CurrentInferenceEnvelopeSchemaVersion, sec, ir)
			if err != nil {
				return nil, fmt.Errorf("marshal inference envelope: %w", err)
			}
			contentType = "application/x-protobuf"
			c.logEmitUserHeightSync(sec, req.Nonce)
			c.recordUserOutboundAnchorIfAnchor(sec, "POST /chat/completions")
		} else {
			body, err = json.Marshal(ir)
			if err != nil {
				return nil, fmt.Errorf("marshal json: %w", err)
			}
			c.logEmitUserHeightSync(nil, req.Nonce)
		}
	} else {
		body, err = json.Marshal(ir)
		if err != nil {
			return nil, fmt.Errorf("marshal json: %w", err)
		}
	}

	var httpResp *http.Response
	httpResp, err = c.doPostRaw(ctx, "/sessions/"+c.escrowID+"/chat/completions", body, contentType)
	if err != nil {
		return nil, err
	}
	defer httpResp.Body.Close()
	if outboundHS != nil && httpResp.StatusCode >= 200 && httpResp.StatusCode < 300 {
		c.markHeightSyncPropagated(outboundHS)
	}

	contentTypeHdr := httpResp.Header.Get("Content-Type")
	if strings.HasPrefix(contentTypeHdr, "text/event-stream") {
		result, err := c.parseSSEResponse(httpResp.Body, req.Nonce)
		if err != nil && result != nil {
			// Partial result: return both so caller can extract receipt from broken stream.
			return result, err
		}
		return result, err
	}

	// Backward compat: JSON response.
	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, err
	}
	var respJSON InferenceResponse
	if err := json.Unmarshal(respBody, &respJSON); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}
	return HostResponseFromJSON(respJSON)
}

// parseSSEResponse reads an SSE stream and extracts devshard_receipt and devshard_meta events.
// Non-protocol data lines are forwarded to StreamCallback if configured.
func (c *HTTPClient) parseSSEResponse(r io.Reader, nonce uint64) (*host.HostResponse, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1<<20), 1<<20) // 1MB max line -- default 64KB breaks on long SSE responses
	var result host.HostResponse
	sawDone := false

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			sawDone = true
			if c.config.StreamCallback != nil {
				c.config.StreamCallback(nonce, line)
			}
			continue
		}

		// Try to parse as devshard protocol envelope.
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal([]byte(data), &envelope); err != nil {
			// Not JSON -- forward as-is.
			if c.config.StreamCallback != nil {
				c.config.StreamCallback(nonce, line)
			}
			continue
		}

		if raw, ok := envelope["height_sync"]; ok && string(raw) != "null" {
			var hs heightsync.HeightSyncSection
			if err := json.Unmarshal(raw, &hs); err == nil {
				hs.Direction = "response"
				c.ingestResponseHeightSync(&hs, nonce, "SSE devshard_receipt line")
			}
		}

		if raw, ok := envelope["devshard_receipt"]; ok {
			var receipt DevshardReceiptEvent
			if err := json.Unmarshal(raw, &receipt); err == nil {
				result.StateSig = receipt.StateSig
				result.StateHash = receipt.StateHash
				result.Nonce = receipt.Nonce
				result.Receipt = receipt.Receipt
				result.ConfirmedAt = receipt.ConfirmedAt
			}
			continue
		}

		if raw, ok := envelope["devshard_meta"]; ok {
			var meta DevshardMetaEvent
			if err := json.Unmarshal(raw, &meta); err == nil {
				txs, txErr := DevshardTxsFromBytes(meta.Mempool)
				if txErr == nil {
					result.Mempool = txs
				}
			}
			continue
		}

		// Inference data line -- forward to callback.
		if c.config.StreamCallback != nil {
			c.config.StreamCallback(nonce, line)
		}
	}
	if err := scanner.Err(); err != nil {
		logging.Warn("transport: SSE stream scanner error",
			"subsystem", "transport",
			"nonce", nonce,
			"saw_done", sawDone,
			"error", err.Error())
		return &result, fmt.Errorf("read SSE stream: %w", err)
	}
	if !sawDone {
		logging.Warn("transport: SSE closed without terminal data [DONE]",
			"subsystem", "transport",
			"nonce", nonce,
			"has_state_sig", len(result.StateSig) > 0,
			"mempool_txs", len(result.Mempool))
	}
	return &result, nil
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
// contentType is e.g. application/json or application/x-protobuf.
func (c *HTTPClient) doPostRaw(ctx context.Context, path string, body []byte, contentType string) (*http.Response, error) {
	url := c.baseURL + c.routePrefix + path

	ts := time.Now().Unix()
	sig, err := SignRequest(c.signer, c.escrowID, body, ts)
	if err != nil {
		return nil, fmt.Errorf("sign request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if contentType == "" {
		contentType = "application/json"
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set(HeaderSignature, hex.EncodeToString(sig))
	req.Header.Set(HeaderTimestamp, strconv.FormatInt(ts, 10))

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http post %s: %w", path, err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("http %s: status %d: %s", path, resp.StatusCode, string(respBody))
	}

	return resp, nil
}

// doPost sends a signed POST request and returns the response body.
func (c *HTTPClient) doPost(ctx context.Context, path string, body []byte) ([]byte, error) {
	resp, err := c.doPostRaw(ctx, path, body, "application/json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// doGet sends a GET request and returns the response body.
// No auth signing -- GET endpoints skip auth on the server side for now.
func (c *HTTPClient) doGet(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get %s: status %d", url, resp.StatusCode)
	}

	return io.ReadAll(resp.Body)
}

func (c *HTTPClient) logEmitUserHeightSync(sec *heightsync.HeightSyncSection, nonce uint64) {
	if sec == nil {
		logging.Debug("heightsync: emit",
			heightsync.LogFieldSubsystem, "heightsync",
			heightsync.LogFieldDirection, "request",
			heightsync.LogFieldMode, "omit",
			heightsync.LogFieldNonce, nonce,
		)
		return
	}
	prefix := heightSyncHashPrefix(sec.MainnetBlockHashHex)
	var localH int64
	if c.heightSyncLogOracle != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		hdr, err := c.heightSyncLogOracle.Latest(ctx)
		cancel()
		if err == nil && hdr != nil {
			localH = hdr.Height
		}
	}
	delta := sec.MainnetHeight - localH
	logging.Debug("heightsync: emit",
		heightsync.LogFieldSubsystem, "heightsync",
		heightsync.LogFieldDirection, "request",
		heightsync.LogFieldMode, "anchor",
		heightsync.LogFieldNonce, nonce,
		heightsync.LogFieldHeight, sec.MainnetHeight,
		heightsync.LogFieldBlockHashPrefix, prefix,
		heightsync.LogFieldLocalAligned, localH,
		heightsync.LogFieldDelta, delta,
		heightsync.LogFieldTrustLevel, string(heightsync.TrustOracle),
	)
}

func (c *HTTPClient) recordUserOutboundAnchorIfAnchor(hs *heightsync.HeightSyncSection, source string) {
	if c.heightSyncAudit == nil || hs == nil || !heightsync.IsAnchorSection(hs) {
		return
	}
	raw, err := decodeMainnetBlockHashHex(hs.MainnetBlockHashHex)
	if err != nil {
		logging.Debug("heightsync: skip audit record", heightsync.LogFieldSubsystem, "heightsync", "error", err.Error())
		return
	}
	c.heightSyncAudit.Append(heightsync.AnchorAttestation{
		PeerID:           c.signer.Address(),
		Direction:        "request",
		MainnetHeight:    hs.MainnetHeight,
		MainnetBlockHash: raw,
		ObservedAtUnixMs: time.Now().UnixMilli(),
		SourceMessage:    source,
		Trust:            heightsync.TrustOracle,
	})
	heightsync.IncOutboundAnchor("request", c.escrowID, c.signer.Address())
}

func (c *HTTPClient) logPeerHeightSyncFromSSE(hs *heightsync.HeightSyncSection, nonce uint64) {
	mode := "omit"
	var peerH int64
	prefix := ""
	if hs != nil && heightsync.IsAnchorSection(hs) {
		mode = "anchor"
		peerH = hs.MainnetHeight
		prefix = heightSyncHashPrefix(hs.MainnetBlockHashHex)
	}
	var oracleHdr *blockoracle.Header
	if c.heightSyncLogOracle != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		hdr, err := c.heightSyncLogOracle.Latest(ctx)
		cancel()
		if err == nil && hdr != nil {
			oracleHdr = hdr
		}
	}
	var localH int64
	if oracleHdr != nil {
		localH = oracleHdr.Height
	}
	delta := peerH - localH
	trust := heightsync.InboundTrust(hs, oracleHdr)
	kvs := []any{
		heightsync.LogFieldSubsystem, "heightsync",
		heightsync.LogFieldDirection, "response",
		heightsync.LogFieldMode, mode,
		heightsync.LogFieldNonce, nonce,
		heightsync.LogFieldPeerID, c.baseURL,
		heightsync.LogFieldPeerHeight, peerH,
		heightsync.LogFieldPeerBlockHashPrefix, prefix,
		heightsync.LogFieldLocalAligned, localH,
		heightsync.LogFieldDelta, delta,
	}
	if trust != "" {
		kvs = append(kvs, heightsync.LogFieldTrustLevel, string(trust))
	}
	logging.Debug("heightsync: peer attestation received", kvs...)
}

func (c *HTTPClient) recordHostInboundAnchorIfAnchor(hs *heightsync.HeightSyncSection, source string, originBlobAvailable bool) {
	if c.heightSyncAudit == nil || hs == nil || !heightsync.IsAnchorSection(hs) {
		return
	}
	raw, err := decodeMainnetBlockHashHex(hs.MainnetBlockHashHex)
	if err != nil {
		logging.Debug("heightsync: skip audit record", heightsync.LogFieldSubsystem, "heightsync", "error", err.Error())
		return
	}
	var oracleHdr *blockoracle.Header
	if c.heightSyncLogOracle != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		hdr, err := c.heightSyncLogOracle.Latest(ctx)
		cancel()
		if err == nil && hdr != nil {
			oracleHdr = hdr
		}
	}
	trust := heightsync.InboundTrust(hs, oracleHdr)
	c.heightSyncAudit.Append(heightsync.AnchorAttestation{
		PeerID:                    c.baseURL,
		Direction:                 "response",
		MainnetHeight:             hs.MainnetHeight,
		MainnetBlockHash:          raw,
		ObservedAtUnixMs:          time.Now().UnixMilli(),
		SourceMessage:             source,
		Trust:                     trust,
		OriginatorSenderID:        strings.TrimSpace(hs.OriginatorSenderID),
		OriginSignedBlobAvailable: originBlobAvailable,
	})
	heightsync.IncInboundAnchor("response", string(trust), c.escrowID)
}
