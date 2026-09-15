package transport

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	json "github.com/goccy/go-json"
	"google.golang.org/protobuf/proto"

	"github.com/labstack/echo/v4"

	"common/chainoracle/blocks"
	"devshard"
	"devshard/bridge"
	"devshard/gossip"
	"devshard/heightsync"
	"devshard/host"
	"devshard/logging"
	"devshard/observability"
	"devshard/signing"
	"devshard/storage"
	"devshard/types"
)

const (
	contextKeySender = "devshard_sender"

	// DefaultMaxBodySize bounds the decompressed body auth reads.
	// The transport enforces actual bytes read, so it also covers chunked bodies
	// and deployments that expose devshardd without the public proxy.
	DefaultMaxBodySize int64 = 10 * 1024 * 1024
)

const (
	// DefaultRPCReadMaxBytes is the Connect read cap for handshake and
	// ordinary unaries (Attach, Watch, GetSignatures, gossip Nonce).
	// Matches the Connect mux request cap for those methods. Chat stays
	// DefaultMaxBodySize. GetPayload client reads default to
	// DefaultRPCPayloadMaxBytes; the handler send cap is
	// DefaultRPCPayloadSendMaxBytes.
	// Dispute unaries and Gossip Txs use DefaultRPCLargeReadMaxBytes.
	// GetDiffs / GetMempool responses use DefaultRPCQueryReadMaxBytes.
	DefaultRPCReadMaxBytes = 16 << 10

	// DefaultRPCQueryReadMaxBytes is the Connect client read cap for
	// GetDiffs and GetMempool responses. Same floor as JSON POSTs
	// (DefaultMaxBodySize). Catch-up batches exceed the 16 KiB unary cap;
	// those requests stay on DefaultRPCReadMaxBytes at the mux. HTTP GET
	// diffs/mempool is still unbounded (io.ReadAll); Connect needs a cap.
	DefaultRPCQueryReadMaxBytes = 10 << 20

	// DefaultRPCLargeReadMaxBytes is the Connect read cap for dispute
	// unaries (VerifyTimeout, VerifyErrorMiss, ChallengeReceipt) and Gossip
	// Txs. Same floor as JSON POSTs (DefaultMaxBodySize): those bodies
	// carry prompts, response payloads, and tx batches.
	DefaultRPCLargeReadMaxBytes = 10 << 20

	// DefaultRPCPayloadMaxBytes is the Connect GetPayload **client** read
	// cap when the caller did not pass a per-inference limit. Matches
	// validation.MaxPayloadResponseBytes. Live fetches pass
	// PayloadReadLimit(PayloadResponseByteLimit(outputTokens)). Request
	// bodies stay DefaultMaxBodySize (10 MiB): GetPayloadRequest is tiny.
	DefaultRPCPayloadMaxBytes = 64 << 20

	// DefaultRPCPayloadSendMaxBytes is the PayloadService handler send cap.
	// Matches validation.MaxPayloadResponseBytesHard so an honest large job
	// is not cut off at 64 MiB. Clients still apply PayloadReadLimit.
	DefaultRPCPayloadSendMaxBytes = 512 << 20
)

var (
	// ErrNoStorage is GET diffs when the host has no store.
	ErrNoStorage = errors.New("no storage configured")
	// ErrGossipMissingStateSig is GossipNonce without a state signature.
	ErrGossipMissingStateSig = errors.New("missing state signature")
	// ErrGossipInvalidSlot is GossipNonce with a slot outside the group.
	ErrGossipInvalidSlot = errors.New("invalid slot id")
	// ErrGossipInvalidStateSig is a state signature that does not recover
	// to the claimed slot (or a warm key for that slot).
	ErrGossipInvalidStateSig = errors.New("invalid gossip state signature")
	// ErrHeightSyncSeedDisabled is POST height-sync when the seed RPC is off.
	ErrHeightSyncSeedDisabled = errors.New("height-sync seed RPC disabled")
	// ErrInvalidRequesterSlot is repair with requester_slot past the group.
	ErrInvalidRequesterSlot = errors.New("invalid requester_slot")
	// ErrRequesterSlotMismatch is repair signed for a slot the sender does not own.
	ErrRequesterSlotMismatch = errors.New("requester_slot does not match sender")
	errGossipMarshal         = errors.New("marshal sig content")
)

