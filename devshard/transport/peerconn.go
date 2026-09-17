package transport

import (
	"context"
	crand "crypto/rand"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"

	devshardpkg "devshard"
	"devshard/logging"
	"devshard/observability"
	"devshard/signing"
	"devshard/transport/rpcpb"
	"devshard/transport/rpcpb/rpcpbconnect"

	"common/httpguard"
)

const (
	defaultAttachBackoffMin = 50 * time.Millisecond
	defaultAttachBackoffMax = 5 * time.Second
	defaultWatchStale       = 90 * time.Second // 3 × 30s heartbeat
	attachNonceBytes        = 16
	tokenRefreshFraction    = 0.75
	reattachReasonWatch     = "watch"
	reattachReasonTTL       = "ttl"
	defaultAttachTTL        = 5 * time.Minute
	maxAttachTTL            = time.Hour
	minAttachTTL            = 30 * time.Second
)

// DefaultAttachTimeout bounds one Attach (first handshake or TTL refresh).
// Same hang cap as DefaultHeightSeedTimeout; a distinct name so a hung
// refresh is not confused with a hung seed. A failed refresh keeps Watch
// and the live token while this timeout fires.
const DefaultAttachTimeout = 5 * time.Second

// ErrPeerNotReady is returned when an RPC is issued before Attach has
// published a token. Callers must fail fast — do not wait on Attach.
var ErrPeerNotReady = errors.New("peer rpc session not ready")

var (
	errWatchStale = errors.New("watch heartbeat stale")
	errAttachTTL  = errors.New("attach expires_at is out of range")
)

var (
	peerConnMu       sync.Mutex
	peerConnRegistry = map[string]*PeerConn{}
)

// PeerConnConfig is one Attach/Watch session to a (host, version, URL, signer) child.
type PeerConnConfig struct {
	BaseURL      string
	RoutePrefix  string
	DoorEscrowID string
	HostAddress  string
	Signer       signing.Signer
	MaxConns     int
	// DirectMux skips /sessions/{id}/rpc on the Connect base. Tests that
	// point at rpcserver.NewMux set this.
	DirectMux bool
	Adoption  *PeerRPCAdoption
	// WatchStale is how long to wait between Watch beats before reconnecting.
	// Zero means 90s.
	WatchStale time.Duration
	BackoffMin time.Duration
	BackoffMax time.Duration
	Now        func() time.Time
	Sleep      func(context.Context, time.Duration) error
	Jitter     func(time.Duration) time.Duration
	// ReadMaxBytes overrides DefaultRPCReadMaxBytes for handshake and
	// ordinary unary Connect clients. Zero uses 16 KiB.
	// GetDiffs and GetMempool responses use DefaultRPCQueryReadMaxBytes (10 MiB).
	// VerifyTimeout / VerifyErrorMiss / ChallengeReceipt use
	// DefaultRPCLargeReadMaxBytes (10 MiB). GetPayload client reads use
	// DefaultRPCPayloadMaxBytes (64 MiB) unless the caller passes a
	// per-inference PayloadReadLimit. Chat stays DefaultMaxBodySize.
	ReadMaxBytes int
	// MinTTL is the floor for AttachResponse.expires_at remaining time.
	// Zero uses minAttachTTL (30s). Tests that must use a SessionTTL
	// below that floor (unix-second TTL refresh) set this explicitly.
	MinTTL time.Duration
	// DialSet is the optional h2 origin. Empty H2URL means HTTP/1.1 on
	// BaseURL only. Production fills this from DEVSHARD_RPC_H2_*.
	// H2URL is the TCP target; TLS SNI/verify use InferenceURL's hostname.
	DialSet PeerRPCDialSet
	// H2ProbeTimeout bounds the h2 Attach probe. Zero uses
	// DefaultRPCH2ProbeTimeout (1s).
	H2ProbeTimeout time.Duration
	// H2ReadIdleTimeout PINGs a quiet h2 mux. Zero uses
	// DefaultRPCH2ReadIdleTimeout (15s).
	H2ReadIdleTimeout time.Duration
	// H2PingTimeout bounds that PING. Zero uses DefaultRPCH2PingTimeout (5s).
	H2PingTimeout time.Duration
	// GRPC uses connect.WithGRPC on the h2 origin only. HTTP/1.1 fallback
	// stays Connect. Ignored when H2URL is empty.
	GRPC bool
}

