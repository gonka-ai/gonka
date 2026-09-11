package rpcserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"connectrpc.com/connect"

	"devshard/observability"
	"devshard/signing"
	"devshard/transport"
	"devshard/transport/rpcpb"
	"devshard/transport/rpcpb/rpcpbconnect"
)

const (
	defaultSessionTTL        = 5 * time.Minute
	defaultHeartbeat         = 30 * time.Second
	defaultWatchWriteTimeout = 10 * time.Second
	minAttachNonceBytes      = 16
	// maxAttachNonceBytes is a UUID or 256-bit id. Larger values become map
	// keys (raw bytes as string); they are not payloads.
	maxAttachNonceBytes = 32

	// Advertised AttachResponse.limits. Not enforced until Phase 4; zeros
	// would look like "refuse all" to a client that honours the field.
	defaultMessagesPerMin uint32 = 6000
	defaultMaxStreams     uint32 = 256
	defaultAttachPerMin   uint32 = 10
	defaultMaxSessions           = 10_000
	// defaultTokenGrace is how long a replaced token still admits RPCs.
	// Matches transport.nonInferenceRetryBudget: one Attach RTT plus retry.
	defaultTokenGrace = 5 * time.Second
	// maxAttachRecvBytes is the HTTP body cap on Attach only. The Connect
	// mux still allows 10 MiB on authenticated RPCs; this path is
	// unauthenticated and must not buy a 10 MiB read before ECDSA.
	maxAttachRecvBytes = 4 << 10
	// retiredNonceTTL is how long a dropped attach_nonce stays unrebindable.
	// It covers the remaining VerifyAttach window after the row leaves
	// sessions, so a captured Attach cannot evict a newer session.
	retiredNonceTTL = time.Duration(transport.MaxTimestampDrift) * time.Second
	// sweepBatchSize is how many expired session / retired-nonce keys SweepOnce
	// deletes per write-lock hold. Attach's in-lock sweep at the cap is
	// unchanged (already under mu).
	sweepBatchSize = 256
)

// AllowPeer decides whether a recovered address may use the escrow in
// ctx (EscrowIDFromContext). Attach uses it as the door (the token is still
// host-wide). Data RPCs check AllowsSender on the session resolved for that
// RPC. A non-nil error means the host could not decide — typically the escrow
// is not open here — and is reported as FailedPrecondition rather than as a
// rejected peer.
type AllowPeer func(ctx context.Context, address string) (bool, error)

// PeerAuthConfig tunes session lifetime. Zero values use defaults.
type PeerAuthConfig struct {
	SessionTTL        time.Duration
	Heartbeat         time.Duration
	WatchWriteTimeout time.Duration
	// MaxSessions caps distinct peers with a current (non-grace) session.
	// Zero means defaultMaxSessions. A replaced token kept for TokenGrace
	// does not consume this cap.
	MaxSessions int
	// TokenGrace is how long a replaced token still LookupToken's. Zero
	// means defaultTokenGrace. In-flight RPCs carry the old header; they are
	// new HTTP requests, not a connection established at Attach.
	TokenGrace time.Duration
	// AttachPerMin is the process-wide Attach cap, enforced before ECDSA.
	// Zero means defaultAttachPerMin (the advertised limits.attach_per_min).
	// Not keyed on peer_address: that is attacker-chosen; recovered address
	// is after ECDSA. Child is on loopback, so this is the process floor,
	// not a client-IP limiter.
	AttachPerMin int
	// Allow is the URL-escrow roster check at Attach. Nil skips (tests).
	Allow AllowPeer
	// SweepInterval is the expired-session ticker. Zero means SessionTTL/2
	// when StartSweeper runs. Tests that do not call StartSweeper never start
	// a goroutine.
	SweepInterval time.Duration
	Now           func() time.Time
}

// PeerAuthHandler implements rpcpbconnect.PeerAuthServiceHandler.
type PeerAuthHandler struct {
	rpcpbconnect.UnimplementedPeerAuthServiceHandler

	verifier    signing.Verifier
	hostAddress string
	cfg         PeerAuthConfig

	mu          sync.RWMutex
	sessions    map[string]*peerSession // raw attach_nonce → session
	byPeer      map[string]string       // peer address → current token
	prevByPeer  map[string]string       // peer address → grace token
	retired     map[string]time.Time    // raw attach_nonce → forget after
	nextWatchID uint64

	closeCh   chan struct{}
	closeOnce sync.Once
	sweepOnce sync.Once

	attachMu    sync.Mutex
	attachTimes []time.Time
}