// Server wraps a host.Host and exposes it over HTTP via Echo.
type Server struct {
	host         *host.Host
	store        storage.Storage
	gossip       *gossip.Gossip // nil until gossip is wired
	verifier     signing.Verifier
	userAddr     string                 // session user address, allowed alongside group members
	peerClients  map[int]HostPeerClient // slot index -> client, for timeout verification
	rateLimit    *rateLimiter           // nil = no limiting
	maxBodySize  int64                  // max request body bytes, 0 = no limit
	bridge       bridge.MainnetBridge   // optional, for warm key verification
	receiptDelay time.Duration          // optional test hook before receipt SSE write

	heightSync          *heightsync.AnchorScheduler
	heightSyncLogOracle blocks.BlockOracle
	heightSyncAudit     *heightsync.AuditRing
	heightSyncMarks     *heightsync.MarkLog
	heightSyncSeedRPC   bool

	pendingUntrustedMu        sync.Mutex
	pendingUntrustedBySession map[string]*pendingUntrustedTip

	heightSyncResponseAfterSignHook func(sec *heightsync.HeightSyncSection, nonce uint64)
	heightSyncOriginSigner          signing.Signer // test seam; nil uses host.Signer()

	holdInferenceMu    sync.Mutex
	holdInferenceGate  chan struct{} // closed to release; non-nil while armed
	holdInferenceArmed bool
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
func WithServerPeerClients(peers map[int]HostPeerClient) ServerOption {
	return func(s *Server) {
		s.peerClients = peers
		if s.host != nil {
			s.host.SetRepairProbe(s.RepairProbe)
		}
	}
}

// WithBridge sets the bridge for warm key verification in transport auth.
func WithBridge(b bridge.MainnetBridge) ServerOption {
	return func(s *Server) { s.bridge = b }
}

// WithReceiptDelay delays the initial receipt SSE event. It is intended for
// integration tests that need to observe pre-receipt gateway timeout paths.
func WithReceiptDelay(delay time.Duration) ServerOption {
	return func(s *Server) { s.receiptDelay = delay }
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
		host:        h,
		store:       store,
		verifier:    verifier,
		userAddr:    userAddr,
		maxBodySize: DefaultMaxBodySize,
	}
	for _, o := range opts {
		o(s)
	}
	return s, nil
}

// Host returns the underlying host.Host.
func (s *Server) Host() *host.Host { return s.host }

// PeerClients is the slot→client roster SetPeerClients stored. Includes this
// host's slot so timeout verify can reach the executor when it is us.
func (s *Server) PeerClients() map[int]HostPeerClient { return s.peerClients }

// Gossip is the outbound nonce/tx propagator, or nil if unwired.
func (s *Server) Gossip() *gossip.Gossip { return s.gossip }

// CloseOutbound stops gossip and releases RPC PeerConns. Host.Close is
// separate: call this before or with Host.Close on session teardown.
func (s *Server) CloseOutbound() {
	if s == nil {
		return
	}
	if s.gossip != nil {
		s.gossip.Stop()
		s.gossip = nil
	}
	for _, pc := range s.peerClients {
		if pc != nil {
			pc.Close()
		}
	}
	s.peerClients = nil
}

// SetGossip attaches a gossip instance for nonce/tx propagation.
func (s *Server) SetGossip(g *gossip.Gossip) { s.gossip = g }

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

