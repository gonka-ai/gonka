package rpcserver

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
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

	defaultMessagesPerMin = transport.DefaultRPCMessagesPerMin
	defaultMaxStreams     = transport.DefaultRPCMaxStreams
	defaultMaxSessions    = 10_000
	// defaultAttachFloorPerMin is the process-wide Attach cap.
	// Per-IP Attach bounds live on versiond / Phase 6 proxy, not this child.
	// The floor is one first-Attach per current peer per minute so a full map
	// re-attaching after a Watch mass-break still fits; known-peer renewals
	// are refunded and do not occupy extra slots.
	defaultAttachFloorPerMin = transport.DefaultRPCAttachFloorPerMin
	// defaultTokenGrace is how long a replaced token still admits RPCs.
	// Matches transport.nonInferenceRetryBudget: one Attach RTT plus retry.
	defaultTokenGrace = 5 * time.Second
	// maxAttachRecvBytes is the HTTP body cap on Attach only. The Connect
	// mux allows 16 KiB on authenticated RPCs; this
	// path is unauthenticated and must not buy that read before ECDSA.
	maxAttachRecvBytes = 4 << 10
	// retiredNonceTTL is how long a dropped attach_nonce stays unrebindable.
	// Anchored to max(drop time, attach timestamp) so a future-skewed
	// signature cannot outlive its retirement.
	retiredNonceTTL = time.Duration(transport.MaxTimestampDrift) * time.Second
	// sweepBatchSize is how many expired session / retired-nonce keys SweepOnce
	// deletes per write-lock hold. Attach's in-lock sweep at the cap is
	// unchanged (already under mu).
	sweepBatchSize = 256
)

// AllowPeer decides whether a recovered address may use the escrow in
// ctx (EscrowIDFromContext). Attach uses it as the door (the token is still
// host-wide). Data RPCs check AllowsSender on the session resolved for that
// RPC. A non-nil error means the host could not decide; mapAllowError maps it
// the same way JSON sessionHTTPError does, never as a rejected peer.
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
	// AttachFloorPerMin is the process-wide Attach cap, enforced before ECDSA.
	// Zero means Limits.AttachFloorPerMin or defaultAttachFloorPerMin. Not keyed
	// on peer_address: that is attacker-chosen; recovered address is after ECDSA.
	// Child is on loopback, so this is the process floor, not a client-IP limiter.
	AttachFloorPerMin int
	// Limits is advertised on Attach and enforced by the channel interceptor.
	// Nil uses defaults (not process env — production passes LoadChannelLimitConfig).
	Limits *transport.ChannelLimitConfig
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
	closed    atomic.Bool

	attachMu    sync.Mutex
	attachTimes []time.Time

	limiter *channelLimiter
	traffic *transport.RPCTraffic
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
	limits := transport.ChannelLimitConfig{}.WithDefaults()
	if cfg.Limits != nil {
		limits = cfg.Limits.WithDefaults()
	}
	if limits.Disabled {
		cfg.AttachFloorPerMin = math.MaxInt
	} else if cfg.AttachFloorPerMin <= 0 {
		cfg.AttachFloorPerMin = limits.AttachFloorPerMin
		if cfg.AttachFloorPerMin <= 0 {
			cfg.AttachFloorPerMin = defaultAttachFloorPerMin
		}
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
		limiter:     newChannelLimiter(limits, cfg.Now),
		traffic:     transport.NewRPCTraffic(cfg.Now),
	}
}

func (h *PeerAuthHandler) now() time.Time { return h.cfg.Now() }

// Traffic is the inbound minute-bucket recorder for GET /stats/rpc.
func (h *PeerAuthHandler) Traffic() *transport.RPCTraffic {
	if h == nil {
		return nil
	}
	return h.traffic
}

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

func (h *PeerAuthHandler) advertisedRateLimits() *rpcpb.RateLimits {
	if h == nil || h.limiter == nil {
		return advertisedRateLimits(transport.ChannelLimitConfig{}.WithDefaults())
	}
	return h.limiter.advertised()
}

