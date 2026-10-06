package rpcserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
	// defaultSessionTTL is how long a token-only read stays usable. Chat,
	// gossip, and height sync also require the peer's envelope signature.
	defaultSessionTTL        = 2 * time.Minute
	defaultHeartbeat         = 30 * time.Second
	defaultWatchWriteTimeout = 10 * time.Second
	minAttachNonceBytes      = 16
	// maxAttachNonceBytes is a UUID or 256-bit id. The token carries only
	// its SHA-256, so the bound is about request size, not token size.
	maxAttachNonceBytes = 32

	defaultMessagesPerMin = transport.DefaultRPCMessagesPerMin
	defaultMaxStreams     = transport.DefaultRPCMaxStreams
	defaultMaxSessions    = 10_000
	// defaultAttachFloorPerMin is the configured ceiling passed in when the
	// operator does not set a tighter cap. The enforced bucket is smaller:
	// defaultAttachBurst tokens, refilled at defaultAttachRefillPerSec.
	defaultAttachFloorPerMin = transport.DefaultRPCAttachFloorPerMin
	// defaultAttachBurst is how many anonymous Attaches may run VerifyAttach
	// before the bucket is empty. A live X-Devshard-Session does not take one.
	defaultAttachBurst = 50
	// defaultAttachRefillPerSec refills the anonymous bucket continuously.
	// A full burst is back one second later. Retry-After on an empty bucket is 1.
	defaultAttachRefillPerSec = 50
	// defaultAttachInFlight is how many anonymous VerifyAttach calls may run
	// at once. Renewals that present a live token are not in this count.
	defaultAttachInFlight = 50
	// maxAttachRecvBytes is the HTTP body cap on Attach only. The Connect
	// mux allows 16 KiB on authenticated RPCs; this
	// path is unauthenticated and must not buy that read before ECDSA.
	maxAttachRecvBytes = 4 << 10
	// attachRetryAfterSec is the Retry-After on an empty anonymous bucket.
	attachRetryAfterSec = 1
)

// AllowPeer decides whether a recovered address may use the escrow in
// ctx (EscrowIDFromContext). Attach uses it as the door on first handshake
// and on any later Attach whose URL is not HostRPCEscrowID (the token is
// still host-wide). Live renewals on `_` skip it while the peer session is
// live. Data RPCs check AllowsSender on the session resolved for that RPC.
// A non-nil error means the host could not decide; mapAllowError maps it
// the same way JSON sessionHTTPError does, never as a rejected peer.
type AllowPeer func(ctx context.Context, address string) (bool, error)

// PeerAuthConfig tunes session lifetime. Zero values use defaults.
type PeerAuthConfig struct {
	SessionTTL        time.Duration
	Heartbeat         time.Duration
	WatchWriteTimeout time.Duration
	// MaxSessions caps concurrent Watch streams. Zero means
	// defaultMaxSessions. It does not cap Attach or live tokens.
	MaxSessions int
	// SessionKey is the HMAC key. Empty means this process generates a
	// random key, so only this process can admit the tokens it issues.
	// Production derives it from the host private key and sets it here.
	SessionKey []byte
	// Version is bound into every token. Empty matches only an empty version.
	Version string
	// KeyID selects the HMAC key. Zero means SessionKeyID.
	KeyID byte
	// AttachFloorPerMin sizes the anonymous Attach bucket. Zero means
	// Limits.AttachFloorPerMin or defaultAttachFloorPerMin. A value below
	// defaultAttachBurst is the burst (tests). The default and anything
	// larger use defaultAttachBurst tokens refilled at defaultAttachRefillPerSec.
	// Not keyed on peer_address: that field is unsigned. A live session token
	// skips the bucket and spends the per-peer renewal bucket instead.
	// math.MaxInt disables it.
	AttachFloorPerMin int
	// Limits is advertised on Attach and enforced by the channel interceptor.
	// Nil uses defaults (not process env — production passes LoadChannelLimitConfig).
	Limits *transport.ChannelLimitConfig
	// Allow is the URL-escrow roster check at Attach. Nil skips (tests).
	Allow AllowPeer
	// LiveSession is whether this child already has a local session for
	// the URL escrow. Shard rows in RPCTraffic are created only when this
	// is true. It must not CreateSession, recover from store, or GetEscrow
	// — in-memory lookup only (HostManager.existingServer). Nil means no
	// shard rows (host / peer / IP still record).
	LiveSession func(escrowID string) bool
	Now         func() time.Time
}

