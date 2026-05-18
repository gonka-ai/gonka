package transport

import (
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
	"google.golang.org/protobuf/proto"

	"github.com/labstack/echo/v4"

	"devshard/blockoracle"
	"devshard/bridge"
	"devshard/gossip"
	"devshard/heightsync"
	"devshard/host"
	"devshard/logging"
	"devshard/signing"
	"devshard/storage"
	"devshard/types"
)

const contextKeySender = "devshard_sender"

// Server wraps a host.Host and exposes it over HTTP via Echo.
type Server struct {
	host        *host.Host
	store       storage.Storage
	gossip      *gossip.Gossip // nil until gossip is wired
	verifier    signing.Verifier
	userAddr    string               // session user address, allowed alongside group members
	peerClients map[int]*HTTPClient  // slot index -> client, for timeout verification
	rateLimit   *rateLimiter         // nil = no limiting
	maxBodySize int64                // max request body bytes, 0 = no limit
	bridge      bridge.MainnetBridge // optional, for warm key verification

	heightSync          *heightsync.AnchorScheduler // optional outbound anchor cadence
	heightSyncAudit     *heightsync.AuditRing       // optional inbound anchor trail
	heightSyncLogOracle blockoracle.BlockOracle     // optional Latest() for debug logs (delta)
	heightSyncSeedRPC   bool                        // optional POST .../height-sync cold-start seed
	respNonceMu         sync.Mutex
	responseNonce       map[string]uint64 // session id (URL :id) -> monotonic host response counter
	firstRespMu         sync.Mutex
	firstInferenceResp  map[string]bool // session id -> first SSE receipt for this session was already sent

	pendingUntrustedMu        sync.Mutex
	pendingUntrustedBySession map[string]*pendingUntrustedTip // session id -> ahead-of-oracle peer claim pending oracle reconciliation

	// Debug (testenv): block after HandleRequest until release or client disconnect.
	holdInferenceMu    sync.Mutex
	holdInferenceGate  chan struct{} // closed to release; non-nil while armed
	holdInferenceArmed bool

	// heightSyncResponseAfterSignHook runs after attachResponseOriginSignature (testenv only).
	heightSyncResponseAfterSignHook func(sec *heightsync.HeightSyncSection, nonce uint64)
}

type pendingUntrustedTip struct {
	MainnetHeight int64
	BlockHash     []byte
	PeerID        string
}

// ServerOption configures the Server.
type ServerOption func(*Server)

// WithRateLimit enables per-sender rate limiting.
func WithRateLimit(cfg RateLimitConfig) ServerOption {
	return func(s *Server) {
		s.rateLimit = newRateLimiter(cfg)
	}
}

// WithMaxBodySize sets the maximum request body size in bytes.
func WithMaxBodySize(n int64) ServerOption {
	return func(s *Server) {
		s.maxBodySize = n
	}
}

// WithServerGossip attaches a gossip instance for nonce/tx propagation.
func WithServerGossip(g *gossip.Gossip) ServerOption {
	return func(s *Server) { s.gossip = g }
}

// WithServerPeerClients sets executor clients for timeout verification.
func WithServerPeerClients(peers map[int]*HTTPClient) ServerOption {
	return func(s *Server) { s.peerClients = peers }
}

// WithBridge sets the bridge for warm key verification in transport auth.
func WithBridge(b bridge.MainnetBridge) ServerOption {
	return func(s *Server) { s.bridge = b }
}

// WithHeightSync wires an anchor scheduler for outbound inference responses and
// enables inbound height-sync audit logging when sched is non-nil. logOracle is
// optional: when set, debug logs include local oracle height and Δ vs peer.
func WithHeightSync(sched *heightsync.AnchorScheduler, logOracle blockoracle.BlockOracle) ServerOption {
	return func(s *Server) {
		s.heightSync = sched
		s.heightSyncLogOracle = logOracle
		if sched != nil {
			s.heightSyncAudit = heightsync.NewAuditRing(0)
			s.pendingUntrustedBySession = make(map[string]*pendingUntrustedTip)
		}
	}
}