// startHandlerSpan opens an internal observability span for a handler and
// updates the request context so downstream code inherits it. The returned
// closure must be deferred to finalize the span with the handler's error.
func startHandlerSpan(c echo.Context, handlerName string) (*observability.Operation, func(*error)) {
	sessionID := c.Param("id")
	req := c.Request()
	ctx, op := observability.Request.StartHandler(req.Context(), handlerName, sessionID)
	c.SetRequest(req.WithContext(ctx))

	route := c.Path()
	if route == "" {
		route = req.URL.Path
	}
	observability.Request.SetHTTPRequest(op, req.Method, route, req.URL.RequestURI(), req.RemoteAddr, req.ContentLength)

	return op, func(errPtr *error) {
		op.FinishErr(errPtr)
	}
}

// AllowsSender reports whether addr is the session user, a group member,
// or a verified warm key for any group member.
func (s *Server) AllowsSender(addr string) bool {
	return s.isAllowedSender(addr)
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

// IsOwner reports whether addr is the escrow creator for this session.
func (s *Server) IsOwner(addr string) bool {
	return s.isOwner(addr)
}

// InjectAuthContext stores a verified sender and request body for handlers
// that run after auth (HandleInference, gossip, etc.).
func InjectAuthContext(c echo.Context, sender string, body []byte) {
	c.Set(contextKeySender, sender)
	c.Set("body", body)
}

// VerifyPOSTAuth reads signature headers and body, verifies the signature, and
// returns the recovered sender address and body. It does not check group
// membership or ownership — callers enforce that.
func VerifyPOSTAuth(c echo.Context, verifier signing.Verifier, escrowID string, maxBodySize int64) (string, []byte, error) {
	sigHex := c.Request().Header.Get(HeaderSignature)
	tsStr := c.Request().Header.Get(HeaderTimestamp)
	if sigHex == "" || tsStr == "" {
		return "", nil, echo.NewHTTPError(http.StatusUnauthorized, "missing auth headers")
	}

	sig, err := hex.DecodeString(sigHex)
	if err != nil {
		return "", nil, echo.NewHTTPError(http.StatusUnauthorized, "invalid signature hex")
	}

	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return "", nil, echo.NewHTTPError(http.StatusUnauthorized, "invalid timestamp")
	}

	if maxBodySize > 0 {
		c.Request().Body = http.MaxBytesReader(c.Response(), c.Request().Body, maxBodySize)
	}

	body, err := io.ReadAll(c.Request().Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			return "", nil, echo.NewHTTPError(
				http.StatusRequestEntityTooLarge,
				fmt.Sprintf("request body exceeds %d bytes", maxBytesErr.Limit),
			)
		}
		return "", nil, echo.NewHTTPError(http.StatusBadRequest, "read body")
	}

	addr, err := VerifyRequest(verifier, escrowID, body, sig, ts, time.Now().Unix())
	if err != nil {
		return "", nil, echo.NewHTTPError(http.StatusUnauthorized, err.Error())
	}
	return addr, body, nil
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

// IsGroupMember reports whether addr is a group member or a warm key for one.
func (s *Server) IsGroupMember(addr string) bool {
	return s.isGroupMember(addr)
}

// AuthMiddleware reads the body, verifies the signature, checks group membership,
// and stores the sender address in the echo context.
// GET requests skip auth intentionally (public observability).
func (s *Server) AuthMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if c.Request().Method == http.MethodGet {
			return next(c)
		}

		addr, body, err := VerifyPOSTAuth(c, s.verifier, s.host.EscrowID(), s.maxBodySize)
		if err != nil {
			return err
		}

		if !s.isAllowedSender(addr) {
			return echo.NewHTTPError(http.StatusForbidden, "sender not in group")
		}

		InjectAuthContext(c, addr, body)
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