type peerSession struct {
	peer        string
	expires     time.Time
	created     time.Time
	attached    int64 // AttachRequest.timestamp that created this session
	watching    bool
	watchID     uint64
	cancelWatch chan struct{}
}

func (s *peerSession) stopWatchLocked() {
	if s == nil || s.cancelWatch == nil {
		return
	}
	select {
	case <-s.cancelWatch:
	default:
		close(s.cancelWatch)
	}
}

// rawTokenKey is the sessions/byPeer map key: the raw attach_nonce, not hex.
func rawTokenKey(token []byte) string {
	return string(token)
}

func NewPeerAuthHandler(verifier signing.Verifier, hostAddress string, cfg PeerAuthConfig) *PeerAuthHandler {
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = defaultSessionTTL
	}
	if cfg.Heartbeat <= 0 {
		cfg.Heartbeat = defaultHeartbeat
	}
	if cfg.WatchWriteTimeout == 0 {
		cfg.WatchWriteTimeout = defaultWatchWriteTimeout
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.MaxSessions <= 0 {
		cfg.MaxSessions = defaultMaxSessions
	}
	if cfg.TokenGrace <= 0 {
		cfg.TokenGrace = defaultTokenGrace
	}
	if cfg.AttachPerMin <= 0 {
		cfg.AttachPerMin = int(defaultAttachPerMin)
	}
	return &PeerAuthHandler{
		verifier:    verifier,
		hostAddress: hostAddress,
		cfg:         cfg,
		sessions:    make(map[string]*peerSession),
		byPeer:      make(map[string]string),
		prevByPeer:  make(map[string]string),
		retired:     make(map[string]time.Time),
		closeCh:     make(chan struct{}),
	}
}

func (h *PeerAuthHandler) now() time.Time { return h.cfg.Now() }

func (h *PeerAuthHandler) checkAllow(ctx context.Context, addr string) error {
	if h.cfg.Allow == nil {
		return nil
	}
	if EscrowIDFromContext(ctx) == "" {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("missing escrow id"))
	}
	allowed, err := h.cfg.Allow(ctx, addr)
	if err != nil {
		return mapAllowError(err)
	}
	if !allowed {
		return connect.NewError(connect.CodePermissionDenied, errors.New("peer is not a known participant"))
	}
	return nil
}

func advertisedRateLimits() *rpcpb.RateLimits {
	return &rpcpb.RateLimits{
		MessagesPerMin: defaultMessagesPerMin,
		MaxStreams:     defaultMaxStreams,
		AttachPerMin:   defaultAttachPerMin,
	}
}