func (c PeerConnConfig) version() string {
	if c.DirectMux {
		return "direct"
	}
	v, err := devshardpkg.VersionForRoutePrefix(c.RoutePrefix)
	if err != nil || v == "" {
		return c.RoutePrefix
	}
	return v
}

func (c PeerConnConfig) childID() string {
	return c.HostAddress + "@" + c.version()
}

// PeerChildID is the adoption / Prometheus identity for a host child
// (addr@version). BindEscrowHosts and SetPeerConnReady must use the same
// string.
func PeerChildID(hostAddress, routePrefix string) string {
	hostAddress = strings.TrimSpace(hostAddress)
	if hostAddress == "" {
		return ""
	}
	return PeerConnConfig{HostAddress: hostAddress, RoutePrefix: routePrefix}.childID()
}

func (c PeerConnConfig) signerAddress() string {
	if c.Signer == nil {
		return ""
	}
	return c.Signer.Address()
}

// registryKey is the PeerConn map identity: host child + dial URL + signer.
// Two escrows with different keys or InferenceUrls must not share a token.
// Prometheus still uses childID.
func (c PeerConnConfig) registryKey() string {
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	return c.childID() + "|" + base + "|" + c.signerAddress()
}

func (c PeerConnConfig) connectBase(escrowID string) string {
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if c.DirectMux {
		return base
	}
	prefix := strings.TrimRight(c.RoutePrefix, "/")
	if prefix == "" {
		prefix = strings.TrimRight(devshardpkg.DefaultRoutePrefix(), "/")
	}
	return base + prefix + "/sessions/" + escrowID + "/rpc"
}

func (c PeerConnConfig) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c PeerConnConfig) sleep(ctx context.Context, d time.Duration) error {
	if c.Sleep != nil {
		return c.Sleep(ctx, d)
	}
	return sleepContext(ctx, d)
}

func (c PeerConnConfig) jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	if c.Jitter != nil {
		return c.Jitter(d)
	}
	// Full jitter in [d/2, d].
	half := d / 2
	span := d - half
	if span <= 0 {
		return d
	}
	return half + time.Duration(rand.Int64N(int64(span)+1))
}

// PeerConn is one Attach → Watch session per (host, version, BaseURL, signer).
type PeerConn struct {
	cfg    PeerConnConfig
	http   *http.Client
	origin *originSwitchTransport
	// authDoor is first Attach (URL escrow is the AllowsSender door).
	authDoor rpcpbconnect.PeerAuthServiceClient
	// authHost is Watch and live renewals: /sessions/_/rpc, no door.
	authHost rpcpbconnect.PeerAuthServiceClient
	// authDoorGRPC / authHostGRPC are native gRPC (connect.WithGRPC). Used
	// only while usingH2(); HTTP/1.1 fallback keeps authDoor / authHost.
	authDoorGRPC rpcpbconnect.PeerAuthServiceClient
	authHostGRPC rpcpbconnect.PeerAuthServiceClient
	key          string
	refs         atomic.Int32
	state        atomic.Int32 // 0 unauthenticated, 1 attaching, 2 ready

	token   atomic.Pointer[[]byte]
	expires atomic.Int64 // unix seconds

	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	startOnce sync.Once
	stopOnce  sync.Once

	// budget is the advertised peer-weight bucket (messages_per_min /
	// messages_burst × RPCProcedureWeight). Shared by every RPCClient on
	// this connection. IP Attach is not paced: success refunds.
	budget  peerRPCBudget
	streams peerStreamBudget
	firstOK atomic.Bool
}

const (
	stateUnauthenticated int32 = 0
	stateAttaching       int32 = 1
	stateReady           int32 = 2
)

func stateName(s int32) string {
	switch s {
	case stateAttaching:
		return observability.PeerSessionAttaching
	case stateReady:
		return observability.PeerSessionReady
	default:
		return observability.PeerSessionUnauthenticated
	}
}

