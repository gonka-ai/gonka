package transport

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	json "github.com/goccy/go-json"
	"google.golang.org/protobuf/proto"

	"github.com/labstack/echo/v4"

	"devshard"
	"devshard/bridge"
	"devshard/gossip"
	"devshard/host"
	"devshard/logging"
	"devshard/observability"
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
		host:     h,
		store:    store,
		verifier: verifier,
		userAddr: userAddr,
	}
	for _, o := range opts {
		o(s)
	}
	return s, nil
}

// Host returns the underlying host.Host.
func (s *Server) Host() *host.Host { return s.host }

// SetGossip attaches a gossip instance for nonce/tx propagation.
func (s *Server) SetGossip(g *gossip.Gossip) { s.gossip = g }

// Register mounts all devshard routes on the given echo group.
// The caller typically mounts this under /v1/devshard.
func (s *Server) Register(g *echo.Group) {
	g.Use(observability.EchoMiddleware())
	g.Use(observability.RequestIDMiddleware)
	g.Use(s.AuthMiddleware)
	if s.rateLimit != nil {
		g.Use(rateLimitMiddleware(s.rateLimit, true))
	}
	g.POST("/sessions/:id/chat/completions", s.HandleInference)
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

// startHandlerSpan opens an internal observability span for a handler and
// updates the request context so downstream code inherits it. The returned
// closure must be deferred to finalize the span with the handler's error.
//
// Auto-attached attributes: http.method, http.route, http.target, peer.address,
// http.request_content_length, devshard.handler. The returned *Operation can
// be used by the caller to attach handler-specific attributes (e.g. nonce,
// inference id, sender) once they are parsed from the request.
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

	var ir InferenceRequest
	if err := json.Unmarshal(body, &ir); err != nil {
		return observability.FailNoReceipt(ctx, s.host.EscrowID(),
			observability.ReasonParseErr, observability.WhereTransportHandleInference,
			"HandleInference: invalid json", echo.NewHTTPError(http.StatusBadRequest, "invalid json: "+err.Error()))
	}

	req, err := HostRequestFromJSON(ir)
	if err != nil {
		return observability.FailNoReceipt(ctx, s.host.EscrowID(),
			observability.ReasonDecodeErr, observability.WhereTransportHandleInference,
			"HandleInference: decode request", echo.NewHTTPError(http.StatusBadRequest, "decode request: "+err.Error()))
	}
	if req.Payload != nil {
		observability.Request.SetModel(op, req.Payload.Model)
	}
	observability.Request.SetNonce(op, req.Nonce)

	resp, err := s.host.HandleRequest(ctx, req)
	if err != nil {
		reason, where := observability.ErrorReason(err, observability.ReasonHandleRequestErr, observability.WhereTransportHandleInference)
		if errors.Is(err, devshard.ErrRequestsDisabled) {
			logging.Debug("HandleInference: devshard_requests_enabled=false", "subsystem", "server")
			c.Response().Header().Set(HeaderDevshardError, DevshardErrorRequestsDisabled)
			return observability.FailNoReceipt(ctx, s.host.EscrowID(), reason, where,
				"HandleInference: requests disabled", echo.NewHTTPError(http.StatusServiceUnavailable, err.Error()))
		}
		return observability.FailNoReceipt(ctx, s.host.EscrowID(), reason, where,
			"HandleInference: handle request", echo.NewHTTPError(http.StatusInternalServerError, err.Error()))
	}
	observability.Request.SetInferenceID(op, resp.InferenceID)
	observability.Request.SetInferenceResponse(op, resp.Nonce, resp.ExecutionExpected, resp.CachedResponseBody != nil)

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
	if werr := writeSSEEvent(w, receiptWrapper); werr != nil {
		observability.RecordReceiptWriteFailure(ctx, s.host.EscrowID(), resp.InferenceID, resp.Nonce, observability.ReasonReceiptWriteErr, observability.WhereTransportWriteReceiptSSE)
		if resp.ExecutionJob != nil {
			s.host.ReleaseExecution(resp.InferenceID)
		}
		return nil
	}

	finishReason := observability.ReasonOK
	var finishFailureWhere observability.Where

	// Event 2+: inference result.
	// If reconnecting to a completed inference, replay cached response.
	// Otherwise run deferred execution with live streaming.
	if resp.CachedResponseBody != nil && resp.ExecutionJob == nil {
		if werr := replaySSEBody(w, resp.CachedResponseBody); werr != nil {
			observability.RecordReceiptNoExecutionInterrupted(ctx, s.host.EscrowID(), resp.InferenceID, resp.Nonce, observability.ReasonCachedReplayErr, observability.WhereRuntimeWriteClientResponse)
			return nil
		}
	} else if resp.ExecutionJob != nil {
		resp.ExecutionJob.ResponseWriter = w
		execResult, execErr := s.host.RunExecution(ctx, resp.ExecutionJob)
		if execErr != nil {
			reason, where := observability.ErrorReason(execErr, observability.ReasonExecuteErr, observability.WhereHostExecute)
			if errors.Is(ctx.Err(), context.Canceled) {
				observability.RecordClientCancelledAfterReceipt(ctx, s.host.EscrowID(), resp.InferenceID, resp.Nonce, where)
				return nil
			}
			observability.RecordExecutionNoFinish(ctx, s.host.EscrowID(), resp.InferenceID, resp.Nonce, reason, where)
			logging.Error("deferred execution failed", "subsystem", "server", "error", execErr)
			return nil
		}
		if execResult != nil && execResult.PartialResponse {
			finishReason = observability.Reason(execResult.PartialResponseReason)
			if finishReason == "" {
				finishReason = observability.ReasonPartialResponseInterrupted
			}
			finishFailureWhere = observability.Where(execResult.PartialResponseWhere)
		}
	}

	// Final event: devshard_meta with updated mempool.
	mempoolTxs := s.host.MempoolTxs()
	mempoolBytes, _ := DevshardTxsToBytes(mempoolTxs)
	metaWrapper := map[string]interface{}{"devshard_meta": DevshardMetaEvent{Mempool: mempoolBytes}}
	_ = writeSSEEvent(w, metaWrapper)

	// Fire gossip in background.
	if s.gossip != nil && resp.StateSig != nil {
		go s.gossip.AfterRequest(context.Background(), resp.Nonce, resp.StateHash, resp.StateSig)
	}
	if s.gossip != nil && resp.StateSig == nil && len(resp.Mempool) > 0 {
		go s.gossip.BroadcastTxs(context.Background(), resp.Mempool)
	}

	switch {
	case resp.ExecutionExpected && resp.ExecutionJob != nil:
		observability.RecordFinishPublished(ctx, s.host.EscrowID(), resp.InferenceID, resp.Nonce, finishReason, finishFailureWhere)
	case resp.Receipt != nil:
		observability.RecordReceiptNoExecutionExpected(ctx, s.host.EscrowID(), resp.InferenceID, resp.Nonce, resp.ReceiptReason, observability.WhereHostSignReceipt)
	default:
		observability.RecordNoReceiptExpected(ctx, s.host.EscrowID(), resp.InferenceID, resp.Nonce, resp.ReceiptReason, observability.WhereHostSignReceipt)
	}

	return nil
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

// SetPeerClients sets the executor clients for timeout verification.
// Key is slot index (position in group), value is an ExecutorClient.
func (s *Server) SetPeerClients(peers map[int]*HTTPClient) {
	s.peerClients = peers
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
	if !s.host.CompletionRequestsEnabled() {
		logging.Debug("HandleVerifyTimeout: devshard_requests_enabled=false", "subsystem", "server")
		return HTTPError(c, http.StatusServiceUnavailable, DevshardErrorRequestsDisabled, devshard.ErrRequestsDisabled.Error())
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

func (s *Server) HandleGossipNonce(c echo.Context) (err error) {
	op, finish := startHandlerSpan(c, "gossip_nonce")
	defer finish(&err)

	// Gossip is host-to-host only. Reject user-signed requests.
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

func (s *Server) HandleGossipTxs(c echo.Context) (err error) {
	op, finish := startHandlerSpan(c, "gossip_txs")
	defer finish(&err)

	// Gossip is host-to-host only.
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

	if s.gossip != nil {
		txs, err := DevshardTxsFromBytes(req.Txs)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "decode txs: "+err.Error())
		}
		observability.Request.SetGossipTxsCount(op, len(txs))
		s.gossip.OnTxsReceived(txs)
	}

	return c.NoContent(http.StatusOK)
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

	sigs, err := s.host.GetSignatures(nonce)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	observability.Request.SetSignaturesReturned(op, len(sigs))

	return writeJSON(c, http.StatusOK, SignaturesResponse{Signatures: sigs})
}

func (s *Server) HandleGetDiffs(c echo.Context) (err error) {
	op, finish := startHandlerSpan(c, "get_diffs")
	defer finish(&err)

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
	observability.Request.SetDiffsRange(op, from, to)

	records, err := s.store.GetDiffs(s.host.EscrowID(), from, to)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	observability.Request.SetDiffsReturned(op, len(records))

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

func (s *Server) HandleGetMempool(c echo.Context) (err error) {
	op, finish := startHandlerSpan(c, "get_mempool")
	defer finish(&err)

	txs := s.host.MempoolTxs()
	observability.Request.SetMempoolSize(op, len(txs))
	data, err := DevshardTxsToBytes(txs)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	observability.Request.SetResponseContentLength(op, len(data))
	return writeJSON(c, http.StatusOK, map[string]interface{}{"txs": data})
}