// WithHeightSyncSeedRPC enables POST /sessions/:id/height-sync for courier cold-start
// cache seeding (proposal §"Cold start"). Off by default.
func WithHeightSyncSeedRPC(enabled bool) ServerOption {
	return func(s *Server) {
		s.heightSyncSeedRPC = enabled
	}
}

// HeightSyncAuditRing returns the inbound audit ring when height sync is enabled, or nil.
func (s *Server) HeightSyncAuditRing() *heightsync.AuditRing {
	return s.heightSyncAudit
}

// SetHeightSyncResponseAfterSignHook registers a hook invoked after the host signs an
// outbound response Anchor (testenv / negative tests only).
func (s *Server) SetHeightSyncResponseAfterSignHook(fn func(sec *heightsync.HeightSyncSection, nonce uint64)) {
	if s == nil {
		return
	}
	s.heightSyncResponseAfterSignHook = fn
}

// NewServer creates an HTTP server wrapping the given host.
// userAddr is the session user's address -- allowed alongside group members.
func NewServer(
	h *host.Host,
	store storage.Storage,
	verifier signing.Verifier,
	userAddr string,
	opts ...ServerOption,
) (*Server, error) {
	s := &Server{
		host:               h,
		store:              store,
		verifier:           verifier,
		userAddr:           userAddr,
		responseNonce:      make(map[string]uint64),
		firstInferenceResp: make(map[string]bool),
	}
	for _, o := range opts {
		o(s)
	}
	s.attachHeightSyncConfirmation()
	return s, nil
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

// Host returns the underlying host.Host.
func (s *Server) Host() *host.Host { return s.host }

// SetGossip attaches a gossip instance for nonce/tx propagation.
func (s *Server) SetGossip(g *gossip.Gossip) { s.gossip = g }

// Register mounts all devshard routes on the given echo group.
// The caller typically mounts this under /v1/devshard.
func (s *Server) Register(g *echo.Group) {
	g.Use(s.AuthMiddleware)
	if s.rateLimit != nil {
		g.Use(rateLimitMiddleware(s.rateLimit))
	}
	g.POST("/sessions/:id/chat/completions", s.HandleInference)
	g.POST("/sessions/:id/height-sync", s.HandleHeightSync)
	g.POST("/sessions/:id/verify-timeout", s.HandleVerifyTimeout)
	g.POST("/sessions/:id/challenge-receipt", s.HandleChallengeReceipt)
	g.POST("/sessions/:id/gossip/nonce", s.HandleGossipNonce)
	g.POST("/sessions/:id/gossip/txs", s.HandleGossipTxs)
	// TODO: GET endpoints are intentionally unauthenticated for now.
	// Before production, restrict these to group members or add read-only auth.
	g.GET("/sessions/:id/diffs", s.HandleGetDiffs)
	g.GET("/sessions/:id/mempool", s.HandleGetMempool)
	g.GET("/sessions/:id/signatures", s.HandleGetSignatures)
}

// writeJSON serializes v with goccy/go-json, bypassing Echo's default serializer.
// TODO: set a custom echo.JSONSerializer using goccy/go-json on all Echo instances
// in decentralized-api, then replace writeJSON calls with c.JSON.
func writeJSON(c echo.Context, code int, v interface{}) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.Blob(code, echo.MIMEApplicationJSON, b)
}

// isAllowedSender returns true if addr is the session user, a group member,
// or a verified warm key for any group member.
func (s *Server) isAllowedSender(addr string) bool {
	if s.userAddr != "" && addr == s.userAddr {
		return true
	}
	if s.host.IsGroupMemberAddr(addr) {
		return true
	}
	return s.isWarmKeySender(addr)
}