func (s *Server) HandleInference(c echo.Context) (err error) {
	sessionID := c.Param("id")
	ctx, op := observability.Request.StartInference(c.Request().Context(), sessionID, "")
	c.SetRequest(c.Request().WithContext(ctx))
	defer op.FinishErr(&err)
	doneInflight := observability.IncInflight(observability.StageRequest)
	defer doneInflight()

	route := c.Path()
	if route == "" {
		route = c.Request().URL.Path
	}
	observability.Request.SetHTTPRequest(op, c.Request().Method, route, c.Request().URL.RequestURI(), c.Request().RemoteAddr, c.Request().ContentLength)
	observability.Request.SetEscrowID(op, s.host.EscrowID())

	sender, err := getSender(c)
	if err != nil {
		return observability.FailNoReceipt(ctx, s.host.EscrowID(),
			observability.ReasonMissingSender, observability.WhereTransportHandleInference,
			"HandleInference: missing sender", echo.NewHTTPError(http.StatusUnauthorized, "missing sender"))
	}
	observability.Request.SetSender(op, sender)
	if !s.isOwner(sender) {
		return observability.FailNoReceipt(ctx, s.host.EscrowID(),
			observability.ReasonOwnerErr, observability.WhereTransportHandleInference,
			"HandleInference: restricted to escrow owner", echo.NewHTTPError(http.StatusForbidden, "restricted to escrow owner"))
	}

	body, err := getBody(c)
	if err != nil {
		return observability.FailNoReceipt(ctx, s.host.EscrowID(),
			observability.ReasonBodyReadErr, observability.WhereTransportHandleInference,
			"HandleInference: read body", err)
	}
	observability.Request.SetInferenceBodyBytes(op, len(body))

	return s.ServeInference(ctx, InferenceCall{
		SessionID: sessionID,
		Sender:    sender,
		Body:      body,
		Source:    c.Request().Method + " " + c.Path(),
		Evidence:  requestLegEvidenceFromContext(c, s.host.EscrowID()),
		Sink:      c.Response(),
		Op:        op,
		OnStreamStart: func() {
			w := c.Response()
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			w.WriteHeader(http.StatusOK)
			w.Flush()
		},
	})
}