func (h *PeerAuthHandler) Attach(ctx context.Context, req *connect.Request[rpcpb.AttachRequest]) (*connect.Response[rpcpb.AttachResponse], error) {
	resp, err := h.attach(ctx, req)
	h.observeAttach(ctx, err)
	if err != nil {
		observability.IncPeerRPCAttach(connect.CodeOf(err).String())
		return nil, err
	}
	observability.IncPeerRPCAttach("ok")
	return resp, nil
}

func (h *PeerAuthHandler) attach(ctx context.Context, req *connect.Request[rpcpb.AttachRequest]) (*connect.Response[rpcpb.AttachResponse], error) {
	if h.Closed() {
		return nil, hostShuttingDown()
	}
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

	chargedAt, err := h.chargeAttach(ctx)
	if err != nil {
		return nil, err
	}

	recovered, err := transport.VerifyAttach(h.verifier, msg, h.now().Unix())
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	if recovered != msg.PeerAddress {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("recovered address %s does not match peer_address", recovered))
	}
	// Refund only after a successful bind. A live peer whose
	// Attach then fails (nonce reuse, roster, cap) must keep the charge.
	wasLive := h.peerSessionLive(recovered)
	if !wasLive {
		// Door check is first Attach only. Watch and live renewals are
		// host-scoped: the URL escrow may already be gone.
		if err := h.checkAllow(ctx, recovered); err != nil {
			return nil, err
		}
	}

	token := append([]byte(nil), msg.AttachNonce...)
	tok := rawTokenKey(token)
	expires := h.now().Add(h.cfg.SessionTTL)

	h.mu.Lock()
	if h.closed.Load() {
		h.mu.Unlock()
		return nil, hostShuttingDown()
	}
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
	if wasLive {
		h.refundAttach(chargedAt)
	}

	return connect.NewResponse(&rpcpb.AttachResponse{
		SessionToken: token,
		ExpiresAt:    expires.Unix(),
		Limits:       h.advertisedRateLimits(),
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
		case <-h.closeCh:
			return hostShuttingDown()
		case <-stop:
			if h.Closed() {
				return hostShuttingDown()
			}
			return connect.NewError(connect.CodeUnauthenticated, errors.New("session replaced"))
		case <-ticker.C:
			if h.Closed() {
				return hostShuttingDown()
			}
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
	if h.closed.Load() {
		return 0, nil, hostShuttingDown()
	}
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

// endWatch clears this Watch. It does not drop the host session: a stream
// break must not force a fresh Attach for every escrow this child serves.
// A later Watch on the same token is allowed. Re-Attach, TTL
// sweep, eviction, and Close still drop. A grace token's Watch is a no-op
// here if watchID no longer matches.
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
	sess.cancelWatch = nil
}

// LookupToken returns the peer address bound to token if it is still valid
// on this host. The token is host-scoped: it authorizes every escrow this
// child serves. Roster is the Attach door (URL escrow) and the data RPC's
// own escrow, not this lookup.
func (h *PeerAuthHandler) LookupToken(token []byte) (string, bool) {
	peer, ok, _ := h.inspectToken(token)
	return peer, ok
}

// inspectToken reports whether token is live (ok), present but past TTL
// (expired), or unknown. Handshake-gate metrics need the expired/forged split;
// LookupToken stays a bool so Watch and callers do not change.
func (h *PeerAuthHandler) inspectToken(token []byte) (peer string, ok, expired bool) {
	if h == nil || h.Closed() || len(token) == 0 || len(token) > maxAttachNonceBytes {
		return "", false, false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	sess, exists := h.sessions[rawTokenKey(token)]
	if !exists {
		return "", false, false
	}
	if h.now().After(sess.expires) {
		return sess.peer, false, true
	}
	return sess.peer, true, false
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

// chargeAttach is the process-wide Attach throttle. Sliding one-minute
// window. Child sees versiond as src, so this is not per client IP.
// handshakeGate charges it on oversized Content-Length (before decode).
// The handler charges it after decode and before ECDSA. A later
// refundAttach drops this charge if the Attach succeeds for a peer that
// already held a live or grace session.
func (h *PeerAuthHandler) chargeAttach(ctx context.Context) (time.Time, error) {
	limit := h.cfg.AttachFloorPerMin
	if limit <= 0 || limit == math.MaxInt {
		return time.Time{}, nil
	}
	now := h.now()
	cutoff := now.Add(-time.Minute)
	h.attachMu.Lock()
	kept := h.attachTimes[:0]
	for _, ts := range h.attachTimes {
		if ts.After(cutoff) {
			kept = append(kept, ts)
		}
	}
	h.attachTimes = kept
	if len(h.attachTimes) >= limit {
		retry := time.Minute
		if len(h.attachTimes) > 0 {
			retry = h.attachTimes[0].Add(time.Minute).Sub(now)
		}
		h.attachMu.Unlock()
		if h.limiter != nil {
			h.limiter.warnBanned(ctx, rpcpbconnect.PeerAuthServiceAttachProcedure, zoneAttachFloor, "process")
		}
		return time.Time{}, attachFloorExhausted(retryAfterSeconds(retry))
	}
	h.attachTimes = append(h.attachTimes, now)
	h.attachMu.Unlock()
	return now, nil
}

func (h *PeerAuthHandler) refundAttach(at time.Time) {
	if at.IsZero() {
		return
	}
	h.attachMu.Lock()
	defer h.attachMu.Unlock()
	for i := len(h.attachTimes) - 1; i >= 0; i-- {
		if h.attachTimes[i].Equal(at) {
			h.attachTimes = append(h.attachTimes[:i], h.attachTimes[i+1:]...)
			return
		}
	}
}

func (h *PeerAuthHandler) peerSessionLive(addr string) bool {
	if h == nil || addr == "" {
		return false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	now := h.now()
	live := func(tok string) bool {
		sess := h.sessions[tok]
		return sess != nil && !now.After(sess.expires)
	}
	if tok, ok := h.byPeer[addr]; ok && live(tok) {
		return true
	}
	if tok, ok := h.prevByPeer[addr]; ok && live(tok) {
		return true
	}
	return false
}

func attachFloorExhausted(retryAfterSec int) error {
	err := connect.NewError(connect.CodeResourceExhausted, errors.New("too many attach attempts"))
	err.Meta().Set("Retry-After", strconv.Itoa(retryAfterSec))
	return err
}

func retryAfterSeconds(d time.Duration) int {
	if d < time.Second {
		return 1
	}
	sec := int((d + time.Second - 1) / time.Second)
	if sec < 1 {
		return 1
	}
	return sec
}

func (h *PeerAuthHandler) dropSessionLocked(tok string, sess *peerSession) {
	delete(h.sessions, tok)
	var attached int64
	if sess != nil {
		attached = sess.attached
		sess.stopWatchLocked()
		if h.byPeer[sess.peer] == tok {
			delete(h.byPeer, sess.peer)
		}
		if h.prevByPeer[sess.peer] == tok {
			delete(h.prevByPeer, sess.peer)
		}
	}
	h.retireNonceLocked(tok, attached)
}

func (h *PeerAuthHandler) retireNonceLocked(tok string, attached int64) {
	if tok == "" {
		return
	}
	until := h.now().Add(retiredNonceTTL)
	if attached > 0 {
		if t := time.Unix(attached, 0).Add(retiredNonceTTL); t.After(until) {
			until = t
		}
	}
	h.retired[tok] = until
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

// Close stops the sweeper, ends every Watch, drops sessions, and refuses new
// Attach and handshake-gated RPCs. Safe without StartSweeper. http.Server.Shutdown
// can then drain the Watch handlers. Closed is FailedPrecondition so the
// client does not retry it as Unavailable.
func (h *PeerAuthHandler) Close() {
	h.closeOnce.Do(func() {
		h.closed.Store(true)
		close(h.closeCh)
		h.mu.Lock()
		defer h.mu.Unlock()
		for tok, sess := range h.sessions {
			h.dropSessionLocked(tok, sess)
		}
		h.observeSizesLocked()
	})
}

// Closed reports whether Close has run. Attach and the handshake gate fail
// closed; in-flight unaries that already bound a peer still finish.
func (h *PeerAuthHandler) Closed() bool {
	return h != nil && h.closed.Load()
}

func hostShuttingDown() error {
	return connect.NewError(connect.CodeFailedPrecondition, errors.New("host shutting down"))
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