// isWarmKeySender checks if addr is a known warm key (from state) or can be
// verified via bridge for any group member. Cached by the bridge implementation.
func (s *Server) isWarmKeySender(addr string) bool {
	if s.host.IsWarmKeyAddress(addr) {
		return true
	}

	// Bridge fallback for gossip bootstrap.
	if s.bridge == nil {
		return false
	}
	seen := make(map[string]bool, len(s.host.Group()))
	for _, slot := range s.host.Group() {
		if seen[slot.ValidatorAddress] {
			continue
		}
		seen[slot.ValidatorAddress] = true
		ok, err := s.bridge.VerifyWarmKey(addr, slot.ValidatorAddress)
		if err == nil && ok {
			return true
		}
	}
	return false
}

// isOwner returns true if addr is the session owner (escrow creator).
func (s *Server) isOwner(addr string) bool {
	return s.userAddr != "" && addr == s.userAddr
}

// isGroupMember returns true if addr is a group member or a warm key for
// a group member (excludes the user). Gossip is host-to-host; the user has
// no business gossiping.
func (s *Server) isGroupMember(addr string) bool {
	if s.host.IsGroupMemberAddr(addr) {
		return true
	}
	return s.isWarmKeySender(addr)
}

// authMiddleware reads the body, verifies the signature, checks group membership,
// and stores the sender address in the echo context.
// GET requests skip auth intentionally for now.
func (s *Server) AuthMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if c.Request().Method == http.MethodGet {
			// GET endpoints skip auth for now -- see Register comment.
			return next(c)
		}

		sigHex := c.Request().Header.Get(HeaderSignature)
		tsStr := c.Request().Header.Get(HeaderTimestamp)
		if sigHex == "" || tsStr == "" {
			return echo.NewHTTPError(http.StatusUnauthorized, "missing auth headers")
		}

		sig, err := hex.DecodeString(sigHex)
		if err != nil {
			return echo.NewHTTPError(http.StatusUnauthorized, "invalid signature hex")
		}

		ts, err := strconv.ParseInt(tsStr, 10, 64)
		if err != nil {
			return echo.NewHTTPError(http.StatusUnauthorized, "invalid timestamp")
		}

		// Cap body size before reading.
		if s.maxBodySize > 0 {
			c.Request().Body = http.MaxBytesReader(c.Response(), c.Request().Body, s.maxBodySize)
		}

		body, err := io.ReadAll(c.Request().Body)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "read body")
		}

		now := time.Now().Unix()
		addr, err := VerifyRequest(s.verifier, s.host.EscrowID(), body, sig, ts, now)
		if err != nil {
			return echo.NewHTTPError(http.StatusUnauthorized, err.Error())
		}

		if !s.isAllowedSender(addr) {
			return echo.NewHTTPError(http.StatusForbidden, "sender not in group")
		}

		// Store sender and re-inject body for handler.
		c.Set(contextKeySender, addr)
		c.Set("body", body)
		return next(c)
	}
}

func getSender(c echo.Context) (string, error) {
	v, ok := c.Get(contextKeySender).(string)
	if !ok || v == "" {
		return "", echo.NewHTTPError(http.StatusUnauthorized, "missing sender")
	}
	return v, nil
}

func getBody(c echo.Context) ([]byte, error) {
	v, ok := c.Get("body").([]byte)
	if !ok {
		return nil, echo.NewHTTPError(http.StatusBadRequest, "missing body")
	}
	return v, nil
}

func (s *Server) nextResponseNonce(sessionID string) uint64 {
	s.respNonceMu.Lock()
	defer s.respNonceMu.Unlock()
	s.responseNonce[sessionID]++
	return s.responseNonce[sessionID]
}