// replaySSEBody writes cached ML response bytes as SSE data lines.
// The cached bytes are the raw response body (JSON). Wrap as a single SSE data event.
func replaySSEBody(w http.ResponseWriter, body []byte) error {
	if _, err := fmt.Fprintf(w, "data: %s\n\n", body); err != nil {
		return err
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	if _, err := fmt.Fprintf(w, "data: [DONE]\n\n"); err != nil {
		return err
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}

// writeSSEEvent writes a single SSE data line with JSON payload.
func writeSSEEvent(w http.ResponseWriter, data interface{}) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
		return err
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}

// RateLimitMiddleware returns per-sender rate limiting for authenticated POST
// routes. Install inside AuthMiddleware so contextKeySender is set first.
// recordChatTerminal=true records throttled chat requests as terminal outcomes.
func (s *Server) RateLimitMiddleware(recordChatTerminal bool) echo.MiddlewareFunc {
	if s.rateLimit == nil {
		return func(next echo.HandlerFunc) echo.HandlerFunc { return next }
	}
	return rateLimitMiddleware(s.rateLimit, recordChatTerminal)
}

// SetPeerClients sets the executor clients for timeout verification and
// the slot→URL map reused by repair probes. Store host-signed
// SelectTransport results so RPC Attach identity matches gossip and
// RepairProbe CloneWithSigner is identity-preserving.
func (s *Server) SetPeerClients(peers map[int]HostPeerClient) {
	s.peerClients = peers
	if s.host != nil {
		s.host.SetRepairProbe(s.RepairProbe)
	}
}

// CloseReadyView is the §12 producer on the hosted session.
func (s *Server) CloseReadyView() heightsync.CloseReadyView {
	if s.host == nil {
		return nil
	}
	return s.host.CloseReadyView()
}

func (s *Server) HandleVerifyTimeout(c echo.Context) (err error) {
	op, finish := startHandlerSpan(c, "verify_timeout")
	defer finish(&err)

	sender, err := getSender(c)
	if err != nil {
		return err
	}
	observability.Request.SetSender(op, sender)
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

	resp, err := s.ServeVerifyTimeout(c.Request().Context(), req)
	if err != nil {
		return mapVerifyHTTP(c, err)
	}
	return writeJSON(c, http.StatusOK, resp)
}

// ServeVerifyTimeout is the transport-neutral core behind POST .../verify-timeout
// and SessionService.VerifyTimeout. Callers enforce owner-only.
func (s *Server) ServeVerifyTimeout(ctx context.Context, req VerifyTimeoutRequest) (*VerifyTimeoutResponse, error) {
	if !s.host.CompletionRequestsEnabled() {
		logging.Debug("ServeVerifyTimeout: devshard_requests_enabled=false", "subsystem", "server")
		return nil, devshard.ErrRequestsDisabled
	}

	reason, err := TimeoutReasonFromString(req.Reason)
	if err != nil {
		return nil, clientRequest(err.Error())
	}

	if len(req.Diffs) > 0 {
		diffs, dErr := decodeDiffsJSON(req.Diffs)
		if dErr != nil {
			return nil, dErr
		}
		s.host.ApplyCatchUpDiffs(diffs)
	}

	st := s.host.SnapshotState()
	localMempool := s.host.MempoolTxs()

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
		accept, err = host.VerifyRefusedTimeout(ctx, st, req.InferenceID, PayloadFromJSON(req.Payload), storedDiffs, localMempool, executorClient, s.host, st.Config, nowUnix)
	case types.TimeoutReason_TIMEOUT_REASON_EXECUTION:
		accept, err = host.VerifyExecutionTimeout(ctx, st, req.InferenceID, localMempool, executorClient, st.Config, nowUnix)
	default:
		return nil, clientRequest(fmt.Sprintf("unknown timeout reason: %s", req.Reason))
	}
	if err != nil {
		return nil, err
	}

	resp := &VerifyTimeoutResponse{Accept: accept}
	if accept {
		sig, voterSlot, sErr := signTimeoutVote(s.host.EscrowID(), req.InferenceID, reason, s.host.Signer(), s.host.PrimarySlot())
		if sErr != nil {
			return nil, sErr
		}
		resp.Signature = sig
		resp.VoterSlot = voterSlot
	} else {
		mempoolBytes, mErr := DevshardTxsToBytes(host.RecoveryTxsFor(s.host.MempoolTxs(), req.InferenceID))
		if mErr != nil {
			return nil, mErr
		}
		resp.Mempool = mempoolBytes
	}
	return resp, nil
}

// signTimeoutVote marshals and signs a TimeoutVoteContent, returning the
// signature and the voter's slot ID. Timeout votes are refused/execution only.
func signTimeoutVote(escrowID string, inferenceID uint64, reason types.TimeoutReason, signer signing.Signer, voterSlot uint32) ([]byte, uint32, error) {
	voteContent := &types.TimeoutVoteContent{
		EscrowId:    escrowID,
		InferenceId: inferenceID,
		Reason:      reason,
		Accept:      true,
	}
	voteData, err := proto.MarshalOptions{Deterministic: true}.Marshal(voteContent)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal vote: %w", err)
	}
	sig, err := signer.Sign(voteData)
	if err != nil {
		return nil, 0, fmt.Errorf("sign vote: %w", err)
	}
	return sig, voterSlot, nil
}

func signErrorMissVote(escrowID string, inferenceID uint64, signer signing.Signer, voterSlot uint32, responseHash []byte) ([]byte, uint32, error) {
	voteContent := &types.ErrorMissVoteContent{
		EscrowId:     escrowID,
		InferenceId:  inferenceID,
		Accept:       true,
		ResponseHash: responseHash,
	}
	voteData, err := proto.MarshalOptions{Deterministic: true}.Marshal(voteContent)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal vote: %w", err)
	}
	sig, err := signer.Sign(voteData)
	if err != nil {
		return nil, 0, fmt.Errorf("sign vote: %w", err)
	}
	return sig, voterSlot, nil
}

