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
)

// ErrPeerNotReady is returned when an RPC is issued before Attach has
// published a token. Callers must fail fast — do not wait on Attach.
var ErrPeerNotReady = errors.New("peer rpc session not ready")

var errWatchStale = errors.New("watch heartbeat stale")

var (
	peerConnMu       sync.Mutex
	peerConnRegistry = map[string]*PeerConn{}
)

// PeerConnConfig is one Attach/Watch session to a (host, version) child.
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
	// ReadMaxBytes caps Connect response bodies (finding 17). Zero uses
	// DefaultMaxBodySize (10 MiB), the JSON query-body ballpark.
	ReadMaxBytes int
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

func (c PeerConnConfig) registryKey() string {
	return c.HostAddress + "@" + c.version()
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

// PeerConn is one Attach → Watch session per (host gonka address, version).
type PeerConn struct {
	cfg  PeerConnConfig
	http *http.Client
	// authDoor is first Attach (URL escrow is the AllowsSender door).
	authDoor rpcpbconnect.PeerAuthServiceClient
	// authHost is Watch and live renewals: /sessions/_/rpc, no door.
	authHost rpcpbconnect.PeerAuthServiceClient
	key      string
	refs     atomic.Int32
	state    atomic.Int32 // 0 unauthenticated, 1 attaching, 2 ready

	token   atomic.Pointer[[]byte]
	expires atomic.Int64 // unix seconds

	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	startOnce sync.Once
	stopOnce  sync.Once
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
	dialer := httpguard.NewDialer()
	fallback := transportAddress(cfg.BaseURL)
	tr := &http.Transport{
		MaxIdleConnsPerHost: maxConns,
		MaxConnsPerHost:     maxConns,
		IdleConnTimeout:     120 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		DialContext:         DefaultHostConnectionTracker().TrackDialContext(dialer.DialContext, fallback),
	}
	rt := DefaultHostConnectionTracker().WrapRoundTripper(&poolWatchRoundTripper{
		base: tr,
		peer: cfg.registryKey(),
		max:  maxConns,
	})
	httpClient := &http.Client{
		Transport:     rt,
		CheckRedirect: noFollowRedirects,
	}
	opts := connectClientOptions(cfg.ReadMaxBytes)
	doorBase := cfg.connectBase(cfg.DoorEscrowID)
	hostBase := cfg.connectBase(HostRPCEscrowID)
	authDoor := rpcpbconnect.NewPeerAuthServiceClient(httpClient, doorBase, opts...)
	authHost := authDoor
	if hostBase != doorBase {
		authHost = rpcpbconnect.NewPeerAuthServiceClient(httpClient, hostBase, opts...)
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &PeerConn{
		cfg:      cfg,
		http:     httpClient,
		authDoor: authDoor,
		authHost: authHost,
		key:      cfg.registryKey(),
		ctx:      ctx,
		cancel:   cancel,
		done:     make(chan struct{}),
	}
	p.setState(stateUnauthenticated)
	return p
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
		// Never Start'ed. Close cancels the unused context (finding 2).
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
					// Finding 4: Watch A and token A stay. Retry refresh;
					// do not drop to unauthenticated.
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
	if refreshBackoff > 0 {
		return p.cfg.jitter(refreshBackoff)
	}
	return p.cfg.jitter(p.refreshDelay(exp))
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
	nonce := make([]byte, attachNonceBytes)
	if _, err := crand.Read(nonce); err != nil {
		return nil, time.Time{}, fmt.Errorf("attach nonce: %w", err)
	}
	ts := p.cfg.now().Unix()
	peer := p.cfg.Signer.Address()
	sig, err := SignAttach(p.cfg.Signer, p.cfg.HostAddress, ts, peer, nonce, AttachProtocolVersion, nil)
	if err != nil {
		return nil, time.Time{}, err
	}
	ctx, cancel := context.WithTimeout(p.ctx, DefaultHeightSeedTimeout)
	defer cancel()
	client := p.authDoor
	if p.Ready() {
		// Live renewal: Watch already proved this peer. Do not pin the
		// door escrow (finding 3).
		client = p.authHost
	}
	resp, err := client.Attach(ctx, connect.NewRequest(&rpcpb.AttachRequest{
		PeerAddress:     peer,
		AttachNonce:     nonce,
		ProtocolVersion: AttachProtocolVersion,
		HostAddress:     p.cfg.HostAddress,
		Timestamp:       ts,
		Signature:       sig,
	}))
	if err != nil {
		return nil, time.Time{}, err
	}
	expires := time.Unix(resp.Msg.GetExpiresAt(), 0)
	if resp.Msg.GetExpiresAt() == 0 {
		expires = p.cfg.now().Add(5 * time.Minute)
	}
	return resp.Msg.GetSessionToken(), expires, nil
}

func (p *PeerConn) watch(ctx context.Context, token []byte) error {
	req := connect.NewRequest(&rpcpb.WatchRequest{SessionToken: token})
	SetSessionHeader(req.Header(), token)
	stream, err := p.authHost.Watch(ctx, req)
	if err != nil {
		return err
	}
	defer func() { _ = stream.Close() }()

	type recvResult struct {
		ok  bool
		err error
	}
	recv := make(chan recvResult, 1)
	go func() {
		for stream.Receive() {
			select {
			case recv <- recvResult{ok: true}:
			case <-ctx.Done():
				return
			}
		}
		select {
		case recv <- recvResult{err: stream.Err()}:
		case <-ctx.Done():
		}
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

func (p *PeerConn) Ready() bool {
	return p != nil && p.state.Load() == stateReady && len(p.LiveToken()) > 0
}

// metricPeer is the Prometheus / adoption identity: addr@version, the
// same key as the PeerConn registry (finding 8).
func (p *PeerConn) metricPeer() string {
	if p == nil {
		return ""
	}
	if p.key != "" {
		return p.key
	}
	return p.cfg.registryKey()
}

func (p *PeerConn) setState(s int32) {
	p.state.Store(s)
	name := stateName(s)
	peer := p.metricPeer()
	observability.SetPeerSessionState(peer, name)
	if p.cfg.Adoption != nil {
		p.cfg.Adoption.SetPeerConnReady(peer, s == stateReady)
	}
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
		if p.http != nil {
			p.http.CloseIdleConnections()
		}
		p.setState(stateUnauthenticated)
		p.clearToken()
		peer := p.metricPeer()
		observability.ClearPeerSessionState(peer)
		if p.cfg.Adoption != nil {
			p.cfg.Adoption.SetPeerConnReady(peer, false)
		}
	})
}

// PeerConnRegistered is whether a registry-backed PeerConn is still live
// for this host+version. Tests for finding 1 (shared vs last Release).
func PeerConnRegistered(hostAddress, version string) bool {
	if hostAddress == "" {
		return false
	}
	if version == "" {
		version = "direct"
	}
	peerConnMu.Lock()
	defer peerConnMu.Unlock()
	_, ok := peerConnRegistry[hostAddress+"@"+version]
	return ok
}

// Release drops a registry reference and Closes when the last user leaves.
// Map delete and the last ref drop run under peerConnMu so acquire cannot
// bump a dying conn (finding 2).
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

func (t *poolWatchRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	n := t.inflight.Add(1)
	defer t.inflight.Add(-1)
	if t.max > 0 && int(n) > t.max {
		observability.IncPeerPoolExhausted(t.peer)
	}
	return t.base.RoundTrip(req)
}