func (h *PeerAuthHandler) Attach(ctx context.Context, req *connect.Request[rpcpb.AttachRequest]) (*connect.Response[rpcpb.AttachResponse], error) {
	msg := req.Msg
	if msg == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("nil attach request"))
	}
	if msg.PeerAddress == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("peer_address is required"))
	}
	if n := len(msg.AttachNonce); n < minAttachNonceBytes || n > maxAttachNonceBytes {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("attach_nonce must be %d–%d bytes", minAttachNonceBytes, maxAttachNonceBytes))
	}
	if len(msg.ChannelBinding) != 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("channel_binding must be empty: HTTP/1.1 has no TLS layer"))
	}
	if msg.HostAddress == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("host_address is required"))
	}
	if h.hostAddress == "" || msg.HostAddress != h.hostAddress {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("host_address does not match"))
	}
	if msg.ProtocolVersion == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("protocol_version is required"))
	}
	if msg.ProtocolVersion != transport.AttachProtocolVersion {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("unsupported protocol_version"))
	}

	if err := h.allowAttach(); err != nil {
		return nil, err
	}

	recovered, err := transport.VerifyAttach(h.verifier, msg, h.now().Unix())
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	if recovered != msg.PeerAddress {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("recovered address %s does not match peer_address", recovered))
	}
	if err := h.checkAllow(ctx, recovered); err != nil {
		return nil, err
	}

	token := append([]byte(nil), msg.AttachNonce...)
	tok := rawTokenKey(token)
	expires := h.now().Add(h.cfg.SessionTTL)

	h.mu.Lock()
	if sess, ok := h.sessions[tok]; ok {
		if !h.now().After(sess.expires) {
			h.mu.Unlock()
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("attach_nonce already in use"))
		}
		h.dropSessionLocked(tok, sess)
	}
	if h.nonceRetiredLocked(tok) {
		h.mu.Unlock()
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("attach_nonce already in use"))
	}
	if curTok, ok := h.byPeer[recovered]; ok {
		if cur := h.sessions[curTok]; cur != nil && msg.Timestamp < cur.attached {
			h.mu.Unlock()
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("attach is not newer than the live session"))
		}
	}
	replacing := false
	if old, ok := h.byPeer[recovered]; ok && old != tok {
		replacing = true
	}
	// The cap is distinct current peers. LookupToken leaves expired rows
	// for the sweeper, so sweep before refusing a new peer.
	if !replacing && len(h.byPeer) >= h.cfg.MaxSessions {
		h.sweepExpiredLocked(h.now())
		if len(h.byPeer) >= h.cfg.MaxSessions && !h.evictOldestIdleLocked() {
			h.observeSizesLocked()
			h.mu.Unlock()
			return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("too many sessions"))
		}
	}
	h.replaceSessionLocked(recovered, token, expires, msg.Timestamp)
	h.observeSizesLocked()
	h.mu.Unlock()

	return connect.NewResponse(&rpcpb.AttachResponse{
		SessionToken: token,
		ExpiresAt:    expires.Unix(),
		Limits:       advertisedRateLimits(),
	}), nil
}

func (h *PeerAuthHandler) Watch(ctx context.Context, req *connect.Request[rpcpb.WatchRequest], stream *connect.ServerStream[rpcpb.SessionEvent]) error {
	_ = req
	token := TokenFromContext(ctx)
	watchID, stop, err := h.beginWatch(token)
	if err != nil {
		return err
	}
	defer h.endWatch(token, watchID)

	sendBeat := func() error {
		if err := setWatchWriteDeadline(ctx, h.cfg.WatchWriteTimeout); err != nil {
			return err
		}
		return stream.Send(&rpcpb.SessionEvent{
			Event: &rpcpb.SessionEvent_Beat{
				Beat: &rpcpb.Heartbeat{UnixSeconds: h.now().Unix()},
			},
		})
	}
	if err := sendBeat(); err != nil {
		return err
	}
	ticker := time.NewTicker(h.cfg.Heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-stop:
			return connect.NewError(connect.CodeUnauthenticated, errors.New("session replaced"))
		case <-ticker.C:
			if _, ok := h.LookupToken(token); !ok {
				return connect.NewError(connect.CodeUnauthenticated, errors.New("session expired"))
			}
			if err := sendBeat(); err != nil {
				return err
			}
		}
	}
}