// PeerAuthHandler implements rpcpbconnect.PeerAuthServiceHandler.
type PeerAuthHandler struct {
	rpcpbconnect.UnimplementedPeerAuthServiceHandler

	verifier    signing.Verifier
	hostAddress string
	cfg         PeerAuthConfig

	sessionKey []byte
	keyID      byte

	closeCh   chan struct{}
	closeOnce sync.Once
	closed    atomic.Bool

	attachMu     sync.Mutex
	attachBucket attachBucket

	limiter *channelLimiter
	traffic *transport.RPCTraffic
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
		cfg.AttachFloorPerMin = transport.ClampAttachFloorPerMin(cfg.AttachFloorPerMin)
	} else {
		cfg.AttachFloorPerMin = transport.ClampAttachFloorPerMin(cfg.AttachFloorPerMin)
	}
	limiter := newChannelLimiter(limits, cfg.Now)
	limiter.maxWatches = cfg.MaxSessions
	key := append([]byte(nil), cfg.SessionKey...)
	if len(key) == 0 {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			panic("devshard: peer session key: " + err.Error())
		}
	}
	keyID := cfg.KeyID
	if keyID == 0 {
		keyID = SessionKeyID
	}
	return &PeerAuthHandler{
		verifier:    verifier,
		hostAddress: hostAddress,
		cfg:         cfg,
		sessionKey:  key,
		keyID:       keyID,
		closeCh:     make(chan struct{}),
		limiter:     limiter,
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

// attachDoorRequired is true when Attach's URL is a real escrow. `_` is
// Watch / live-refresh only and is not a roster id.
func attachDoorRequired(ctx context.Context) bool {
	id := EscrowIDFromContext(ctx)
	return id != "" && id != transport.HostRPCEscrowID
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

	// A live X-Devshard-Session skips the anonymous bucket and spends the
	// token peer's renewal bucket instead. peer_address is unsigned, so it
	// is not the key for either.
	tokenPeer, tokenLive := h.liveSessionToken(req.Header())
	if tokenLive {
		if err := h.limiter.takeRenewal(ctx, tokenPeer); err != nil {
			return nil, err
		}
	} else {
		if err := h.acquireAttachVerify(); err != nil {
			return nil, err
		}
		defer h.releaseAttachVerify()
	}

	recovered, err := transport.VerifyAttach(h.verifier, msg, h.now().Unix())
	if err != nil {
		// The anonymous path already spent a token. A skipped Attach whose
		// signature does not verify spends one now, so the next anonymous
		// attempt pays the floor. The auth error is unchanged.
		if tokenLive {
			h.takeAttachToken()
		}
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	// A signature for the token's peer leaves the bucket alone, even when
	// peer_address does not match. Any other recovered peer pays once; if
	// the bucket is empty that request is not admitted.
	if tokenLive && recovered != tokenPeer && !h.takeAttachToken() {
		return nil, attachFloorExhausted(attachRetryAfterSec)
	}
	if recovered != msg.PeerAddress {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("recovered address %s does not match peer_address", recovered))
	}
	wasLive := tokenLive && recovered == tokenPeer
	// Live renewals on /sessions/_/rpc skip the door: the URL is not a
	// roster. Any real escrow URL is checked even when the peer already
	// has a host session, so a Watch drop cannot re-Attach on a settled id.
	if !wasLive || attachDoorRequired(ctx) {
		if err := h.checkAllow(ctx, recovered); err != nil {
			return nil, err
		}
	}

	if h.Closed() {
		return nil, hostShuttingDown()
	}
	// A timestamp ahead of this host's clock does not buy extra lifetime.
	// A past one keeps a replay deterministic: same request, same token.
	attachedUnix := msg.Timestamp
	if now := h.now().Unix(); now < attachedUnix {
		attachedUnix = now
	}
	token, expires, err := issueSessionToken(h.sessionKey, h.keyID, h.hostAddress, h.cfg.Version, recovered, attachedUnix, h.cfg.SessionTTL, msg.AttachNonce)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
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
	if _, _, err := h.openToken(token); err != nil {
		return err
	}

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
		case <-ticker.C:
			if h.Closed() {
				return hostShuttingDown()
			}
			if _, _, err := h.openToken(token); err != nil {
				return err
			}
			if err := sendBeat(); err != nil {
				return err
			}
		}
	}
}

func (h *PeerAuthHandler) openToken(token []byte) (string, time.Time, error) {
	peer, exp, expired, err := openSessionToken(h.sessionKey, h.keyID, h.hostAddress, h.cfg.Version, token, h.now(), sessionTokenSkew)
	if expired {
		return "", time.Time{}, connect.NewError(connect.CodeUnauthenticated, errors.New("session expired"))
	}
	if err != nil {
		return "", time.Time{}, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid or expired session token"))
	}
	return peer, exp, nil
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
	if h == nil || h.Closed() || len(token) == 0 || len(token) > maxSessionTokenBytes {
		return "", false, false
	}
	peer, _, expired, err := openSessionToken(h.sessionKey, h.keyID, h.hostAddress, h.cfg.Version, token, h.now(), sessionTokenSkew)
	if expired {
		return peer, false, true
	}
	if err != nil {
		return "", false, false
	}
	return peer, true, false
}

