package transport

import (
	"bytes"
	"context"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"common/chainoracle/blocks"
	"devshard/heightsync"
	"devshard/host"
	"devshard/logging"
	"devshard/signing"
	"devshard/types"
)

type pendingUntrustedTip struct {
	MainnetHeight int64
	BlockHash     []byte
	PeerID        string
}

// WithHeightSync wires an anchor scheduler for outbound inference responses and
// enables inbound height-sync audit logging when sched is non-nil. logOracle is
// optional: when set, debug logs include local oracle height and Δ vs peer.
func WithHeightSync(sched *heightsync.AnchorScheduler, logOracle blocks.BlockOracle) ServerOption {
	return func(s *Server) {
		s.heightSync = sched
		s.heightSyncLogOracle = logOracle
		if sched != nil {
			s.heightSyncAudit = heightsync.NewAuditRing(0)
			s.heightSyncMarks = heightsync.NewMarkLog()
			s.pendingUntrustedBySession = make(map[string]*pendingUntrustedTip)
			// Cold-start seed RPC (spec §18.5) is on whenever height sync is.
			// WithHeightSyncSeedRPC(false) after this option still disables it
			// (host stays correct, just unseedable).
			s.heightSyncSeedRPC = true
		}
	}
}

// WithHeightSyncSeedRPC toggles POST /sessions/:id/height-sync for courier
// cold-start cache seeding (spec §18.5). Defaults to on when WithHeightSync is set.
func WithHeightSyncSeedRPC(enabled bool) ServerOption {
	return func(s *Server) {
		s.heightSyncSeedRPC = enabled
	}
}

// HeightSyncAuditRing returns the inbound audit ring when height sync is enabled, or nil.
func (s *Server) HeightSyncAuditRing() *heightsync.AuditRing {
	return s.heightSyncAudit
}

// HeightSyncMarks returns L4/L5a marks recorded at the transport edge.
func (s *Server) HeightSyncMarks() *heightsync.MarkLog {
	if s == nil {
		return nil
	}
	return s.heightSyncMarks
}

// SetHeightSyncResponseAfterSignHook registers a hook invoked after the host signs an
// outbound response Anchor (testenv / negative tests only).
func (s *Server) SetHeightSyncResponseAfterSignHook(fn func(sec *heightsync.HeightSyncSection, nonce uint64)) {
	if s == nil {
		return
	}
	s.heightSyncResponseAfterSignHook = fn
}

// SetHeightSyncSeedRPCEnabled toggles POST /sessions/:id/height-sync (tests only).
// Held-response courier scenarios disable it so the cache warms from inference
// responses rather than the cold-start seed round, which would MarkPropagated
// the seed tip to every slot during the initial sync turn.
func (s *Server) SetHeightSyncSeedRPCEnabled(enabled bool) {
	if s == nil {
		return
	}
	s.heightSyncSeedRPC = enabled
}

func (s *Server) attachHeightSyncConfirmation() {
	if s == nil || s.heightSyncAudit == nil || s.host == nil {
		return
	}
	seen := make(map[string]struct{})
	var roster []string
	for _, slot := range s.host.Group() {
		addr := strings.TrimSpace(slot.ValidatorAddress)
		if addr == "" {
			continue
		}
		if _, dup := seen[addr]; dup {
			continue
		}
		seen[addr] = struct{}{}
		roster = append(roster, addr)
	}
	idx := heightsync.NewConfirmationIndex(heightsync.ConfirmationConfig{
		Roster: roster,
		Oracle: s.heightSyncLogOracle,
	})
	s.heightSyncAudit.AttachConfirmation(idx)
}

// ConfirmationView returns host-side IsStrictlyConfirmed when height sync is enabled.
func (s *Server) ConfirmationView() heightsync.ConfirmationView {
	if s == nil || s.heightSyncAudit == nil {
		return nil
	}
	return s.heightSyncAudit.ConfirmationView()
}

func (s *Server) latestOracleHeader(ctx context.Context) *blocks.Header {
	if s.heightSyncLogOracle == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	hdr, err := s.heightSyncLogOracle.Latest(ctx)
	if err != nil || hdr == nil {
		return nil
	}
	return hdr
}