func (s *Server) HandleVerifyErrorMiss(c echo.Context) (err error) {
	op, finish := startHandlerSpan(c, "verify_error_miss")
	defer finish(&err)

	sender, err := getSender(c)
	if err != nil {
		return err
	}
	observability.Request.SetSender(op, sender)
	if !s.isOwner(sender) {
		return echo.NewHTTPError(http.StatusForbidden, "restricted to escrow owner")
	}

	body, err := getBody(c)
	if err != nil {
		return err
	}

	var req VerifyErrorMissRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid json")
	}

	resp, err := s.ServeVerifyErrorMiss(c.Request().Context(), req)
	if err != nil {
		return mapVerifyHTTP(c, err)
	}
	return writeJSON(c, http.StatusOK, resp)
}

// ServeVerifyErrorMiss is the transport-neutral core behind POST .../verify-error-miss
// and SessionService.VerifyErrorMiss. Callers enforce owner-only.
func (s *Server) ServeVerifyErrorMiss(ctx context.Context, req VerifyErrorMissRequest) (*VerifyErrorMissResponse, error) {
	_ = ctx
	if !s.host.CompletionRequestsEnabled() {
		logging.Debug("ServeVerifyErrorMiss: devshard_requests_enabled=false", "subsystem", "server")
		return nil, devshard.ErrRequestsDisabled
	}

	if len(req.Diffs) > 0 {
		diffs, dErr := decodeDiffsJSON(req.Diffs)
		if dErr != nil {
			return nil, dErr
		}
		s.host.ApplyCatchUpDiffs(diffs)
	}

	st := s.host.SnapshotState()
	localMempool := s.host.MempoolTxs()
	accept, responseHash, rejectCause, err := host.VerifyErrorMiss(st, req.InferenceID, req.FinishTx, req.ResponsePayload, localMempool, s.host)
	if err != nil {
		return nil, err
	}

	resp := &VerifyErrorMissResponse{Accept: accept, RejectCause: rejectCause}
	if accept {
		sig, voterSlot, sErr := signErrorMissVote(s.host.EscrowID(), req.InferenceID, s.host.Signer(), s.host.PrimarySlot(), responseHash)
		if sErr != nil {
			return nil, sErr
		}
		resp.Signature = sig
		resp.VoterSlot = voterSlot
	} else {
		mempoolBytes, mErr := DevshardTxsToBytes(host.RecoveryTxsFor(s.host.MempoolTxs(), req.InferenceID))
		if mErr != nil {
			return nil, mErr
		}
		resp.Mempool = mempoolBytes
	}
	return resp, nil
}

func (s *Server) HandleChallengeReceipt(c echo.Context) (err error) {
	op, finish := startHandlerSpan(c, "challenge_receipt")
	defer finish(&err)

	sender, err := getSender(c)
	if err != nil {
		return err
	}
	observability.Request.SetSender(op, sender)
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
	observability.Request.SetInferenceID(op, req.InferenceID)
	observability.Request.SetDiffsCount(op, len(req.Diffs))

	resp, err := s.ServeChallengeReceipt(c.Request().Context(), req)
	if err != nil {
		return mapVerifyHTTP(c, err)
	}
	return writeJSON(c, http.StatusOK, resp)
}

// ServeChallengeReceipt is the transport-neutral core behind POST .../challenge-receipt
// and SessionService.ChallengeReceipt. Callers enforce owner-or-group.
func (s *Server) ServeChallengeReceipt(ctx context.Context, req ChallengeReceiptRequest) (*ChallengeReceiptResponse, error) {
	diffs, err := decodeDiffsJSON(req.Diffs)
	if err != nil {
		return nil, err
	}

	receipt, _, err := s.host.ChallengeReceipt(ctx, req.InferenceID, PayloadFromJSON(req.Payload), diffs)
	if err != nil {
		return nil, err
	}

	mempoolBytes, err := DevshardTxsToBytes(host.RecoveryTxsFor(s.host.MempoolTxs(), req.InferenceID))
	if err != nil {
		return nil, err
	}
	return &ChallengeReceiptResponse{Receipt: receipt, Mempool: mempoolBytes}, nil
}