func (h *PeerAuthHandler) beginWatch(token []byte) (uint64, <-chan struct{}, error) {
	if len(token) == 0 || len(token) > maxAttachNonceBytes {
		return 0, nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid or expired session token"))
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	tok := rawTokenKey(token)
	sess, ok := h.sessions[tok]
	if !ok {
		return 0, nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid or expired session token"))
	}
	if h.now().After(sess.expires) {
		h.dropSessionLocked(tok, sess)
		h.observeSizesLocked()
		return 0, nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid or expired session token"))
	}
	if sess.watching {
		return 0, nil, connect.NewError(connect.CodeAlreadyExists, errors.New("watch already active"))
	}
	h.nextWatchID++
	sess.watching = true
	sess.watchID = h.nextWatchID
	sess.cancelWatch = make(chan struct{})
	return sess.watchID, sess.cancelWatch, nil
}

// endWatch clears this Watch. It drops the session only if the token is
// still the peer's current one. A grace token from a newer Attach stays so
// in-flight RPCs that still carry the old header are admitted until TokenGrace.
func (h *PeerAuthHandler) endWatch(token []byte, watchID uint64) {
	if len(token) == 0 || len(token) > maxAttachNonceBytes || watchID == 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	tok := rawTokenKey(token)
	sess, ok := h.sessions[tok]
	if !ok || sess.watchID != watchID {
		return
	}
	sess.watching = false
	sess.watchID = 0
	if h.byPeer[sess.peer] == tok {
		h.dropSessionLocked(tok, sess)
	}
	h.observeSizesLocked()
}

// LookupToken returns the peer address bound to token if it is still valid
// on this host. The token is host-scoped: it authorizes every escrow this
// child serves. Roster is the Attach door (URL escrow) and the data RPC's
// own escrow, not this lookup.
func (h *PeerAuthHandler) LookupToken(token []byte) (string, bool) {
	if len(token) == 0 || len(token) > maxAttachNonceBytes {
		return "", false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	tok := rawTokenKey(token)
	sess, ok := h.sessions[tok]
	if !ok {
		return "", false
	}
	if h.now().After(sess.expires) {
		return "", false
	}
	return sess.peer, true
}

// InvalidateToken drops a session token. Safe if the token is already gone.
func (h *PeerAuthHandler) InvalidateToken(token []byte) {
	if len(token) == 0 || len(token) > maxAttachNonceBytes {
		return
	}
	h.mu.Lock()
	tok := rawTokenKey(token)
	if sess, ok := h.sessions[tok]; ok {
		h.dropSessionLocked(tok, sess)
		h.observeSizesLocked()
	}
	h.mu.Unlock()
}

func (h *PeerAuthHandler) replaceSessionLocked(peer string, token []byte, expires time.Time, attached int64) {
	tok := rawTokenKey(token)
	if old, ok := h.byPeer[peer]; ok && old != tok {
		if prev, ok := h.prevByPeer[peer]; ok && prev != old && prev != tok {
			if sess, ok := h.sessions[prev]; ok {
				h.dropSessionLocked(prev, sess)
			}
		}
		if sess, ok := h.sessions[old]; ok {
			graceEnd := h.now().Add(h.cfg.TokenGrace)
			if sess.expires.After(graceEnd) {
				sess.expires = graceEnd
			}
			h.prevByPeer[peer] = old
			// Token is no longer current. End its Watch now; grace still
			// admits in-flight unaries until TokenGrace.
			sess.stopWatchLocked()
		}
	}
	h.sessions[tok] = &peerSession{peer: peer, expires: expires, created: h.now(), attached: attached}
	h.byPeer[peer] = tok
}

func (h *PeerAuthHandler) dropPeerLocked(peer string) {
	if prev, ok := h.prevByPeer[peer]; ok {
		if sess, ok := h.sessions[prev]; ok {
			h.dropSessionLocked(prev, sess)
		}
	}
	if tok, ok := h.byPeer[peer]; ok {
		if sess, ok := h.sessions[tok]; ok {
			h.dropSessionLocked(tok, sess)
		}
	}
}

// evictOldestIdleLocked drops the current session that has been sitting
// without a Watch the longest. Watching peers are left alone. False if every
// current session is watching (then Attach is resource_exhausted).
func (h *PeerAuthHandler) evictOldestIdleLocked() bool {
	var (
		oldestPeer string
		oldestAt   time.Time
	)
	for peer, tok := range h.byPeer {
		sess := h.sessions[tok]
		if sess == nil || sess.watching {
			continue
		}
		if oldestPeer == "" || sess.created.Before(oldestAt) {
			oldestPeer = peer
			oldestAt = sess.created
		}
	}
	if oldestPeer == "" {
		return false
	}
	h.dropPeerLocked(oldestPeer)
	return true
}

// allowAttach is the process-wide Attach throttle, before ECDSA. Sliding
// one-minute window. Child sees versiond as src, so this is not per client IP.
func (h *PeerAuthHandler) allowAttach() error {
	limit := h.cfg.AttachPerMin
	now := h.now()
	cutoff := now.Add(-time.Minute)
	h.attachMu.Lock()
	defer h.attachMu.Unlock()
	kept := h.attachTimes[:0]
	for _, ts := range h.attachTimes {
		if ts.After(cutoff) {
			kept = append(kept, ts)
		}
	}
	h.attachTimes = kept
	if len(h.attachTimes) >= limit {
		return connect.NewError(connect.CodeResourceExhausted, errors.New("too many attach attempts"))
	}
	h.attachTimes = append(h.attachTimes, now)
	return nil
}

func (h *PeerAuthHandler) dropSessionLocked(tok string, sess *peerSession) {
	delete(h.sessions, tok)
	h.retireNonceLocked(tok)
	if sess == nil {
		return
	}
	sess.stopWatchLocked()
	if h.byPeer[sess.peer] == tok {
		delete(h.byPeer, sess.peer)
	}
	if h.prevByPeer[sess.peer] == tok {
		delete(h.prevByPeer, sess.peer)
	}
}

func (h *PeerAuthHandler) retireNonceLocked(tok string) {
	if tok == "" {
		return
	}
	h.retired[tok] = h.now().Add(retiredNonceTTL)
}

func (h *PeerAuthHandler) nonceRetiredLocked(tok string) bool {
	until, ok := h.retired[tok]
	if !ok {
		return false
	}
	if !h.now().After(until) {
		return true
	}
	delete(h.retired, tok)
	return false
}

func (h *PeerAuthHandler) observeSizesLocked() {
	observability.SetPeerRPCSessionCounts(len(h.sessions), len(h.byPeer))
}

// SessionCount is the number of live (including unswept-expired) map entries.
func (h *PeerAuthHandler) SessionCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.sessions)
}