func (s *Server) latestOracleHeader(ctx context.Context) *blockoracle.Header {
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

func (s *Server) reconcilePendingUntrusted(sessionID string, oracleHdr *blockoracle.Header) {
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

func (s *Server) notePendingUntrustedInbound(sessionID, peerID string, hs *heightsync.HeightSyncSection, oracleHdr *blockoracle.Header) {
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

func (s *Server) logInboundHeightSync(peerID, sessionID string, nonce uint64, hs *heightsync.HeightSyncSection, oracleHdr *blockoracle.Header, v heightsync.InboundValidation) {
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

func (s *Server) classifyInboundHeightSync(nonce uint64, hs *heightsync.HeightSyncSection, oracleHdr *blockoracle.Header) heightsync.InboundValidation {
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

func heightSyncHashPrefix(hexStr string) string {
	h := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(hexStr), "0x"), "0X")
	if len(h) >= 8 {
		return strings.ToLower(h[:8])
	}
	return strings.ToLower(h)
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
		PeerID:             peerID,
		Direction:          "request",
		MainnetHeight:      hs.MainnetHeight,
		MainnetBlockHash:   raw,
		ObservedAtUnixMs:   time.Now().UnixMilli(),
		SourceMessage:      source,
		Trust:              v.Trust,
		Tag:                v.Tag,
		OriginatorSenderID: strings.TrimSpace(hs.OriginatorSenderID),
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
		PeerID:             peerID,
		Direction:          "request",
		MainnetHeight:      hs.MainnetHeight,
		MainnetBlockHash:   raw,
		ObservedAtUnixMs:   time.Now().UnixMilli(),
		SourceMessage:      source,
		Trust:              v.Trust,
		OriginatorSenderID: strings.TrimSpace(hs.OriginatorSenderID),
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
		PeerID:             localID,
		Direction:          "response",
		MainnetHeight:      hs.MainnetHeight,
		MainnetBlockHash:   raw,
		ObservedAtUnixMs:   time.Now().UnixMilli(),
		SourceMessage:      source,
		Trust:              heightsync.TrustOracle,
		OriginatorSenderID: strings.TrimSpace(hs.OriginatorSenderID),
	})
	dir := hs.Direction
	if dir == "" {
		dir = "response"
	}
	heightsync.IncOutboundAnchor(dir, s.host.EscrowID(), localID)
}

func decodeMainnetBlockHashHex(s string) ([]byte, error) {
	h := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(s), "0x"), "0X")
	if len(h)%2 == 1 {
		h = "0" + h
	}
	return hex.DecodeString(h)
}

// heightSyncSeedResponse is the JSON body for POST .../height-sync (courier cold-start).
type heightSyncSeedResponse struct {
	HeightSync *heightsync.HeightSyncSection `json:"height_sync,omitempty"`
}

// HandleHeightSync emits one host Anchor (ForceAnchor) for courier cache seeding.
// Requires WithHeightSync and WithHeightSyncSeedRPC(true); escrow owner only.
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
		Nonce:              1,
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
		s.attachResponseOriginSignature(sec, h.Nonce)
		s.logOutboundHeightSync(sec, h.Nonce)
		s.recordOutboundAnchorIfAnchor(sec, c.Request().Method+" "+c.Path())
	}
	return writeJSON(c, http.StatusOK, heightSyncSeedResponse{HeightSync: sec})
}

func (s *Server) attachResponseOriginSignature(sec *heightsync.HeightSyncSection, nonce uint64) {
	if s == nil || sec == nil || s.heightSync == nil {
		return
	}
	_, sig, err := heightsync.SignOrigin(s.host.Signer(), sec)
	if err != nil {
		logging.Debug("heightsync: sign response origin failed",
			heightsync.LogFieldSubsystem, "heightsync",
			"error", err.Error())
		return
	}
	sec.SenderSignature = sig
	if s.heightSyncResponseAfterSignHook != nil {
		s.heightSyncResponseAfterSignHook(sec, nonce)
	}
}