func mapVerifyHTTP(c echo.Context, err error) error {
	if errors.Is(err, devshard.ErrRequestsDisabled) {
		return HTTPError(c, http.StatusServiceUnavailable, DevshardErrorRequestsDisabled, devshard.ErrRequestsDisabled.Error())
	}
	var cre *clientRequestError
	if errors.As(err, &cre) {
		return echo.NewHTTPError(http.StatusBadRequest, cre.Error())
	}
	return echo.NewHTTPError(http.StatusInternalServerError, err.Error()).SetInternal(err)
}

type clientRequestError struct{ msg string }

func (e *clientRequestError) Error() string { return e.msg }

func clientRequest(msg string) error {
	return &clientRequestError{msg: msg}
}

// IsClientRequest is a malformed request the HTTP path maps to 400.
func IsClientRequest(err error) bool {
	var cre *clientRequestError
	return errors.As(err, &cre)
}

func decodeDiffsJSON(djs []DiffJSON) ([]types.Diff, error) {
	if len(djs) == 0 {
		return nil, nil
	}
	diffs := make([]types.Diff, 0, len(djs))
	for i, dj := range djs {
		d, err := DiffFromJSON(dj)
		if err != nil {
			return nil, clientRequest(fmt.Sprintf("decode diff %d: %v", i, err))
		}
		diffs = append(diffs, d)
	}
	return diffs, nil
}

func (s *Server) HandleGossipNonce(c echo.Context) (err error) {
	op, finish := startHandlerSpan(c, "gossip_nonce")
	defer finish(&err)

	sender, err := getSender(c)
	if err != nil {
		return err
	}
	observability.Request.SetSender(op, sender)
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
	observability.Request.SetNonce(op, req.Nonce)
	observability.Request.SetSlotID(op, req.SlotID)
	observability.Request.SetStateHash(op, hex.EncodeToString(req.StateHash))

	if err := s.ServeGossipNonce(req); err != nil {
		return mapGossipHTTP(err)
	}
	return c.NoContent(http.StatusOK)
}

// ServeGossipNonce is the transport-neutral core behind POST .../gossip/nonce
// and GossipService.Nonce. Callers enforce group membership.
func (s *Server) ServeGossipNonce(req GossipNonceRequest) error {
	if len(req.StateSig) == 0 {
		return ErrGossipMissingStateSig
	}
	if req.SlotID >= uint32(len(s.host.Group())) {
		return ErrGossipInvalidSlot
	}

	expectedAddr := s.host.Group()[req.SlotID].ValidatorAddress

	sigContent := &types.StateSignatureContent{
		StateRoot: req.StateHash,
		EscrowId:  s.host.EscrowID(),
		Nonce:     req.Nonce,
	}
	sigData, err := proto.Marshal(sigContent)
	if err != nil {
		return fmt.Errorf("%w: %v", errGossipMarshal, err)
	}
	addr, err := s.verifier.RecoverAddress(sigData, req.StateSig)
	if err != nil {
		return ErrGossipInvalidStateSig
	}
	if addr != expectedAddr {
		if !s.host.IsWarmKeyForSlot(addr, req.SlotID) {
			return ErrGossipInvalidStateSig
		}
	}

	if s.gossip != nil {
		if err := s.gossip.OnNonceReceived(req.Nonce, req.StateHash, req.StateSig, req.SlotID); err != nil {
			return err
		}
	}

	if err := s.host.AccumulateGossipSig(req.Nonce, req.StateHash, req.StateSig, req.SlotID); err != nil {
		logging.Debug("accumulate gossip sig skipped", "subsystem", "server", "nonce", req.Nonce, "error", err)
	}
	return nil
}

