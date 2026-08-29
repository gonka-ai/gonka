package transport

import (
	"context"
	"fmt"
	"strings"
	"time"

	json "github.com/goccy/go-json"

	"common/chainoracle/blocks"
	"devshard/heightsync"
	"devshard/host"
	"devshard/logging"
)

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

// HeightSyncEvidenceFor returns the verified signed origin blob for dispute
// exculpation (spec §15 / §18.3).
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
		if originatorObservedAtMs(hs) <= 0 {
			logging.Debug("heightsync: origin_ts_missing",
				heightsync.LogFieldSubsystem, "heightsync",
				heightsync.LogFieldDirection, "response",
				heightsync.LogFieldNonce, nonce)
		} else {
			c.heightSyncPeerTips.RecordOriginWithBlob(hs, blob, hs.SenderSignature)
			blobOK = true
		}
	} else if c.heightSyncPeerTips != nil {
		if originatorObservedAtMs(hs) <= 0 {
			logging.Debug("heightsync: origin_ts_missing",
				heightsync.LogFieldSubsystem, "heightsync",
				heightsync.LogFieldDirection, "response",
				heightsync.LogFieldNonce, nonce)
		} else {
			c.updateObservedPeerTip(hs)
		}
	}
	c.logPeerHeightSyncFromSSE(hs, nonce)
	c.recordHostInboundAnchorIfAnchor(hs, source, blobOK)
}

func (c *HTTPClient) carryForwardPeerTip(sec *heightsync.HeightSyncSection) {
	if c.heightSyncPeerTips == nil {
		return
	}
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

// HeightSyncPeerTips returns the shared peer-tip cache when height sync is enabled, or nil.
func (c *HTTPClient) HeightSyncPeerTips() *HeightSyncPeerTips {
	return c.heightSyncPeerTips
}

// ObservedHeightNow returns the highest fresh mainnet height in the courier
// peer-tip cache (spec §18.2). The bool is false when height sync is off or no
// fresh tip exists.
func (c *HTTPClient) ObservedHeightNow() (uint64, bool) {
	h, _, ok := c.ObservedStampNow()
	return h, ok
}

// ObservedStampNow returns the highest fresh (height, hash) in the courier
// peer-tip cache. Hash is nil when the tip has no decodable block hash.
func (c *HTTPClient) ObservedStampNow() (uint64, []byte, bool) {
	if c == nil || c.heightSyncPeerTips == nil {
		return 0, nil, false
	}
	tip := c.heightSyncPeerTips.MaxFresh(time.Now(), c.heightSyncPeerTips.freshness())
	if tip == nil || tip.MainnetHeight <= 0 {
		return 0, nil, false
	}
	hash, err := decodeMainnetBlockHashHex(tip.MainnetBlockHashHex)
	if err != nil {
		hash = nil
	}
	return uint64(tip.MainnetHeight), hash, true
}

// SeedHeightSync calls the optional host cold-start RPC and records the returned
// Anchor in the peer-tip cache. ok is false when height sync is disabled, the RPC
// is unavailable (404), or the host omitted the section (oracle miss). One HTTP
// attempt: the session seed loop owns 429/503 retry, not doPostRaw.
func (c *HTTPClient) SeedHeightSync(ctx context.Context) (ok bool, err error) {
	if c == nil || c.heightSyncPeerTips == nil {
		return false, nil
	}
	var out heightSyncSeedResponse
	err = c.postOnce(ctx, "/sessions/"+c.escrowID+"/height-sync", c.config.QueryTimeout, struct{}{}, &out)
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
		PeerID:                c.signer.Address(),
		Direction:             "request",
		MainnetHeight:         hs.MainnetHeight,
		MainnetBlockHash:      raw,
		ObservedAtUnixMs:      time.Now().UnixMilli(),
		SourceMessage:         source,
		Trust:                 heightsync.TrustOracle,
		OriginatorSenderID:    strings.TrimSpace(hs.OriginatorSenderID),
		OriginatorTimestampMs: hs.OriginatorTimestampMs,
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
	var oracleHdr *blocks.Header
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
	var oracleHdr *blocks.Header
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
		OriginatorTimestampMs:     hs.OriginatorTimestampMs,
		OriginSignedBlobAvailable: originBlobAvailable,
	})
	heightsync.IncInboundAnchor("response", string(trust), c.escrowID)
}

func (c *HTTPClient) wrapInferenceRequest(ctx context.Context, req host.HostRequest, ir InferenceRequest) (body []byte, contentType string, outboundHS *heightsync.HeightSyncSection, err error) {
	contentType = "application/json"
	if c.heightSync == nil {
		body, err = json.Marshal(ir)
		if err != nil {
			return nil, "", nil, fmt.Errorf("marshal json: %w", err)
		}
		return body, contentType, nil, nil
	}
	h := heightsync.DecideHints{
		Nonce:       req.Nonce,
		ForceAnchor: req.ForceHeightSyncAnchor,
		Escrow:      req.HeightSyncEscrow,
		Recipient:   c.baseURL,
		Direction:   "request",
	}
	if c.heightSyncPeerTips != nil {
		h.Propagator = c.heightSyncPeerTips
	} else {
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
			return nil, "", nil, fmt.Errorf("marshal inference envelope: %w", err)
		}
		contentType = "application/x-protobuf"
		c.logEmitUserHeightSync(sec, req.Nonce)
		c.recordUserOutboundAnchorIfAnchor(sec, "POST /chat/completions")
		return body, contentType, outboundHS, nil
	}
	body, err = json.Marshal(ir)
	if err != nil {
		return nil, "", nil, fmt.Errorf("marshal json: %w", err)
	}
	if c.heightSyncPeerTips != nil {
		ev := "request_decide_omit"
		if oracleMiss {
			ev = "request_decide_omit_oracle_miss"
		}
		c.heightSyncPeerTips.LogCacheState(ev, req.Nonce)
	}
	c.logEmitUserHeightSync(nil, req.Nonce)
	return body, contentType, nil, nil
}

// HeightSyncRepair POSTs a signed repair probe (group-member HTTP auth).
func (c *HTTPClient) HeightSyncRepair(ctx context.Context, req *heightsync.RepairRequest) (*heightsync.RepairResponse, error) {
	path := fmt.Sprintf("/sessions/%s/heightsync/repair", c.escrowID)
	timeout := c.config.QueryTimeout
	if timeout <= 0 || timeout > DefaultRepairTimeout {
		timeout = DefaultRepairTimeout
	}
	var resp heightsync.RepairResponse
	if err := c.post(ctx, path, timeout, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