func (s *Server) reconcilePendingUntrusted(sessionID string, oracleHdr *blocks.Header) {
	if s.pendingUntrustedBySession == nil || oracleHdr == nil {
		return
	}
	s.pendingUntrustedMu.Lock()
	defer s.pendingUntrustedMu.Unlock()
	p := s.pendingUntrustedBySession[sessionID]
	if p == nil {
		return
	}
	if oracleHdr.Height < p.MainnetHeight {
		return
	}
	if oracleHdr.Height > p.MainnetHeight {
		delete(s.pendingUntrustedBySession, sessionID)
		return
	}
	if !bytes.Equal(oracleHdr.BlockHash, p.BlockHash) {
		logging.Warn("heightsync: untrusted peer tip disagrees with oracle at reconciled height",
			heightsync.LogFieldSubsystem, "heightsync",
			heightsync.LogFieldSessionID, sessionID,
			heightsync.LogFieldHeight, p.MainnetHeight,
			heightsync.LogFieldPeerID, p.PeerID,
			"oracle_block_hash_prefix", heightSyncHashPrefix(hex.EncodeToString(oracleHdr.BlockHash)),
			"untrusted_block_hash_prefix", heightSyncHashPrefix(hex.EncodeToString(p.BlockHash)),
		)
	}
	delete(s.pendingUntrustedBySession, sessionID)
}

func (s *Server) notePendingUntrustedInbound(sessionID, peerID string, hs *heightsync.HeightSyncSection, oracleHdr *blocks.Header) {
	if s.pendingUntrustedBySession == nil || hs == nil || !heightsync.IsAnchorSection(hs) {
		return
	}
	localH := int64(0)
	if oracleHdr != nil {
		localH = oracleHdr.Height
	}
	if hs.MainnetHeight <= localH {
		return
	}
	raw, err := decodeMainnetBlockHashHex(hs.MainnetBlockHashHex)
	if err != nil {
		return
	}
	s.pendingUntrustedMu.Lock()
	defer s.pendingUntrustedMu.Unlock()
	s.pendingUntrustedBySession[sessionID] = &pendingUntrustedTip{
		MainnetHeight: hs.MainnetHeight,
		BlockHash:     append([]byte(nil), raw...),
		PeerID:        peerID,
	}
}

func (s *Server) logInboundHeightSync(peerID, sessionID string, nonce uint64, hs *heightsync.HeightSyncSection, oracleHdr *blocks.Header, v heightsync.InboundValidation) {
	mode := "omit"
	var peerH int64
	peerPrefix := ""
	if hs != nil && heightsync.IsAnchorSection(hs) {
		mode = "anchor"
		peerH = hs.MainnetHeight
		peerPrefix = heightSyncHashPrefix(hs.MainnetBlockHashHex)
	}
	var localH int64
	if oracleHdr != nil {
		localH = oracleHdr.Height
	}
	delta := peerH - localH
	kvs := []any{
		heightsync.LogFieldSubsystem, "heightsync",
		heightsync.LogFieldDirection, "request",
		heightsync.LogFieldMode, mode,
		heightsync.LogFieldNonce, nonce,
		heightsync.LogFieldHostID, s.host.Signer().Address(),
		heightsync.LogFieldPeerID, peerID,
		heightsync.LogFieldSessionID, sessionID,
		heightsync.LogFieldPeerHeight, peerH,
		heightsync.LogFieldPeerBlockHashPrefix, peerPrefix,
		heightsync.LogFieldLocalAligned, localH,
		heightsync.LogFieldDelta, delta,
		heightsync.LogFieldClassification, string(v.Result),
	}
	if v.Tag != "" {
		kvs = append(kvs, heightsync.LogFieldTag, string(v.Tag))
	}
	if v.Reason != "" {
		kvs = append(kvs, heightsync.LogFieldReason, v.Reason)
	}
	if v.Trust != "" {
		kvs = append(kvs, heightsync.LogFieldTrustLevel, string(v.Trust))
	}
	logging.Debug("heightsync: peer attestation received", kvs...)
}