func (s *Server) HandleGossipTxs(c echo.Context) (err error) {
	op, finish := startHandlerSpan(c, "gossip_txs")
	defer finish(&err)

	sender, err := getSender(c)
	if err != nil {
		return err
	}
	observability.Request.SetSender(op, sender)
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
	observability.Request.SetGossipTxsBytes(op, len(req.Txs))

	txs, err := DevshardTxsFromBytes(req.Txs)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "decode txs: "+err.Error())
	}
	observability.Request.SetGossipTxsCount(op, len(txs))
	s.ServeGossipTxs(txs)
	return c.NoContent(http.StatusOK)
}

// ServeGossipTxs is the transport-neutral core behind POST .../gossip/txs
// and GossipService.Txs. Callers enforce group membership and decode txs.
func (s *Server) ServeGossipTxs(txs []*types.DevshardTx) {
	if s.gossip != nil {
		s.gossip.OnTxsReceived(txs)
	}
}

func mapGossipHTTP(err error) error {
	switch {
	case errors.Is(err, ErrGossipMissingStateSig), errors.Is(err, ErrGossipInvalidSlot), errors.Is(err, ErrGossipInvalidStateSig):
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	case errors.Is(err, errGossipMarshal):
		return echo.NewHTTPError(http.StatusInternalServerError, "marshal sig content")
	default:
		return echo.NewHTTPError(http.StatusConflict, err.Error())
	}
}

func (s *Server) HandleGetSignatures(c echo.Context) (err error) {
	op, finish := startHandlerSpan(c, "get_signatures")
	defer finish(&err)

	nonceStr := c.QueryParam("nonce")
	if nonceStr == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "missing 'nonce' parameter")
	}
	nonce, err := strconv.ParseUint(nonceStr, 10, 64)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid 'nonce' parameter")
	}
	observability.Request.SetNonce(op, nonce)

	sigs, err := s.ServeGetSignatures(nonce)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	observability.Request.SetSignaturesReturned(op, len(sigs))

	return writeJSON(c, http.StatusOK, SignaturesResponse{Signatures: sigs})
}

// ServeGetSignatures is the transport-neutral core behind GET .../signatures
// and SessionService.GetSignatures.
func (s *Server) ServeGetSignatures(nonce uint64) (map[uint32][]byte, error) {
	return s.host.GetSignatures(nonce)
}

func (s *Server) HandleGetDiffs(c echo.Context) (err error) {
	op, finish := startHandlerSpan(c, "get_diffs")
	defer finish(&err)

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
	observability.Request.SetDiffsRange(op, from, to)

	records, err := s.ServeGetDiffs(from, to)
	if err != nil {
		if errors.Is(err, ErrNoStorage) {
			return echo.NewHTTPError(http.StatusNotFound, err.Error())
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	observability.Request.SetDiffsReturned(op, len(records))

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

// ServeGetDiffs is the transport-neutral core behind GET .../diffs
// and SessionService.GetDiffs.
func (s *Server) ServeGetDiffs(from, to uint64) ([]types.DiffRecord, error) {
	if s.store == nil {
		return nil, ErrNoStorage
	}
	return s.store.GetDiffs(s.host.EscrowID(), from, to)
}

func (s *Server) HandleGetMempool(c echo.Context) (err error) {
	op, finish := startHandlerSpan(c, "get_mempool")
	defer finish(&err)

	txs, err := s.ServeGetMempool(c.Request().Context())
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	observability.Request.SetMempoolSize(op, len(txs))
	data, err := DevshardTxsToBytes(txs)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	observability.Request.SetResponseContentLength(op, len(data))
	return writeJSON(c, http.StatusOK, map[string]interface{}{"txs": data})
}

// ServeGetMempool is the transport-neutral core behind GET .../mempool
// and SessionService.GetMempool.
func (s *Server) ServeGetMempool(ctx context.Context) ([]*types.DevshardTx, error) {
	if catchErr := s.host.CatchUpFromStore(ctx); catchErr != nil {
		logging.Debug("get_mempool catch-up from store failed",
			"subsystem", "transport",
			"escrow_id", s.host.EscrowID(),
			"error", catchErr)
	}
	s.host.EnqueueDueValidations()
	return s.host.MempoolTxs(), nil
}