// liveSessionToken is the peer bound to a still-valid X-Devshard-Session.
// A missing, forged, or expired header is not live. The unsigned
// peer_address is not consulted.
func (h *PeerAuthHandler) liveSessionToken(header http.Header) (string, bool) {
	if h == nil || header == nil {
		return "", false
	}
	enc := header.Get(transport.SessionHeader)
	if enc == "" || len(enc) > maxSessionTokenBytes*2 {
		return "", false
	}
	raw, err := hex.DecodeString(enc)
	if err != nil || len(raw) == 0 {
		return "", false
	}
	return h.LookupToken(raw)
}

// acquireAttachVerify takes one anonymous-bucket token and an in-flight
// slot before VerifyAttach. A live session token does not call this.
func (h *PeerAuthHandler) acquireAttachVerify() error {
	if !h.spendAttachToken(true) {
		return attachFloorExhausted(attachRetryAfterSec)
	}
	return nil
}

func (h *PeerAuthHandler) releaseAttachVerify() {
	h.attachMu.Lock()
	if h.attachBucket.inFlight > 0 {
		h.attachBucket.inFlight--
	}
	h.attachMu.Unlock()
}

// takeAttachToken charges one anonymous token after a skipped Attach whose
// signature was not for the token's peer. false means the bucket was empty,
// so this request must not be treated as admitted.
func (h *PeerAuthHandler) takeAttachToken() bool {
	return h.spendAttachToken(false)
}

func (h *PeerAuthHandler) spendAttachToken(inFlight bool) bool {
	burst, perSec, maxIn, unlimited := h.attachLimit()
	if unlimited {
		return true
	}
	now := h.now()
	h.attachMu.Lock()
	defer h.attachMu.Unlock()
	h.attachBucket.refill(now, burst, perSec)
	if h.attachBucket.tokens < 1 {
		if h.limiter != nil {
			h.limiter.warnBanned(context.Background(), rpcpbconnect.PeerAuthServiceAttachProcedure, zoneAttachFloor, "process")
		}
		return false
	}
	if inFlight && h.attachBucket.inFlight >= maxIn {
		if h.limiter != nil {
			h.limiter.warnBanned(context.Background(), rpcpbconnect.PeerAuthServiceAttachProcedure, zoneAttachFloor, "process")
		}
		return false
	}
	h.attachBucket.tokens--
	if inFlight {
		h.attachBucket.inFlight++
	}
	return true
}

func (h *PeerAuthHandler) attachLimit() (burst, perSec float64, maxIn int, unlimited bool) {
	n := h.cfg.AttachFloorPerMin
	if n <= 0 || n == math.MaxInt {
		return 0, 0, 0, true
	}
	if n < defaultAttachBurst {
		return float64(n), float64(n), n, false
	}
	return defaultAttachBurst, defaultAttachRefillPerSec, defaultAttachInFlight, false
}

// attachBucket is the anonymous Attach budget. tokens refill continuously.
// inFlight counts VerifyAttach calls that already took a token.
type attachBucket struct {
	tokens   float64
	burst    float64
	perSec   float64
	last     time.Time
	ready    bool
	inFlight int
}

func (b *attachBucket) refill(now time.Time, burst, perSec float64) {
	if b == nil {
		return
	}
	if !b.ready || b.burst != burst || b.perSec != perSec {
		b.tokens = burst
		b.burst = burst
		b.perSec = perSec
		b.last = now
		b.ready = true
		return
	}
	elapsed := now.Sub(b.last).Seconds()
	if elapsed <= 0 {
		return
	}
	b.tokens += elapsed * b.perSec
	if b.tokens > b.burst {
		b.tokens = b.burst
	}
	b.last = now
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

// Close refuses new Attach and handshake-gated RPCs and ends Watch.
// http.Server.Shutdown can then drain Watch.
func (h *PeerAuthHandler) Close() {
	h.closeOnce.Do(func() {
		h.closed.Store(true)
		close(h.closeCh)
	})
}

func hostShuttingDown() error {
	return connect.NewError(connect.CodeFailedPrecondition, errors.New("host shutting down"))
}

// Closed reports whether Close has run. Attach and the handshake gate fail
// closed; in-flight unaries that already bound a peer still finish.
func (h *PeerAuthHandler) Closed() bool {
	return h != nil && h.closed.Load()
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
