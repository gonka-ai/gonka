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
	// sessionReplacedReleaseAfter is how many times another generation may
	// take this host's session before this process stops dialing. The first
	// loss is retried so an in-flight Attach from the retiring child cannot
	// stick the new child with the loss. The second loss is the retiring
	// child; it must not Attach again.
	sessionReplacedReleaseAfter = 2
	defaultAttachTTL            = 5 * time.Minute
	maxAttachTTL                = time.Hour
	minAttachTTL                = 30 * time.Second
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
	errWatchStale   = errors.New("watch heartbeat stale")
	errAttachTTL    = errors.New("attach expires_at is out of range")
	errNoAttachDoor = errors.New("peer rpc has no attach door")
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
	// GetPayload client reads use the smallest rpcPayloadReadBuckets
	// entry >= PayloadReadLimit (32 / 64 / 256 / 512 MiB). Chat stays
	// DefaultMaxBodySize.
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

	// waitMu / waitCh wake WaitReady. close-and-replace is the broadcast:
	// publishToken, setState(ready), last door gone, Close.
	waitMu sync.Mutex
	waitCh chan struct{}

	// doorWaitMu / doorCh wake the attach loop when a door appears.
	// Separate from waitCh so WaitReady and the no-door wait do not
	// consume each other's broadcast.
	doorWaitMu sync.Mutex
	doorCh     chan struct{}
	noDoorLog  atomic.Int64 // unix nano of the last "no attach door" log

	// budget is the advertised peer-weight bucket (messages_per_min /
	// messages_burst × RPCProcedureWeight). Shared by every RPCClient on
	// this connection. IP Attach is not paced: success refunds.
	budget  peerRPCBudget
	streams peerStreamBudget
	firstOK atomic.Bool
	// replaced counts Watch endings caused by another Attach of this same
	// host key. The second one means a newer generation owns the identity;
	// re-Attach would cancel its Watch.
	replaced atomic.Int32

	// doors are escrow IDs of live RPCClient refs. First Attach (and
	// re-Attach after a full session loss) must use one of these, not
	// HostRPCEscrowID: checkAllow needs a real roster. deadDoors are
	// settled / not-found ids that must not be retried.
	doorMu     sync.Mutex
	doors      map[string]int
	deadDoors  map[string]struct{}
	hadDoor    bool // set by the first real addDoor; empty doors then mean none
	attachDoor string
	// authByDoor caches PeerAuth clients per escrow. The creator door is
	// seeded from authDoor; other doors are built once and dropped with the door.
	authByDoor       map[string]rpcpbconnect.PeerAuthServiceClient
	authByDoorGRPC   map[string]rpcpbconnect.PeerAuthServiceClient
	doorClientsBuilt int
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
		cfg:            cfg,
		http:           httpClient,
		origin:         origin,
		authDoor:       authDoor,
		authHost:       authHost,
		authDoorGRPC:   authDoorGRPC,
		authHostGRPC:   authHostGRPC,
		key:            cfg.registryKey(),
		ctx:            ctx,
		cancel:         cancel,
		done:           make(chan struct{}),
		waitCh:         make(chan struct{}),
		doorCh:         make(chan struct{}),
		doors:          make(map[string]int),
		deadDoors:      make(map[string]struct{}),
		attachDoor:     cfg.DoorEscrowID,
		authByDoor:     make(map[string]rpcpbconnect.PeerAuthServiceClient),
		authByDoorGRPC: make(map[string]rpcpbconnect.PeerAuthServiceClient),
	}
	if validAttachDoorID(cfg.DoorEscrowID) {
		if authDoor != nil {
			p.authByDoor[cfg.DoorEscrowID] = authDoor
		}
		if authDoorGRPC != nil {
			p.authByDoorGRPC[cfg.DoorEscrowID] = authDoorGRPC
		}
	}
	// Zero state is already unauthenticated, so setState would no-op and the
	// gauge would stay unpublished until the first real transition.
	p.publishChildState(stateUnauthenticated)
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

// outboundPeerReleased is process-wide. The retiring child sets it so a
// later RPC cannot open a new PeerConn and take the identity back.
var outboundPeerReleased atomic.Bool