func NewPeerConn(cfg PeerConnConfig) *PeerConn {
	if cfg.MaxConns <= 0 {
		cfg.MaxConns = DefaultRPCMaxConnsPerPeer
	}
	if cfg.BackoffMin <= 0 {
		cfg.BackoffMin = defaultAttachBackoffMin
	}
	if cfg.BackoffMax <= 0 {
		cfg.BackoffMax = defaultAttachBackoffMax
	}
	if cfg.WatchStale <= 0 {
		cfg.WatchStale = defaultWatchStale
	}
	maxConns := cfg.MaxConns
	origin, rt := newPeerConnTransports(cfg, maxConns)
	httpClient := &http.Client{
		Transport:     rt,
		CheckRedirect: noFollowRedirects,
	}
	opts := connectClientOptions(cfg.ReadMaxBytes)
	grpcOpts := maybeGRPC(opts, cfg.GRPC)
	doorBase := cfg.connectBase(cfg.DoorEscrowID)
	hostBase := cfg.connectBase(HostRPCEscrowID)
	authDoor := rpcpbconnect.NewPeerAuthServiceClient(httpClient, doorBase, opts...)
	authHost := authDoor
	if hostBase != doorBase {
		authHost = rpcpbconnect.NewPeerAuthServiceClient(httpClient, hostBase, opts...)
	}
	var authDoorGRPC, authHostGRPC rpcpbconnect.PeerAuthServiceClient
	if cfg.GRPC {
		authDoorGRPC = rpcpbconnect.NewPeerAuthServiceClient(httpClient, doorBase, grpcOpts...)
		authHostGRPC = authDoorGRPC
		if hostBase != doorBase {
			authHostGRPC = rpcpbconnect.NewPeerAuthServiceClient(httpClient, hostBase, grpcOpts...)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &PeerConn{
		cfg:          cfg,
		http:         httpClient,
		origin:       origin,
		authDoor:     authDoor,
		authHost:     authHost,
		authDoorGRPC: authDoorGRPC,
		authHostGRPC: authHostGRPC,
		key:          cfg.registryKey(),
		ctx:          ctx,
		cancel:       cancel,
		done:         make(chan struct{}),
	}
	p.setState(stateUnauthenticated)
	return p
}

func newPeerConnTransports(cfg PeerConnConfig, maxConns int) (*originSwitchTransport, http.RoundTripper) {
	dialer := httpguard.NewDialer()
	h1 := &http.Transport{
		MaxIdleConnsPerHost: maxConns,
		MaxConnsPerHost:     maxConns,
		IdleConnTimeout:     120 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		DialContext:         DefaultHostConnectionTracker().TrackDialContext(dialer.DialContext, transportAddress(cfg.BaseURL)),
	}
	h2URL := parseH2Origin(cfg.DialSet.H2URL)
	var h2 http.RoundTripper
	if h2URL != nil {
		h2Dial := DefaultHostConnectionTracker().TrackDialContext(dialer.DialContext, transportAddress(cfg.DialSet.H2URL))
		serverName := rpcH2ServerName(cfg.DialSet.InferenceURL)
		if serverName == "" {
			serverName = rpcH2ServerName(cfg.BaseURL)
		}
		h2 = rpch2Clients.get(h2URL, serverName, h2Dial, cfg.H2ReadIdleTimeout, cfg.H2PingTimeout)
	}
	origin := newOriginSwitchTransport(h1, h2, h2URL)
	rt := DefaultHostConnectionTracker().WrapRoundTripper(&poolWatchRoundTripper{
		base: origin,
		peer: cfg.childID(),
		max:  maxConns,
	})
	return origin, rt
}

func acquirePeerConn(cfg PeerConnConfig) *PeerConn {
	key := cfg.registryKey()
	peerConnMu.Lock()
	if pc := peerConnRegistry[key]; pc != nil {
		pc.refs.Add(1)
		peerConnMu.Unlock()
		return pc
	}
	peerConnMu.Unlock()

	fresh := NewPeerConn(cfg)
	peerConnMu.Lock()
	if pc := peerConnRegistry[key]; pc != nil {
		pc.refs.Add(1)
		peerConnMu.Unlock()
		// Never Start'ed. Close cancels the unused context.
		fresh.Close()
		return pc
	}
	fresh.refs.Store(1)
	peerConnRegistry[key] = fresh
	fresh.Start()
	peerConnMu.Unlock()
	return fresh
}

// Start runs the Attach/Watch loop. Idempotent. Tests that construct via
// NewPeerConn must call Start; acquirePeerConn already does.
func (p *PeerConn) Start() {
	if p == nil {
		return
	}
	p.startOnce.Do(func() { go p.loop() })
}

func (p *PeerConn) loop() {
	defer close(p.done)
	backoff := time.Duration(0)
	for {
		if err := p.cfg.sleep(p.ctx, p.cfg.jitter(backoff)); err != nil {
			return
		}
		p.setState(stateAttaching)
		tok, exp, err := p.attach()
		p.incAttach(err)
		if err != nil {
			p.setState(stateUnauthenticated)
			p.clearToken()
			backoff = nextAttachBackoff(backoff, p.cfg.BackoffMin, p.cfg.BackoffMax)
			continue
		}
		p.publishToken(tok, exp)
		p.setState(stateReady)
		backoff = 0
		if p.firstOK.CompareAndSwap(false, true) {
			RecordPeerReconnect(p.metricPeer(), ReconnectFirstAttach)
		}
		if err := p.serveWatch(tok, exp); err != nil {
			if p.ctx.Err() != nil {
				return
			}
			p.setState(stateUnauthenticated)
			p.clearToken()
			backoff = p.cfg.BackoffMin
		}
	}
}

func (p *PeerConn) serveWatch(tok []byte, exp time.Time) error {
	for {
		watchCtx, cancelWatch := context.WithCancel(p.ctx)
		watchErr := make(chan error, 1)
		go func(token []byte) {
			watchErr <- p.watch(watchCtx, token)
		}(append([]byte(nil), tok...))

		refreshBackoff := time.Duration(0)
		for {
			delay := p.nextRefreshWait(exp, refreshBackoff)
			timer := time.NewTimer(delay)
			select {
			case <-p.ctx.Done():
				timer.Stop()
				cancelWatch()
				<-watchErr
				return p.ctx.Err()
			case err := <-watchErr:
				timer.Stop()
				cancelWatch()
				if p.ctx.Err() != nil {
					return p.ctx.Err()
				}
				p.incReattach(reattachReasonWatch)
				if err == nil {
					err = io.EOF
				}
				return err
			case <-timer.C:
				newTok, newExp, err := p.attach()
				p.incAttach(err)
				if err != nil {
					if isRPCH2Miss(err) {
						// Origin is gone (listen/RST/ALPN/half-open). End
						// Watch; do not flip setH2(false) until the stream
						// has returned. Outer loop probes then HTTP/1.1.
						cancelWatch()
						<-watchErr
						p.incReattach(reattachReasonWatch)
						return err
					}
					// Watch and token stay. Retry refresh; do not drop to
					// unauthenticated.
					refreshBackoff = nextAttachBackoff(refreshBackoff, p.cfg.BackoffMin, p.cfg.BackoffMax)
					continue
				}
				p.incReattach(reattachReasonTTL)
				p.publishToken(newTok, newExp)
				cancelWatch()
				<-watchErr
				tok, exp = newTok, newExp
			}
			break
		}
	}
}

func (p *PeerConn) nextRefreshWait(exp time.Time, refreshBackoff time.Duration) time.Duration {
	min := p.cfg.BackoffMin
	if min <= 0 {
		min = defaultAttachBackoffMin
	}
	if refreshBackoff > 0 {
		// The first failed-refresh step is BackoffMin. Do not jitter it
		// to BackoffMin/2. Larger backoffs still jitter so a shared
		// timeout does not resynchronize a reconnect storm.
		if refreshBackoff <= min {
			return refreshBackoff
		}
		return p.cfg.jitter(refreshBackoff)
	}
	d := p.refreshDelay(exp)
	// refreshDelay floors expired / sub-min TTL to BackoffMin so Attach
	// cannot tight-loop and drop the TokenGrace predecessor. Do not jitter
	// that floor down to BackoffMin/2.
	if d <= min {
		return d
	}
	return p.cfg.jitter(d)
}

func (p *PeerConn) refreshDelay(exp time.Time) time.Duration {
	now := p.cfg.now()
	ttl := exp.Sub(now)
	min := p.cfg.BackoffMin
	if min <= 0 {
		min = defaultAttachBackoffMin
	}
	// AttachResponse.expires_at is unix seconds, so remaining can collapse
	// to 0 in the same second. Floor to BackoffMin so we do not tight-loop
	// Attach and drop the TokenGrace predecessor on the second replace.
	if ttl <= 0 {
		return min
	}
	d := time.Duration(float64(ttl) * tokenRefreshFraction)
	if d < min {
		return min
	}
	return d
}

func (p *PeerConn) attach() ([]byte, time.Time, error) {
	if p.cfg.Signer == nil {
		return nil, time.Time{}, fmt.Errorf("peer conn: signer is required")
	}
	msg, err := p.newAttachRequest()
	if err != nil {
		return nil, time.Time{}, err
	}
	// Probe + HTTP/1.1 share one DefaultAttachTimeout. h2 is capped at
	// h2ProbeTimeout; 1.1 gets the remainder (≈5s if the probe returns
	// immediately, ≈4s after a 1s blackhole).
	overall, cancel := context.WithTimeout(p.ctx, DefaultAttachTimeout)
	defer cancel()
	if !p.liveSession() && p.shouldProbeH2() {
		p.origin.setH2(true)
		h2ctx, h2cancel := context.WithTimeout(overall, p.h2ProbeTimeout())
		tok, exp, err := p.attachOnce(h2ctx, p.peerAuthClient(false), msg)
		h2cancel()
		if err == nil {
			return tok, exp, nil
		}
		if p.ctx.Err() != nil {
			return nil, time.Time{}, err
		}
		if !isRPCH2Miss(err) {
			return nil, time.Time{}, err
		}
		rememberRPCH2Miss(p.cfg.BaseURL)
		p.origin.setH2(false)
		logging.Warn("peer rpc h2 origin missed; using HTTP/1.1",
			"subsystem", "transport",
			"host", inferenceHostKey(p.cfg.BaseURL),
			"h2_url", p.cfg.DialSet.H2URL,
			"error", err,
		)
		msg, err = p.newAttachRequest()
		if err != nil {
			return nil, time.Time{}, err
		}
	}
	return p.attachOnce(overall, p.peerAuthClient(p.liveSession()), msg)
}

func (p *PeerConn) newAttachRequest() (*rpcpb.AttachRequest, error) {
	nonce := make([]byte, attachNonceBytes)
	if _, err := crand.Read(nonce); err != nil {
		return nil, fmt.Errorf("attach nonce: %w", err)
	}
	ts := p.cfg.now().Unix()
	peer := p.cfg.Signer.Address()
	sig, err := SignAttach(p.cfg.Signer, p.cfg.HostAddress, ts, peer, nonce, AttachProtocolVersion, nil)
	if err != nil {
		return nil, err
	}
	return &rpcpb.AttachRequest{
		PeerAddress:     peer,
		AttachNonce:     nonce,
		ProtocolVersion: AttachProtocolVersion,
		HostAddress:     p.cfg.HostAddress,
		Timestamp:       ts,
		Signature:       sig,
	}, nil
}

func (p *PeerConn) attachOnce(ctx context.Context, client rpcpbconnect.PeerAuthServiceClient, msg *rpcpb.AttachRequest) ([]byte, time.Time, error) {
	resp, err := client.Attach(ctx, connect.NewRequest(msg))
	if err != nil {
		return nil, time.Time{}, err
	}
	expires, err := p.attachExpiry(resp.Msg.GetExpiresAt())
	if err != nil {
		return nil, time.Time{}, err
	}
	p.budget.apply(resp.Msg.GetLimits(), p.cfg.now())
	p.streams.applyPool(resp.Msg.GetLimits(), p.cfg.MaxConns)
	return resp.Msg.GetSessionToken(), expires, nil
}

func (p *PeerConn) shouldProbeH2() bool {
	if p == nil || p.cfg.DialSet.H2URL == "" {
		return false
	}
	return !skipRPCH2(p.cfg.BaseURL)
}

func (p *PeerConn) useGRPC() bool {
	return p != nil && p.cfg.GRPC && p.origin.usingH2()
}

// peerAuthClient is the door (first Attach) or host (Watch / live renew)
// client. Native gRPC is only selected while the live origin is h2.
func (p *PeerConn) peerAuthClient(host bool) rpcpbconnect.PeerAuthServiceClient {
	if p.useGRPC() {
		if host {
			if p.authHostGRPC != nil {
				return p.authHostGRPC
			}
		} else if p.authDoorGRPC != nil {
			return p.authDoorGRPC
		}
	}
	if host {
		return p.authHost
	}
	return p.authDoor
}

func (p *PeerConn) h2ProbeTimeout() time.Duration {
	if p.cfg.H2ProbeTimeout > 0 {
		return p.cfg.H2ProbeTimeout
	}
	return DefaultRPCH2ProbeTimeout
}

// UsingH2 is whether the live origin is the published hop (prior-knowledge
// HTTP/2), not InferenceUrl HTTP/1.1.
func (p *PeerConn) UsingH2() bool {
	return p != nil && p.origin.usingH2()
}

// UsingGRPC is whether live RPCs use connect.WithGRPC. False on HTTP/1.1.
func (p *PeerConn) UsingGRPC() bool {
	return p.useGRPC()
}

func (p *PeerConn) takePeerBudget(ctx context.Context, procedure string) error {
	if p == nil {
		return nil
	}
	return p.budget.take(ctx, procedure, p.cfg.now, p.cfg.sleep)
}

func (p *PeerConn) refundPeerBudget(procedure string) {
	if p == nil {
		return
	}
	p.budget.refund(p.cfg.now(), procedure)
}

func (p *PeerConn) acquireStream() bool {
	if p == nil {
		return true
	}
	return p.streams.acquire()
}

func (p *PeerConn) acquireChatStream() bool {
	if p == nil {
		return true
	}
	return p.streams.acquireChat()
}

func (p *PeerConn) releaseStream() {
	if p == nil {
		return
	}
	p.streams.release()
}

func (p *PeerConn) releaseChatStream() {
	if p == nil {
		return
	}
	p.streams.releaseChat()
}

func (p *PeerConn) minTTL() time.Duration {
	if p.cfg.MinTTL > 0 {
		return p.cfg.MinTTL
	}
	return minAttachTTL
}

// attachExpiry accepts expires_at remaining in [minTTL, maxAttachTTL].
// Zero means the server omitted it and we use defaultAttachTTL. Anything
// else is a protocol error so a host cannot drive Attach in a tight loop.
func (p *PeerConn) attachExpiry(expiresAt int64) (time.Time, error) {
	now := p.cfg.now()
	if expiresAt == 0 {
		return now.Add(defaultAttachTTL), nil
	}
	exp := time.Unix(expiresAt, 0)
	ttl := exp.Sub(now)
	if ttl < p.minTTL() || ttl > maxAttachTTL {
		return time.Time{}, errAttachTTL
	}
	return exp, nil
}

func (p *PeerConn) watch(ctx context.Context, token []byte) error {
	if !p.acquireStream() {
		return connect.NewError(connect.CodeResourceExhausted, errors.New("too many concurrent streams"))
	}
	defer p.releaseStream()
	req := connect.NewRequest(&rpcpb.WatchRequest{SessionToken: token})
	SetSessionHeader(req.Header(), token)
	// Watch's HTTP request must not use ctx. PeerConn.Close cancels ctx
	// first; x/net Body.Close then returns on cs.ctx.Done without waiting
	// for forgetStreamID, so CloseIdleConnections leaves the mux up until
	// IdleConnTimeout. An independent reqCtx keeps Close blocking until
	// the stream is gone.
	reqCtx, reqCancel := context.WithCancel(context.Background())
	defer reqCancel()
	stream, err := p.peerAuthClient(true).Watch(reqCtx, req)
	if err != nil {
		return err
	}

	type recvResult struct {
		ok  bool
		err error
	}
	recv := make(chan recvResult, 1)
	recvDone := make(chan struct{})
	shutdown := make(chan struct{})
	go func() {
		defer close(recvDone)
		for stream.Receive() {
			select {
			case recv <- recvResult{ok: true}:
			case <-ctx.Done():
			case <-shutdown:
				return
			}
		}
		select {
		case recv <- recvResult{err: stream.Err()}:
		default:
		}
	}()
	defer func() {
		_ = stream.Close()
		close(shutdown)
		<-recvDone
	}()

	timer := time.NewTimer(p.cfg.WatchStale)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return errWatchStale
		case r := <-recv:
			if !r.ok {
				if r.err == nil {
					return io.EOF
				}
				return r.err
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(p.cfg.WatchStale)
		}
	}
}

func (p *PeerConn) publishToken(tok []byte, exp time.Time) {
	cp := append([]byte(nil), tok...)
	p.token.Store(&cp)
	p.expires.Store(exp.Unix())
}

func (p *PeerConn) clearToken() {
	p.token.Store(nil)
	p.expires.Store(0)
}

// LiveToken is the current session id, or nil if unauthenticated. Fail-fast:
// never waits for Attach.
func (p *PeerConn) LiveToken() []byte {
	if p == nil {
		return nil
	}
	got := p.token.Load()
	if got == nil || len(*got) == 0 {
		return nil
	}
	return append([]byte(nil), *got...)
}

func (p *PeerConn) State() string {
	if p == nil {
		return observability.PeerSessionUnauthenticated
	}
	return stateName(p.state.Load())
}

func (p *PeerConn) tokenPresent() bool {
	if p == nil {
		return false
	}
	got := p.token.Load()
	return got != nil && len(*got) > 0
}

func (p *PeerConn) tokenUnexpired() bool {
	if p == nil {
		return false
	}
	exp := p.expires.Load()
	if exp <= 0 {
		return false
	}
	return !p.cfg.now().After(time.Unix(exp, 0))
}

// liveSession is a published token in state ready, ignoring local expiry.
// Attach renewals use this so a clock-expired token still refreshes on the
// host path.
func (p *PeerConn) liveSession() bool {
	return p != nil && p.state.Load() == stateReady && p.tokenPresent()
}

func (p *PeerConn) Ready() bool {
	return p.liveSession() && p.tokenUnexpired()
}

// metricPeer is the Prometheus / adoption identity: addr@version.
// Distinct from registryKey, which also includes BaseURL and signer.
func (p *PeerConn) metricPeer() string {
	if p == nil {
		return ""
	}
	return p.cfg.childID()
}

func (p *PeerConn) connID() string {
	// Instance identity, not registryKey: acquire's unused NewPeerConn
	// shares the registry key with the winner and Close must not drop
	// the winner's series.
	return fmt.Sprintf("%p", p)
}

func (p *PeerConn) setState(s int32) {
	prev := p.state.Swap(s)
	p.publishChildState(s)
	if p.cfg.Adoption == nil {
		return
	}
	wasReady := prev == stateReady
	nowReady := s == stateReady
	if wasReady == nowReady {
		return
	}
	p.cfg.Adoption.setConnReady(p.metricPeer(), p.connID(), nowReady)
}

var (
	childMetricMu     sync.Mutex
	childMetricStates = map[string]map[string]int32{} // childID -> connID -> state
)

func bestChildState(conns map[string]int32) int32 {
	best := stateUnauthenticated
	for _, s := range conns {
		if s == stateReady {
			return stateReady
		}
		if s == stateAttaching {
			best = stateAttaching
		}
	}
	return best
}

func (p *PeerConn) publishChildState(s int32) {
	peer := p.metricPeer()
	if peer == "" {
		return
	}
	id := p.connID()
	childMetricMu.Lock()
	defer childMetricMu.Unlock()
	conns := childMetricStates[peer]
	if conns == nil {
		conns = map[string]int32{}
		childMetricStates[peer] = conns
	}
	conns[id] = s
	observability.SetPeerSessionState(peer, stateName(bestChildState(conns)))
}

func (p *PeerConn) dropChildState() {
	peer := p.metricPeer()
	id := p.connID()
	childMetricMu.Lock()
	defer childMetricMu.Unlock()
	conns := childMetricStates[peer]
	if conns == nil {
		observability.ClearPeerSessionState(peer)
		return
	}
	delete(conns, id)
	if len(conns) == 0 {
		delete(childMetricStates, peer)
		observability.ClearPeerSessionState(peer)
		return
	}
	observability.SetPeerSessionState(peer, stateName(bestChildState(conns)))
}

func (p *PeerConn) incAttach(err error) {
	result := "ok"
	if err != nil {
		if code := connect.CodeOf(err); code != connect.CodeUnknown {
			result = code.String()
		} else {
			result = "error"
		}
	}
	observability.IncPeerAttach(p.metricPeer(), result)
}

func (p *PeerConn) incReattach(reason string) {
	observability.IncPeerReattach(p.metricPeer(), reason)
	RecordPeerReconnect(p.metricPeer(), reason)
}

// Close stops the attach loop. Tests that called NewPeerConn+Start must
// Close. Registry-backed conns use Release.
func (p *PeerConn) Close() {
	if p == nil {
		return
	}
	p.stopOnce.Do(func() {
		p.cancel()
		p.startOnce.Do(func() { close(p.done) })
		<-p.done
		// watch() has already Close'd the Watch stream (or never started).
		// Idle muxes can FIN; a shared origin with sibling streams stays up.
		if p.http != nil {
			p.http.CloseIdleConnections()
		}
		if p.origin != nil {
			p.origin.CloseIdleConnections()
		}
		p.setState(stateUnauthenticated)
		p.clearToken()
		p.dropChildState()
	})
}

// PeerConnRegistered is whether any registry-backed PeerConn is still live
// for this host+version (any signer / BaseURL).
func PeerConnRegistered(hostAddress, version string) bool {
	if hostAddress == "" {
		return false
	}
	if version == "" {
		version = "direct"
	}
	prefix := hostAddress + "@" + version + "|"
	peerConnMu.Lock()
	defer peerConnMu.Unlock()
	for key := range peerConnRegistry {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// Release drops a registry reference and Closes when the last user leaves.
// Map delete and the last ref drop run under peerConnMu so acquire cannot
// bump a dying conn.
func (p *PeerConn) Release() {
	if p == nil {
		return
	}
	peerConnMu.Lock()
	if p.refs.Add(-1) > 0 {
		peerConnMu.Unlock()
		return
	}
	if p.key != "" && peerConnRegistry[p.key] == p {
		delete(peerConnRegistry, p.key)
	}
	peerConnMu.Unlock()
	p.Close()
}

func nextAttachBackoff(prev, min, max time.Duration) time.Duration {
	if prev <= 0 {
		return min
	}
	next := prev * 2
	if next > max {
		return max
	}
	return next
}

type poolWatchRoundTripper struct {
	base     http.RoundTripper
	peer     string
	max      int
	inflight atomic.Int32
}

func (t *poolWatchRoundTripper) CloseIdleConnections() {
	if t == nil {
		return
	}
	closeIdleConnections(t.base)
}

func (t *poolWatchRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	n := t.inflight.Add(1)
	if t.max > 0 && int(n) > t.max {
		observability.IncPeerPoolExhausted(t.peer)
	}
	resp, err := t.base.RoundTrip(req)
	if err != nil || resp == nil {
		t.inflight.Add(-1)
		return resp, err
	}
	// Connect server-streams typically return after headers. Count Watch
	// (and any other body) until Close, not RoundTrip return.
	if resp.Body == nil || resp.Body == http.NoBody {
		t.inflight.Add(-1)
		return resp, nil
	}
	resp.Body = &countedBody{ReadCloser: resp.Body, inflight: &t.inflight}
	return resp, nil
}

type countedBody struct {
	io.ReadCloser
	inflight *atomic.Int32
	closed   atomic.Bool
}

func (b *countedBody) Close() error {
	if b.closed.CompareAndSwap(false, true) {
		b.inflight.Add(-1)
	}
	if b.ReadCloser == nil {
		return nil
	}
	return b.ReadCloser.Close()
}