func (s *Server) HandleInference(c echo.Context) error {
	sender, err := getSender(c)
	if err != nil {
		logging.Error("HandleInference", "error", err)
		return echo.NewHTTPError(http.StatusUnauthorized, "missing sender")
	}
	if !s.isOwner(sender) {
		logging.Error("HandleInference", "error", "restricted to escrow owner")
		return echo.NewHTTPError(http.StatusForbidden, "restricted to escrow owner")
	}

	body, err := getBody(c)
	if err != nil {
		logging.Error("HandleInference", "error", err)
		return err
	}

	unwrapped, err := UnwrapInferenceRequestBody(body)
	if err != nil {
		logging.Error("HandleInference", "error", "decode body: "+err.Error())
		return echo.NewHTTPError(http.StatusBadRequest, "decode body: "+err.Error())
	}

	req, err := HostRequestFromJSON(unwrapped.Request)
	if err != nil {
		logging.Error("HandleInference", "error", "decode request: "+err.Error())
		return echo.NewHTTPError(http.StatusBadRequest, "decode request: "+err.Error())
	}

	sessionID := c.Param("id")
	oracleHdr := s.latestOracleHeader(c.Request().Context())
	if s.pendingUntrustedBySession != nil {
		s.reconcilePendingUntrusted(sessionID, oracleHdr)
	}
	inboundVal := s.classifyInboundHeightSync(req.Nonce, unwrapped.HeightSync, oracleHdr)
	if inboundVal.Result == heightsync.ResultInvalidStaleOrigin {
		heightsync.IncStaleOriginRejected()
		logging.Warn("heightsync: invalid inbound anchor",
			heightsync.LogFieldSubsystem, "heightsync",
			heightsync.LogFieldDirection, "request",
			heightsync.LogFieldNonce, req.Nonce,
			heightsync.LogFieldPeerID, sender,
			heightsync.LogFieldReason, inboundVal.Reason,
			heightsync.LogFieldClassification, string(inboundVal.Result),
		)
	}
	s.logInboundHeightSync(sender, sessionID, req.Nonce, unwrapped.HeightSync, oracleHdr, inboundVal)
	s.recordInboundAnchorIfAnchor(sender, unwrapped.HeightSync, c.Request().Method+" "+c.Path(), inboundVal)
	if inboundVal.Result == heightsync.ResultValidAnchor || inboundVal.Result == heightsync.ResultValidLazyAnchor {
		s.notePendingUntrustedInbound(sessionID, sender, unwrapped.HeightSync, oracleHdr)
	}

	resp, err := s.host.HandleRequest(c.Request().Context(), req)
	if err != nil {
		logging.Error("HandleInference", "error", "handle request: "+err.Error())
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	// Run AFTER HandleRequest so any MsgForceHeightSyncTurn carried in the
	// catch-up diff has already been applied and is visible via escrow hints.
	s.recordForceRequestAnchorMissingIfApplicable(sender, req.Nonce, unwrapped.HeightSync, c.Request().Method+" "+c.Path())

	if err := s.waitInferenceResponseHold(c.Request().Context(), req.Nonce); err != nil {
		logging.Debug("HandleInference: response hold ended without SSE",
			"subsystem", "transport", "nonce", req.Nonce, "error", err.Error())
		return err
	}

	// Always SSE response.
	w := c.Response()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	w.Flush()

	// Event 1: receipt + protocol metadata.
	receiptEvent := DevshardReceiptEvent{
		StateSig:    resp.StateSig,
		StateHash:   resp.StateHash,
		Nonce:       resp.Nonce,
		Receipt:     resp.Receipt,
		ConfirmedAt: resp.ConfirmedAt,
	}
	receiptWrapper := map[string]interface{}{"devshard_receipt": receiptEvent}
	if s.heightSync != nil {
		var sessionStart bool
		s.firstRespMu.Lock()
		if !s.firstInferenceResp[sessionID] {
			sessionStart = true
			s.firstInferenceResp[sessionID] = true
		}
		s.firstRespMu.Unlock()

		schedK := s.heightSync.K()
		schedSlots := s.heightSync.SlotsNum()
		escrowH := s.host.HeightSyncEscrowHints(schedK, schedSlots)
		h := heightsync.DecideHints{
			Nonce:              req.Nonce,
			SessionStart:       sessionStart,
			ForceAnchor:        req.ForceHeightSyncAnchor && escrowH == nil,
			Escrow:             escrowH,
			OriginatorSenderID: s.host.Signer().Address(),
			Direction:          "response",
		}
		sec, dErr, oracleMiss := s.heightSync.Decide(c.Request().Context(), h)
		if oracleMiss {
			heightsync.IncOracleFailure(s.host.Signer().Address())
		}
		if dErr != nil {
			logging.Debug("heightsync: outbound anchor error",
				heightsync.LogFieldSubsystem, "heightsync",
				heightsync.LogFieldNonce, req.Nonce,
				"error", dErr.Error())
			s.logOutboundHeightSync(nil, req.Nonce)
		} else if sec != nil {
			sec.Direction = "response"
			s.attachResponseOriginSignature(sec, req.Nonce)
			receiptWrapper["height_sync"] = sec
			s.logOutboundHeightSync(sec, req.Nonce)
			s.recordOutboundAnchorIfAnchor(sec, c.Request().Method+" "+c.Path())
		} else {
			s.logOutboundHeightSync(nil, req.Nonce)
		}
	}
	writeSSEEvent(w, receiptWrapper)

	// Event 2+: inference result.
	// If reconnecting to a completed inference, replay cached response.
	// Otherwise run deferred execution with live streaming.
	if resp.CachedResponseBody != nil && resp.ExecutionJob == nil {
		replaySSEBody(w, resp.CachedResponseBody)
	} else if resp.ExecutionJob != nil {
		resp.ExecutionJob.ResponseWriter = w
		_, execErr := s.host.RunExecution(c.Request().Context(), resp.ExecutionJob)
		if execErr != nil {
			logging.Error("deferred execution failed", "subsystem", "server", "error", execErr)
		}
	}

	// Final event: devshard_meta with updated mempool.
	mempoolTxs := s.host.MempoolTxs()
	mempoolBytes, _ := DevshardTxsToBytes(mempoolTxs)
	metaWrapper := map[string]interface{}{"devshard_meta": DevshardMetaEvent{Mempool: mempoolBytes}}
	writeSSEEvent(w, metaWrapper)

	// Fire gossip in background.
	if s.gossip != nil && resp.StateSig != nil {
		go s.gossip.AfterRequest(context.Background(), resp.Nonce, resp.StateHash, resp.StateSig)
	}
	if s.gossip != nil && resp.StateSig == nil && len(resp.Mempool) > 0 {
		go s.gossip.BroadcastTxs(context.Background(), resp.Mempool)
	}

	return nil
}

// replaySSEBody writes cached ML response bytes as SSE data lines.
// The cached bytes are the raw response body (JSON). Wrap as a single SSE data event.
func replaySSEBody(w http.ResponseWriter, body []byte) {
	fmt.Fprintf(w, "data: %s\n\n", body)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	fmt.Fprintf(w, "data: [DONE]\n\n")
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// writeSSEEvent writes a single SSE data line with JSON payload.
func writeSSEEvent(w http.ResponseWriter, data interface{}) {
	b, err := json.Marshal(data)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "data: %s\n\n", b)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// SetPeerClients sets the executor clients for timeout verification.
// Key is slot index (position in group), value is an ExecutorClient.
func (s *Server) SetPeerClients(peers map[int]*HTTPClient) {
	s.peerClients = peers
}

func (s *Server) HandleVerifyTimeout(c echo.Context) error {
	sender, err := getSender(c)
	if err != nil {
		return err
	}
	if !s.isOwner(sender) {
		return echo.NewHTTPError(http.StatusForbidden, "restricted to escrow owner")
	}

	body, err := getBody(c)
	if err != nil {
		return err
	}

	var req VerifyTimeoutRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid json")
	}

	reason, err := TimeoutReasonFromString(req.Reason)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	// Apply catch-up diffs so the verifier knows about the inference.
	if len(req.Diffs) > 0 {
		diffs := make([]types.Diff, 0, len(req.Diffs))
		for i, dj := range req.Diffs {
			d, dErr := DiffFromJSON(dj)
			if dErr != nil {
				return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("decode diff %d: %v", i, dErr))
			}
			diffs = append(diffs, d)
		}
		s.host.ApplyCatchUpDiffs(diffs)
	}

	st := s.host.SnapshotState()
	localMempool := s.host.MempoolTxs()

	// Determine executor slot from inference_id.
	executorIdx := int(req.InferenceID % uint64(len(s.host.Group())))
	var executorClient host.ExecutorClient
	if s.peerClients != nil {
		if pc, ok := s.peerClients[executorIdx]; ok {
			executorClient = pc
		}
	}

	nowUnix := time.Now().Unix()

	var accept bool
	switch reason {
	case types.TimeoutReason_TIMEOUT_REASON_REFUSED:
		// Fetch stored diffs to forward to executor during challenge.
		var storedDiffs []types.Diff
		if s.store != nil && st.LatestNonce > 0 {
			records, dErr := s.store.GetDiffs(s.host.EscrowID(), 1, st.LatestNonce)
			if dErr == nil {
				storedDiffs = make([]types.Diff, len(records))
				for i, r := range records {
					storedDiffs[i] = r.Diff
				}
			}
		}
		accept, err = host.VerifyRefusedTimeout(c.Request().Context(), st, req.InferenceID, PayloadFromJSON(req.Payload), storedDiffs, localMempool, executorClient, st.Config, nowUnix)
	case types.TimeoutReason_TIMEOUT_REASON_EXECUTION:
		accept, err = host.VerifyExecutionTimeout(c.Request().Context(), st, req.InferenceID, localMempool, executorClient, st.Config, nowUnix)
	default:
		return echo.NewHTTPError(http.StatusBadRequest, "unknown reason")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	resp := VerifyTimeoutResponse{Accept: accept}
	if accept {
		sig, voterSlot, sErr := signTimeoutVote(s.host.EscrowID(), req.InferenceID, reason, s.host.Signer(), s.host.PrimarySlot())
		if sErr != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, sErr.Error())
		}
		resp.Signature = sig
		resp.VoterSlot = voterSlot
	}
	return writeJSON(c, http.StatusOK, resp)
}