// ReleaseOutboundPeerConns stops every outbound Attach/Watch in this process
// and refuses new ones. The admin listener calls it when versiond retires
// this generation. Safe to call more than once.
func ReleaseOutboundPeerConns() {
	outboundPeerReleased.Store(true)
	peerConnMu.Lock()
	conns := make([]*PeerConn, 0, len(peerConnRegistry))
	for key, pc := range peerConnRegistry {
		delete(peerConnRegistry, key)
		conns = append(conns, pc)
	}
	peerConnMu.Unlock()
	var wg sync.WaitGroup
	for _, pc := range conns {
		wg.Add(1)
		go func(pc *PeerConn) {
			defer wg.Done()
			pc.Close()
		}(pc)
	}
	wg.Wait()
}

func acquirePeerConn(cfg PeerConnConfig) *PeerConn {
	if outboundPeerReleased.Load() {
		pc := NewPeerConn(cfg)
		pc.Close()
		return pc
	}
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
	peerConnMu.Unlock()
	return fresh
}

// Start runs the Attach/Watch loop. Idempotent. Tests that construct via
// NewPeerConn must call Start. Registry-backed conns start from the first
// NewRPCClient so addDoor runs before the first Attach.
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
		if !p.liveSession() && !p.hasAttachDoor() {
			p.clearToken()
			p.noteNoAttachDoor()
			if !p.waitForAttachDoor() {
				return
			}
			backoff = 0
			continue
		}
		if err := p.cfg.sleep(p.ctx, p.cfg.jitter(backoff)); err != nil {
			return
		}
		p.setState(stateAttaching)
		tok, exp, err := p.attach()
		if errors.Is(err, errNoAttachDoor) {
			p.setState(stateUnauthenticated)
			p.clearToken()
			p.noteNoAttachDoor()
			if !p.waitForAttachDoor() {
				return
			}
			backoff = 0
			continue
		}
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
			if peerSessionReplaced(err) && p.replaced.Add(1) >= sessionReplacedReleaseAfter {
				// The other generation's Attach won twice. Further Attach
				// from here only cancels its Watch.
				logging.Warn("peer rpc release: another generation owns this host identity",
					"subsystem", "transport",
					"host", p.cfg.HostAddress,
					"peer", p.metricPeer(),
				)
				p.setState(stateUnauthenticated)
				p.clearToken()
				return
			}
			p.setState(stateUnauthenticated)
			p.clearToken()
			backoff = p.cfg.BackoffMin
		}
	}
}

func (p *PeerConn) waitForAttachDoor() bool {
	if p.hasAttachDoor() {
		return true
	}
	wait := p.doorWaiter()
	if p.hasAttachDoor() {
		return true
	}
	ceiling := p.cfg.BackoffMax
	if ceiling <= 0 {
		ceiling = defaultAttachBackoffMax
	}
	timer := time.NewTimer(ceiling)
	defer timer.Stop()
	select {
	case <-p.ctx.Done():
		return false
	case <-wait:
		return p.ctx.Err() == nil
	case <-timer.C:
		return p.ctx.Err() == nil
	}
}

func (p *PeerConn) noteNoAttachDoor() {
	if p == nil {
		return
	}
	now := p.cfg.now().UnixNano()
	interval := int64(p.cfg.BackoffMax)
	if interval <= 0 {
		interval = int64(defaultAttachBackoffMax)
	}
	prev := p.noDoorLog.Load()
	if prev != 0 && now-prev < interval {
		return
	}
	if !p.noDoorLog.CompareAndSwap(prev, now) {
		return
	}
	logging.Warn("peer rpc has no attach door",
		"subsystem", "transport",
		"host", p.cfg.HostAddress,
	)
}

func (p *PeerConn) wakeDoors() {
	if p == nil {
		return
	}
	p.doorWaitMu.Lock()
	old := p.doorCh
	p.doorCh = make(chan struct{})
	p.doorWaitMu.Unlock()
	if old != nil {
		close(old)
	}
}

func (p *PeerConn) doorWaiter() <-chan struct{} {
	p.doorWaitMu.Lock()
	defer p.doorWaitMu.Unlock()
	if p.doorCh == nil {
		p.doorCh = make(chan struct{})
	}
	return p.doorCh
}