func (s *Server) classifyInboundHeightSync(nonce uint64, hs *heightsync.HeightSyncSection, oracleHdr *blocks.Header) heightsync.InboundValidation {
	if s.heightSync == nil {
		if heightsync.IsAnchorSection(hs) {
			return heightsync.InboundValidation{
				Result: heightsync.ResultValidAnchor,
				Trust:  heightsync.InboundTrust(hs, oracleHdr),
			}
		}
		return heightsync.InboundValidation{Result: heightsync.ResultOmit}
	}
	schedK := s.heightSync.K()
	schedSlots := s.heightSync.SlotsNum()
	escrowH := s.host.HeightSyncEscrowHints(schedK, schedSlots)
	return heightsync.ClassifyInboundRequestAnchor(hs, heightsync.InboundValidateParams{
		Nonce:     nonce,
		K:         schedK,
		Slots:     schedSlots,
		Escrow:    escrowH,
		Now:       time.Now(),
		Freshness: heightsync.DefaultOriginatorFreshness,
		OracleHdr: oracleHdr,
	})
}

func (s *Server) recordInboundAnchorIfAnchor(peerID string, hs *heightsync.HeightSyncSection, source string, v heightsync.InboundValidation) {
	if s.heightSyncAudit == nil || hs == nil {
		return
	}
	switch v.Result {
	case heightsync.ResultValidAnchor, heightsync.ResultValidLazyAnchor:
		if !heightsync.IsAnchorSection(hs) {
			return
		}
	case heightsync.ResultInvalidStaleOrigin:
		s.recordInboundDispute(peerID, hs, source, v)
		return
	default:
		return
	}
	raw, err := decodeMainnetBlockHashHex(hs.MainnetBlockHashHex)
	if err != nil {
		logging.Debug("heightsync: skip audit record", heightsync.LogFieldSubsystem, "heightsync", "error", err.Error())
		return
	}
	s.heightSyncAudit.Append(heightsync.AnchorAttestation{
		PeerID:                peerID,
		Direction:             "request",
		MainnetHeight:         hs.MainnetHeight,
		MainnetBlockHash:      raw,
		ObservedAtUnixMs:      time.Now().UnixMilli(),
		SourceMessage:         source,
		Trust:                 v.Trust,
		Tag:                   v.Tag,
		OriginatorSenderID:    strings.TrimSpace(hs.OriginatorSenderID),
		OriginatorTimestampMs: hs.OriginatorTimestampMs,
	})
	heightsync.IncInboundAnchor("request", string(v.Trust), s.host.EscrowID())
	if v.Result == heightsync.ResultValidLazyAnchor {
		heightsync.IncLazyAnchor()
	}
}

func (s *Server) recordInboundDispute(peerID string, hs *heightsync.HeightSyncSection, source string, v heightsync.InboundValidation) {
	if s.heightSyncAudit == nil || hs == nil {
		return
	}
	raw, err := decodeMainnetBlockHashHex(hs.MainnetBlockHashHex)
	if err != nil {
		raw = nil
	}
	s.heightSyncAudit.Append(heightsync.AnchorAttestation{
		PeerID:                peerID,
		Direction:             "request",
		MainnetHeight:         hs.MainnetHeight,
		MainnetBlockHash:      raw,
		ObservedAtUnixMs:      time.Now().UnixMilli(),
		SourceMessage:         source,
		Trust:                 v.Trust,
		OriginatorSenderID:    strings.TrimSpace(hs.OriginatorSenderID),
		OriginatorTimestampMs: hs.OriginatorTimestampMs,
	})
}

func (s *Server) logOutboundHeightSync(sec *heightsync.HeightSyncSection, nonce uint64) {
	if sec == nil {
		logging.Debug("heightsync: emit",
			heightsync.LogFieldSubsystem, "heightsync",
			heightsync.LogFieldDirection, "response",
			heightsync.LogFieldMode, "omit",
			heightsync.LogFieldNonce, nonce,
		)
		return
	}
	prefix := heightSyncHashPrefix(sec.MainnetBlockHashHex)
	var localH int64
	if s.heightSyncLogOracle != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		hdr, err := s.heightSyncLogOracle.Latest(ctx)
		cancel()
		if err == nil && hdr != nil {
			localH = hdr.Height
		}
	}
	delta := sec.MainnetHeight - localH
	logging.Debug("heightsync: emit",
		heightsync.LogFieldSubsystem, "heightsync",
		heightsync.LogFieldDirection, "response",
		heightsync.LogFieldMode, "anchor",
		heightsync.LogFieldNonce, nonce,
		heightsync.LogFieldHeight, sec.MainnetHeight,
		heightsync.LogFieldBlockHashPrefix, prefix,
		heightsync.LogFieldLocalAligned, localH,
		heightsync.LogFieldDelta, delta,
		heightsync.LogFieldTrustLevel, string(heightsync.TrustOracle),
	)
}