// signTimeoutVote marshals and signs a TimeoutVoteContent, returning the
// signature and the voter's slot ID.
func signTimeoutVote(escrowID string, inferenceID uint64, reason types.TimeoutReason, signer signing.Signer, voterSlot uint32) ([]byte, uint32, error) {
	voteContent := &types.TimeoutVoteContent{
		EscrowId:    escrowID,
		InferenceId: inferenceID,
		Reason:      reason,
		Accept:      true,
	}
	voteData, err := proto.Marshal(voteContent)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal vote: %w", err)
	}
	sig, err := signer.Sign(voteData)
	if err != nil {
		return nil, 0, fmt.Errorf("sign vote: %w", err)
	}
	return sig, voterSlot, nil
}

func (s *Server) HandleChallengeReceipt(c echo.Context) error {
	sender, err := getSender(c)
	if err != nil {
		return err
	}
	if !s.isOwner(sender) && !s.isGroupMember(sender) {
		return echo.NewHTTPError(http.StatusForbidden, "restricted to escrow owner or group member")
	}

	body, err := getBody(c)
	if err != nil {
		return err
	}

	var req ChallengeReceiptRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid json")
	}

	diffs := make([]types.Diff, len(req.Diffs))
	for i, dj := range req.Diffs {
		d, err := DiffFromJSON(dj)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("decode diff %d: %v", i, err))
		}
		diffs[i] = d
	}

	receipt, _, err := s.host.ChallengeReceipt(c.Request().Context(), req.InferenceID, PayloadFromJSON(req.Payload), diffs)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	return writeJSON(c, http.StatusOK, ChallengeReceiptResponse{Receipt: receipt})
}