func (p *PeerConn) serveWatch(tok []byte, exp time.Time) error {
	// Refresh is scheduled on its own deadline so it still fires while Watch
	// is down. Shutting down and EOF reopen Watch with the same token.
	// session replaced, session expired, and any other Unauthenticated end
	// the loop so the caller clears the token and Attaches again.
	refreshBackoff := time.Duration(0)
	refreshDue := time.Now().Add(p.nextRefreshWait(exp, 0))
	var (
		cancelWatch context.CancelFunc
		watchErr    chan error
	)
	stopWatch := func() {
		if cancelWatch == nil {
			return
		}
		cancelWatch()
		<-watchErr
		cancelWatch = nil
		watchErr = nil
	}
	startWatch := func(token []byte) {
		stopWatch()
		watchCtx, cancel := context.WithCancel(p.ctx)
		cancelWatch = cancel
		watchErr = make(chan error, 1)
		go func(token []byte) {
			watchErr <- p.watch(watchCtx, token)
		}(append([]byte(nil), token...))
	}
	startWatch(tok)
	defer stopWatch()

	for {
		var watchC <-chan error
		if watchErr != nil {
			watchC = watchErr
		}
		var reopenTimer *time.Timer
		var reopenC <-chan time.Time
		if cancelWatch == nil {
			reopenTimer = time.NewTimer(p.reopenWait())
			reopenC = reopenTimer.C
		}
		refreshWait := time.Until(refreshDue)
		if refreshWait < 0 {
			refreshWait = 0
		}
		refreshTimer := time.NewTimer(refreshWait)
		select {
		case <-p.ctx.Done():
			stopTimer(refreshTimer)
			stopTimer(reopenTimer)
			stopWatch()
			return p.ctx.Err()
		case err := <-watchC:
			stopTimer(refreshTimer)
			stopTimer(reopenTimer)
			if cancelWatch != nil {
				cancelWatch()
			}
			cancelWatch = nil
			watchErr = nil
			if p.ctx.Err() != nil {
				return p.ctx.Err()
			}
			if err == nil {
				err = io.EOF
			}
			// A dead HTTP/2 origin has to leave this loop so the caller
			// can probe HTTP/1.1. Shutting down and a clean EOF stay here
			// and reopen Watch with the same token.
			if !isRPCH2TransportMiss(err) && watchReopen(err) {
				continue
			}
			p.incReattach(reattachReasonWatch)
			return err
		case <-reopenC:
			stopTimer(refreshTimer)
			startWatch(tok)
		case <-refreshTimer.C:
			stopTimer(reopenTimer)
			newTok, newExp, err := p.attach()
			p.incAttach(err)
			if err != nil {
				if isRPCH2TransportMiss(err) {
					// Origin is gone (listen/RST/ALPN/half-open). End
					// Watch; do not flip setH2(false) until the stream
					// has returned. Outer loop probes then HTTP/1.1.
					// A slow Attach (DeadlineExceeded) is not this:
					// keep Watch and retry refresh.
					stopWatch()
					p.incReattach(reattachReasonWatch)
					return err
				}
				// Watch and token stay. Retry refresh; do not drop to
				// unauthenticated.
				refreshBackoff = nextAttachBackoff(refreshBackoff, p.cfg.BackoffMin, p.cfg.BackoffMax)
				refreshDue = time.Now().Add(p.nextRefreshWait(exp, refreshBackoff))
				continue
			}
			p.incReattach(reattachReasonTTL)
			p.publishToken(newTok, newExp)
			tok, exp = newTok, newExp
			refreshBackoff = 0
			refreshDue = time.Now().Add(p.nextRefreshWait(exp, 0))
			startWatch(tok)
		}
	}
}

func (p *PeerConn) reopenWait() time.Duration {
	if p.cfg.BackoffMin > 0 {
		return p.cfg.BackoffMin
	}
	return defaultAttachBackoffMin
}