// recordForceRequestAnchorMissingIfApplicable emits a structured warn log and
// appends a sentinel audit-ring entry when an inbound request whose nonce falls
// inside an active forced sync turn arrives without a valid Anchor section.
//
// Per HEIGHT_SYNC_PROTOCOL_PROPOSAL forced sync turn
// the diff that opened the turn is the only
// authoritative trigger; hosts learn the window from their own escrow state
// after applying the diff, NOT from a per-request HTTP signal. A user that
// strips height_sync from its in-window requests is therefore self-inflicting
// a violation of its own signed diff. The request still processes normally —
// this path produces dispute evidence, not a rejection.
func (s *Server) recordForceRequestAnchorMissingIfApplicable(peerID string, nonce uint64, hs *heightsync.HeightSyncSection, source string) {
	if s.heightSync == nil {
		return
	}
	if heightsync.IsAnchorSection(hs) {
		return
	}
	schedK := s.heightSync.K()
	schedSlots := s.heightSync.SlotsNum()
	escrowH := s.host.HeightSyncEscrowHints(schedK, schedSlots)
	if escrowH == nil || escrowH.ForcedEnd == 0 {
		return
	}
	if nonce < escrowH.ForcedStart || nonce > escrowH.ForcedEnd {
		return
	}
	logging.Warn("heightsync: force_request_anchor_missing",
		heightsync.LogFieldSubsystem, "heightsync",
		heightsync.LogFieldDirection, "request",
		heightsync.LogFieldNonce, nonce,
		heightsync.LogFieldPeerID, peerID,
		heightsync.LogFieldForcedStart, escrowH.ForcedStart,
		heightsync.LogFieldForcedEnd, escrowH.ForcedEnd,
		heightsync.LogFieldSource, source,
	)
	if s.heightSyncAudit == nil {
		return
	}
	s.heightSyncAudit.Append(heightsync.AnchorAttestation{
		PeerID:           peerID,
		Direction:        "request",
		MainnetHeight:    0,
		MainnetBlockHash: nil,
		ObservedAtUnixMs: time.Now().UnixMilli(),
		SourceMessage:    source,
		Trust:            heightsync.TrustForceRequestAnchorMissing,
	})
}

func (s *Server) recordOutboundAnchorIfAnchor(hs *heightsync.HeightSyncSection, source string) {
	if s.heightSyncAudit == nil || hs == nil || !heightsync.IsAnchorSection(hs) {
		return
	}
	raw, err := decodeMainnetBlockHashHex(hs.MainnetBlockHashHex)
	if err != nil {
		logging.Debug("heightsync: skip outbound audit record", heightsync.LogFieldSubsystem, "heightsync", "error", err.Error())
		return
	}
	localID := s.host.Signer().Address()
	s.heightSyncAudit.Append(heightsync.AnchorAttestation{
		PeerID:                localID,
		Direction:             "response",
		MainnetHeight:         hs.MainnetHeight,
		MainnetBlockHash:      raw,
		ObservedAtUnixMs:      time.Now().UnixMilli(),
		SourceMessage:         source,
		Trust:                 heightsync.TrustOracle,
		OriginatorSenderID:    strings.TrimSpace(hs.OriginatorSenderID),
		OriginatorTimestampMs: hs.OriginatorTimestampMs,
	})
	dir := hs.Direction
	if dir == "" {
		dir = "response"
	}
	heightsync.IncOutboundAnchor(dir, s.host.EscrowID(), localID)
}

// heightSyncSeedResponse is the JSON body for POST .../height-sync (courier cold-start).
type heightSyncSeedResponse struct {
	HeightSync *heightsync.HeightSyncSection `json:"height_sync,omitempty"`
}