func (s *Server) HandleGossipNonce(c echo.Context) error {
	// Gossip is host-to-host only. Reject user-signed requests.
	sender, err := getSender(c)
	if err != nil {
		return err
	}
	if !s.isGroupMember(sender) {
		return echo.NewHTTPError(http.StatusForbidden, "gossip restricted to group members")
	}

	body, err := getBody(c)
	if err != nil {
		return err
	}

	var req GossipNonceRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid json")
	}

	// Reject empty sig or invalid slot upfront. Without this, an attacker
	// can poison the seen map with a fake (nonce, hash) and cause false
	// equivocation detection against an honest host.
	if len(req.StateSig) == 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "missing state signature")
	}
	if req.SlotID >= uint32(len(s.host.Group())) {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid slot id")
	}

	// Verify stateSig recovers to the claimed slot's address.
	// SlotIDs are compact 0..len(group)-1 so direct index is safe after bounds check above.
	expectedAddr := s.host.Group()[req.SlotID].ValidatorAddress

	sigContent := &types.StateSignatureContent{
		StateRoot: req.StateHash,
		EscrowId:  s.host.EscrowID(),
		Nonce:     req.Nonce,
	}
	sigData, err := proto.Marshal(sigContent)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "marshal sig content")
	}
	addr, err := s.verifier.RecoverAddress(sigData, req.StateSig)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid gossip state signature")
	}
	if addr != expectedAddr {
		if !s.host.IsWarmKeyForSlot(addr, req.SlotID) {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid gossip state signature")
		}
	}

	if s.gossip != nil {
		if err := s.gossip.OnNonceReceived(req.Nonce, req.StateHash, req.StateSig, req.SlotID); err != nil {
			return echo.NewHTTPError(http.StatusConflict, err.Error())
		}
	}

	// Accumulate sig directly if the host has this nonce backed.
	if err := s.host.AccumulateGossipSig(req.Nonce, req.StateHash, req.StateSig, req.SlotID); err != nil {
		logging.Debug("accumulate gossip sig skipped", "subsystem", "server", "nonce", req.Nonce, "error", err)
	}

	return c.NoContent(http.StatusOK)
}