func stopTimer(t *time.Timer) {
	if t == nil {
		return
	}
	if !t.Stop() {
		select {
		case <-t.C:
		default:
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
	// Probe + HTTP/1.1 share one DefaultAttachTimeout. h2 is capped at
	// h2ProbeTimeout; 1.1 gets the remainder (≈5s if the probe returns
	// immediately, ≈4s after a 1s blackhole).
	overall, cancel := context.WithTimeout(p.ctx, DefaultAttachTimeout)
	defer cancel()
	if p.liveSession() {
		msg, err := p.newAttachRequest()
		if err != nil {
			return nil, time.Time{}, err
		}
		return p.attachOnce(overall, p.peerAuthClient(true), msg)
	}
	h2Tried := false
	var lastErr error
	for {
		if err := overall.Err(); err != nil {
			if lastErr != nil {
				return nil, time.Time{}, lastErr
			}
			return nil, time.Time{}, err
		}
		door := p.pickDoor()
		if door == "" {
			if lastErr != nil {
				return nil, time.Time{}, lastErr
			}
			return nil, time.Time{}, errNoAttachDoor
		}
		msg, err := p.newAttachRequest()
		if err != nil {
			return nil, time.Time{}, err
		}
		if !h2Tried && p.shouldProbeH2() {
			p.origin.setH2(true)
			h2ctx, h2cancel := context.WithTimeout(overall, p.h2ProbeTimeout())
			tok, exp, err := p.attachOnce(h2ctx, p.doorAuthClient(door), msg)
			h2cancel()
			h2Tried = true
			if err == nil {
				return tok, exp, nil
			}
			lastErr = err
			if p.ctx.Err() != nil {
				return nil, time.Time{}, err
			}
			if isDeadDoorError(err) {
				p.killDoor(door)
				continue
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
		tok, exp, err := p.attachOnce(overall, p.doorAuthClient(door), msg)
		if err == nil {
			return tok, exp, nil
		}
		lastErr = err
		if isDeadDoorError(err) {
			p.killDoor(door)
			continue
		}
		return nil, time.Time{}, err
	}
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
		// Timeout/cancel of this Attach must win over a racing RST /
		// INTERNAL_ERROR so a slow live refresh is not an h2 miss.
		if ctxErr := ctx.Err(); isContextDone(ctxErr) {
			return nil, time.Time{}, ctxErr
		}
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
	if host {
		if p.useGRPC() && p.authHostGRPC != nil {
			return p.authHostGRPC
		}
		return p.authHost
	}
	door := p.pickDoor()
	if door == "" {
		door = p.cfg.DoorEscrowID
	}
	return p.doorAuthClient(door)
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
	p.wakeWaiters()
}

func (p *PeerConn) clearToken() {
	p.token.Store(nil)
	p.expires.Store(0)
}

func (p *PeerConn) wakeWaiters() {
	if p == nil {
		return
	}
	p.waitMu.Lock()
	old := p.waitCh
	p.waitCh = make(chan struct{})
	p.waitMu.Unlock()
	if old != nil {
		close(old)
	}
}

func (p *PeerConn) waiter() <-chan struct{} {
	p.waitMu.Lock()
	defer p.waitMu.Unlock()
	if p.waitCh == nil {
		p.waitCh = make(chan struct{})
	}
	return p.waitCh
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
	if prev == s {
		return
	}
	p.publishChildState(s)
	if s == stateReady && prev != stateReady {
		p.wakeWaiters()
	}
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
		// Close idle HTTP/1.1 conns this PeerConn owns. Do not CloseIdle
		// the pooled h2 transport: overlay muxes every peer onto one TCP.
		if p.http != nil {
			p.http.CloseIdleConnections()
		}
		p.setState(stateUnauthenticated)
		p.clearToken()
		p.wakeWaiters()
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

// watchReopen is a Watch that ended because this child is going away or the
// stream closed cleanly. The token is still good on the other children, so
// the client opens Watch again and does not Attach.
func watchReopen(err error) bool {
	if err == nil || errors.Is(err, io.EOF) {
		return true
	}
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		return false
	}
	return strings.Contains(err.Error(), "host shutting down")
}

func peerSessionReplaced(err error) bool {
	if err == nil || connect.CodeOf(err) != connect.CodeUnauthenticated {
		return false
	}
	return strings.Contains(err.Error(), "session replaced")
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