// HandleHeightSync emits one host Anchor (ForceAnchor) for courier cache seeding.
// Requires WithHeightSync (seed RPC defaults on); escrow owner only.
func (s *Server) HandleHeightSync(c echo.Context) error {
	if s.heightSync == nil || !s.heightSyncSeedRPC {
		return echo.NewHTTPError(http.StatusNotFound, "height-sync seed RPC disabled")
	}
	sender, err := getSender(c)
	if err != nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "missing sender")
	}
	if !s.isOwner(sender) {
		return echo.NewHTTPError(http.StatusForbidden, "restricted to escrow owner")
	}
	sessionID := c.Param("id")
	if sessionID != s.host.EscrowID() {
		return echo.NewHTTPError(http.StatusNotFound, "session not found")
	}

	h := heightsync.DecideHints{
		Nonce:              0, // seed consumes no session nonce
		ForceAnchor:        true,
		OriginatorSenderID: s.host.Signer().Address(),
	}
	sec, dErr, oracleMiss := s.heightSync.Decide(c.Request().Context(), h)
	if oracleMiss {
		heightsync.IncOracleFailure(s.host.Signer().Address())
	}
	if dErr != nil {
		logging.Debug("heightsync: seed RPC anchor error",
			heightsync.LogFieldSubsystem, "heightsync",
			"error", dErr.Error())
	}
	if sec != nil {
		sec.Direction = "response"
		if !s.attachResponseOriginSignature(sec, h.Nonce) {
			s.logOutboundHeightSync(nil, h.Nonce)
			return writeJSON(c, http.StatusOK, heightSyncSeedResponse{})
		}
		s.logOutboundHeightSync(sec, h.Nonce)
		s.recordOutboundAnchorIfAnchor(sec, c.Request().Method+" "+c.Path())
	}
	return writeJSON(c, http.StatusOK, heightSyncSeedResponse{HeightSync: sec})
}

// SetHeightSyncOriginSigner overrides the signer used for outbound response
// origin signatures (negative tests only).
func (s *Server) SetHeightSyncOriginSigner(signer signing.Signer) {
	if s == nil {
		return
	}
	s.heightSyncOriginSigner = signer
}

func (s *Server) attachResponseOriginSignature(sec *heightsync.HeightSyncSection, nonce uint64) bool {
	if s == nil || sec == nil || s.heightSync == nil {
		return false
	}
	signer := s.host.Signer()
	if s.heightSyncOriginSigner != nil {
		signer = s.heightSyncOriginSigner
	}
	_, sig, err := heightsync.SignOrigin(signer, sec)
	if err != nil {
		logging.Debug("heightsync: sign response origin failed",
			heightsync.LogFieldSubsystem, "heightsync",
			"error", err.Error())
		return false
	}
	sec.SenderSignature = sig
	if s.heightSyncResponseAfterSignHook != nil {
		s.heightSyncResponseAfterSignHook(sec, nonce)
	}
	return true
}

func (s *Server) recordEnvelopeBindingRequest(c echo.Context, req host.HostRequest, sec *heightsync.HeightSyncSection, oracleHdr *blocks.Header) {
	if s == nil || s.heightSyncMarks == nil || sec == nil {
		return
	}
	var txs []*types.DevshardTx
	for _, d := range req.Diffs {
		txs = append(txs, d.Txs...)
	}
	var local uint64
	if oracleHdr != nil && oracleHdr.Height > 0 {
		local = uint64(oracleHdr.Height)
	}
	marks := heightsync.CheckEnvelopeBinding(heightsync.LogPlaneInput{
		Nonce:        req.Nonce,
		Txs:          txs,
		Sec:          sec,
		LocalAligned: local,
		RequestLeg:   requestLegEvidenceFromContext(c, s.host.EscrowID()),
	}, heightsync.DefaultHeartbeatConfig())
	s.heightSyncMarks.AppendAll(marks)
}

func (s *Server) recordEnvelopeBindingResponse(nonce uint64, sec *heightsync.HeightSyncSection) {
	if s == nil || s.heightSyncMarks == nil || sec == nil {
		return
	}
	marks := heightsync.CheckEnvelopeBinding(heightsync.LogPlaneInput{
		Nonce: nonce,
		Txs:   s.host.MempoolTxs(),
		Sec:   sec,
	}, heightsync.DefaultHeartbeatConfig())
	s.heightSyncMarks.AppendAll(marks)
}

func requestLegEvidenceFromContext(c echo.Context, escrowID string) *heightsync.RequestLegEvidence {
	if c == nil || c.Request() == nil {
		return nil
	}
	body, _ := getBody(c)
	sigHex := c.Request().Header.Get(HeaderSignature)
	tsStr := c.Request().Header.Get(HeaderTimestamp)
	sig, _ := hex.DecodeString(sigHex)
	ts, _ := strconv.ParseInt(tsStr, 10, 64)
	return &heightsync.RequestLegEvidence{
		Body:      body,
		Sig:       sig,
		Timestamp: ts,
		EscrowID:  escrowID,
	}
}