func (s *Server) HandleGossipTxs(c echo.Context) error {
	// Gossip is host-to-host only.
	sender, err := getSender(c)
	if err != nil {
		return err
	}
	if !s.isGroupMember(sender) {
		return echo.NewHTTPError(http.StatusForbidden, "gossip restricted to group members")
	}

	body, err := getBody(c)
	if err != nil {
		return err
	}

	var req GossipTxsRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid json")
	}

	if s.gossip != nil {
		txs, err := DevshardTxsFromBytes(req.Txs)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "decode txs: "+err.Error())
		}
		s.gossip.OnTxsReceived(txs)
	}

	return c.NoContent(http.StatusOK)
}

func (s *Server) HandleGetSignatures(c echo.Context) error {
	nonceStr := c.QueryParam("nonce")
	if nonceStr == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "missing 'nonce' parameter")
	}
	nonce, err := strconv.ParseUint(nonceStr, 10, 64)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid 'nonce' parameter")
	}

	sigs, err := s.host.GetSignatures(nonce)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	return writeJSON(c, http.StatusOK, SignaturesResponse{Signatures: sigs})
}

func (s *Server) HandleGetDiffs(c echo.Context) error {
	if s.store == nil {
		return echo.NewHTTPError(http.StatusNotFound, "no storage configured")
	}

	fromStr := c.QueryParam("from")
	toStr := c.QueryParam("to")

	from, err := strconv.ParseUint(fromStr, 10, 64)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid 'from' parameter")
	}
	to, err := strconv.ParseUint(toStr, 10, 64)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid 'to' parameter")
	}

	records, err := s.store.GetDiffs(s.host.EscrowID(), from, to)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	// Convert to JSON-friendly format.
	type diffRecordJSON struct {
		DiffJSON  `json:"diff"`
		StateHash []byte `json:"state_hash"`
	}

	result := make([]diffRecordJSON, len(records))
	for i, rec := range records {
		dj, err := DiffToJSON(rec.Diff)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, fmt.Sprintf("encode diff %d: %v", rec.Nonce, err))
		}
		result[i] = diffRecordJSON{DiffJSON: dj, StateHash: rec.StateHash}
	}

	return writeJSON(c, http.StatusOK, result)
}

func (s *Server) HandleGetMempool(c echo.Context) error {
	txs := s.host.MempoolTxs()
	data, err := DevshardTxsToBytes(txs)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return writeJSON(c, http.StatusOK, map[string]interface{}{"txs": data})
}