// StartSweeper runs a ticker that drops expired sessions. Safe to call once;
// tests that never call it do not start a goroutine. Stop with Close.
func (h *PeerAuthHandler) StartSweeper() {
	h.sweepOnce.Do(func() {
		interval := h.cfg.SweepInterval
		if interval <= 0 {
			interval = h.cfg.SessionTTL / 2
		}
		if interval <= 0 {
			interval = defaultSessionTTL / 2
		}
		go h.sweepLoop(interval)
	})
}

// Close stops the sweeper. Safe without StartSweeper.
func (h *PeerAuthHandler) Close() {
	h.closeOnce.Do(func() {
		close(h.closeCh)
	})
}

// SweepOnce drops expired sessions and retired nonces. Exported for tests;
// the sweeper calls it. Expired keys are collected under RLock and deleted
// in sweepBatchSize holds so LookupToken is not blocked for a full 10k scan.
func (h *PeerAuthHandler) SweepOnce() {
	now := h.now()
	for {
		expired, retired := h.listExpired(now, sweepBatchSize)
		if len(expired) == 0 && len(retired) == 0 {
			return
		}
		h.deleteExpired(now, expired, retired)
	}
}

func (h *PeerAuthHandler) listExpired(now time.Time, limit int) (sessions, retired []string) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	remaining := limit
	if remaining <= 0 {
		return nil, nil
	}
	for tok, sess := range h.sessions {
		if !now.After(sess.expires) {
			continue
		}
		sessions = append(sessions, tok)
		remaining--
		if remaining == 0 {
			return sessions, retired
		}
	}
	for tok, until := range h.retired {
		if !now.After(until) {
			continue
		}
		retired = append(retired, tok)
		remaining--
		if remaining == 0 {
			return sessions, retired
		}
	}
	return sessions, retired
}

func (h *PeerAuthHandler) deleteExpired(now time.Time, sessions, retired []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, tok := range sessions {
		sess, ok := h.sessions[tok]
		if !ok || !now.After(sess.expires) {
			continue
		}
		h.dropSessionLocked(tok, sess)
	}
	for _, tok := range retired {
		until, ok := h.retired[tok]
		if !ok || !now.After(until) {
			continue
		}
		delete(h.retired, tok)
	}
	h.observeSizesLocked()
}

func (h *PeerAuthHandler) sweepExpiredLocked(now time.Time) {
	for tok, sess := range h.sessions {
		if now.After(sess.expires) {
			h.dropSessionLocked(tok, sess)
		}
	}
	for tok, until := range h.retired {
		if now.After(until) {
			delete(h.retired, tok)
		}
	}
}

func (h *PeerAuthHandler) sweepLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-h.closeCh:
			return
		case <-ticker.C:
			h.SweepOnce()
		}
	}
}

func setWatchWriteDeadline(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	w := responseWriterFromContext(ctx)
	if w == nil {
		return nil
	}
	err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(d))
	if err == nil || errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}
