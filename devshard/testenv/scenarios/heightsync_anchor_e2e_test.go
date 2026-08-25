package scenarios

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"common/chainoracle/blocks"
	"devshard/bridge"
	"devshard/heightsync"
	"devshard/host"
	"devshard/internal/testutil"
	"devshard/logging"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/transport"
	"devshard/types"
	"devshard/user"
)

const hsAnchorE2EEscrowID = "9001"
const hsE2ERoutePrefix = "/devshard/v2"

func registerHeightSyncServer(g *echo.Group, srv *transport.Server) {
	withAuth := func(recordChatTerminal bool, handler echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			wrapped := srv.RateLimitMiddleware(recordChatTerminal)(handler)
			return srv.AuthMiddleware(wrapped)(c)
		}
	}
	g.POST("/sessions/:id/chat/completions", withAuth(true, srv.HandleInference))
	g.POST("/sessions/:id/height-sync", withAuth(false, srv.HandleHeightSync))
	g.POST("/sessions/:id/heightsync/repair", withAuth(false, srv.HandleHeightSyncRepair))
	g.POST("/sessions/:id/verify-timeout", withAuth(false, srv.HandleVerifyTimeout))
	g.POST("/sessions/:id/challenge-receipt", withAuth(false, srv.HandleChallengeReceipt))
	g.POST("/sessions/:id/gossip/nonce", withAuth(false, srv.HandleGossipNonce))
	g.POST("/sessions/:id/gossip/txs", withAuth(false, srv.HandleGossipTxs))
	g.GET("/sessions/:id/diffs", srv.HandleGetDiffs)
	g.GET("/sessions/:id/mempool", srv.HandleGetMempool)
	g.GET("/sessions/:id/signatures", srv.HandleGetSignatures)
}

// scenarioBridge is a minimal MainnetBridge for wiring multi-host HTTP tests.
type scenarioBridge struct {
	escrow *bridge.EscrowInfo
	hosts  map[string]*bridge.HostInfo
}

func (m *scenarioBridge) GetEscrow(_ string) (*bridge.EscrowInfo, error) { return m.escrow, nil }
func (m *scenarioBridge) GetHostInfo(addr string) (*bridge.HostInfo, error) {
	info, ok := m.hosts[addr]
	if !ok {
		return nil, bridge.ErrParticipantNotFound
	}
	return info, nil
}
func (m *scenarioBridge) GetValidationThreshold(uint64, string) (*bridge.Decimal, error) {
	return nil, bridge.ErrNotImplemented
}
func (m *scenarioBridge) VerifyWarmKey(_, _ string) (bool, error)   { return false, nil }
func (m *scenarioBridge) OnEscrowCreated(_ bridge.EscrowInfo) error { return bridge.ErrNotImplemented }
func (m *scenarioBridge) OnSettlementProposed(_ string, _ []byte, _ uint64) error {
	return bridge.ErrNotImplemented
}
func (m *scenarioBridge) OnSettlementFinalized(_ string) error { return bridge.ErrNotImplemented }
func (m *scenarioBridge) SubmitDisputeState(_ string, _ []byte, _ uint64, _ map[uint32][]byte) error {
	return bridge.ErrNotImplemented
}

type staticOracle struct {
	hdr *blocks.Header
}

func (o *staticOracle) Latest(ctx context.Context) (*blocks.Header, error) {
	_ = ctx
	h := *o.hdr
	h.BlockHash = append([]byte(nil), o.hdr.BlockHash...)
	return &h, nil
}

func (o *staticOracle) At(ctx context.Context, height int64) (*blocks.Header, error) {
	_ = ctx
	if o.hdr != nil && height == o.hdr.Height {
		return o.Latest(ctx)
	}
	return nil, errors.New("no header at height")
}

func (o *staticOracle) Prove(context.Context, string, int64) (*blocks.Proof, error) {
	return nil, errors.New("not implemented")
}

func (o *staticOracle) Subscribe(context.Context, int64) (<-chan *blocks.Header, error) {
	ch := make(chan *blocks.Header)
	close(ch)
	return ch, nil
}

// sharedStoppingOracle simulates loss of the height-sync HTTP feed when heightsyncd stops:
// every host and the user client share one instance; SetStopped(true) makes Latest fail so
// AnchorScheduler degrades to Omit on cadence paths (spec §9 / §13).
type sharedStoppingOracle struct {
	mu      sync.Mutex
	hdr     *blocks.Header
	stopped bool
}

func newSharedStoppingOracle(height int64, hash []byte) *sharedStoppingOracle {
	return &sharedStoppingOracle{hdr: &blocks.Header{
		Height:    height,
		ChainID:   "gonka-testenv-1",
		BlockHash: append([]byte(nil), hash...),
	}}
}

func (o *sharedStoppingOracle) SetStopped(v bool) {
	o.mu.Lock()
	o.stopped = v
	o.mu.Unlock()
}

func (o *sharedStoppingOracle) Latest(ctx context.Context) (*blocks.Header, error) {
	_ = ctx
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.stopped {
		return nil, errors.New("block oracle unavailable (height-sync feed stopped)")
	}
	if o.hdr == nil {
		return nil, nil
	}
	h := *o.hdr
	h.BlockHash = append([]byte(nil), o.hdr.BlockHash...)
	return &h, nil
}

func (o *sharedStoppingOracle) At(ctx context.Context, height int64) (*blocks.Header, error) {
	_ = ctx
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.stopped {
		return nil, errors.New("block oracle unavailable (height-sync feed stopped)")
	}
	if o.hdr != nil && height == o.hdr.Height {
		h := *o.hdr
		h.BlockHash = append([]byte(nil), o.hdr.BlockHash...)
		return &h, nil
	}
	return nil, errors.New("no header at height")
}

func (o *sharedStoppingOracle) Prove(context.Context, string, int64) (*blocks.Proof, error) {
	return nil, errors.New("not implemented")
}

func (o *sharedStoppingOracle) Subscribe(context.Context, int64) (<-chan *blocks.Header, error) {
	ch := make(chan *blocks.Header)
	close(ch)
	return ch, nil
}

// Stale reports whether the height-sync feed is unavailable (spec §17).
func (o *sharedStoppingOracle) Stale() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.stopped
}

type fourHostStack struct {
	Session   *user.Session
	Servers   []*transport.Server
	Oracle    *staticOracle
	UserAddr  string
	HostAddrs []string
	httpSrvs  []*httptest.Server
}

type oneHostRestartStack struct {
	Session       *user.Session
	Server        *transport.Server
	Oracle        *staticOracle
	UserAddr      string
	PrivateKeyHex string
	Bridge        *scenarioBridge
	StoragePath   string
	httpSrv       *httptest.Server
}

type repairTimingStack struct {
	user     *signing.Secp256k1Signer
	hosts    []*signing.Secp256k1Signer
	oracles  []*staticOracle
	servers  []*transport.Server
	httpSrvs []*httptest.Server
	hostObjs []*host.Host
}

type testLogEntry struct {
	msg string
	kv  map[string]string
}

type captureLogger struct {
	mu      sync.Mutex
	entries []testLogEntry
}

func (l *captureLogger) Info(msg string, keyvals ...any)  { l.append(msg, keyvals...) }
func (l *captureLogger) Error(msg string, keyvals ...any) { l.append(msg, keyvals...) }
func (l *captureLogger) Warn(msg string, keyvals ...any)  { l.append(msg, keyvals...) }
func (l *captureLogger) Debug(msg string, keyvals ...any) { l.append(msg, keyvals...) }

func (l *captureLogger) append(msg string, keyvals ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	kv := make(map[string]string)
	for i := 0; i+1 < len(keyvals); i += 2 {
		k := fmt.Sprint(keyvals[i])
		kv[k] = fmt.Sprint(keyvals[i+1])
	}
	l.entries = append(l.entries, testLogEntry{msg: msg, kv: kv})
}

func (l *captureLogger) snapshot() []testLogEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]testLogEntry, len(l.entries))
	copy(out, l.entries)
	return out
}

type discardLogger struct{}

func (*discardLogger) Info(string, ...any)  {}
func (*discardLogger) Error(string, ...any) {}
func (*discardLogger) Warn(string, ...any)  {}
func (*discardLogger) Debug(string, ...any) {}

func installCaptureLogger(t *testing.T) *captureLogger {
	t.Helper()
	cl := &captureLogger{}
	logging.SetLogger(cl)
	t.Cleanup(func() {
		logging.SetLogger(&discardLogger{})
	})
	return cl
}

func defaultInferenceParams() user.InferenceParams {
	return user.InferenceParams{
		Model:       "llama",
		Prompt:      testutil.TestPrompt,
		InputLength: 100,
		MaxTokens:   50,
		StartedAt:   1000,
	}
}

func extractHeightSyncRequestEmitModes(entries []testLogEntry) []string {
	var out []string
	for _, e := range entries {
		if e.msg != "heightsync: emit" {
			continue
		}
		if e.kv["direction"] != "request" {
			continue
		}
		mode := strings.ToLower(strings.TrimSpace(e.kv["mode"]))
		if mode == "" {
			continue
		}
		out = append(out, mode)
	}
	return out
}

func extractHeightSyncRequestEmitEvents(entries []testLogEntry) []testLogEntry {
	var out []testLogEntry
	for _, e := range entries {
		if e.msg != "heightsync: emit" {
			continue
		}
		if e.kv["direction"] != "request" {
			continue
		}
		out = append(out, e)
	}
	return out
}

func extractHostInboundPeerEvents(entries []testLogEntry, userAddr string) []testLogEntry {
	var out []testLogEntry
	for _, e := range entries {
		if e.msg != "heightsync: peer attestation received" {
			continue
		}
		if e.kv["direction"] != "request" {
			continue
		}
		if e.kv["peer_id"] != userAddr {
			continue
		}
		out = append(out, e)
	}
	return out
}

func staticOracleWith(height int64, hash []byte) *staticOracle {
	return &staticOracle{hdr: &blocks.Header{
		Height:    height,
		ChainID:   "gonka-testenv-1",
		BlockHash: append([]byte(nil), hash...),
	}}
}

// setupFourHostHTTPHeightSyncFromChainOracles builds the standard four-host httptest stack.
// Host schedulers use hostSchedOracle[i]; server debug/log oracle uses hostLogOracle[i].
// stackMeta is stored on fourHostStack.Oracle for tests that need a static snapshot (nil ok).
func setupFourHostHTTPHeightSyncFromChainOracles(t *testing.T, hostSchedOracle, hostLogOracle []blocks.BlockOracle, clientOracle, clientLogOracle blocks.BlockOracle, stackMeta *staticOracle, tweak ...func(*transport.ClientConfig)) *fourHostStack {
	t.Helper()
	require.Len(t, hostSchedOracle, 4)
	require.Len(t, hostLogOracle, 4)
	require.NotNil(t, clientOracle)
	require.NotNil(t, clientLogOracle)
	for i := range hostSchedOracle {
		require.NotNil(t, hostSchedOracle[i], "host sched oracle %d must be non-nil", i)
		require.NotNil(t, hostLogOracle[i], "host log oracle %d must be non-nil", i)
	}

	userSigner := testutil.MustGenerateKey(t)
	hostSigners := make([]*signing.Secp256k1Signer, 4)
	hostAddrs := make([]string, 4)
	for i := range hostSigners {
		hostSigners[i] = testutil.MustGenerateKey(t)
		hostAddrs[i] = hostSigners[i].Address()
	}
	group := testutil.MakeGroup(hostSigners)
	cfg := types.SessionConfigWithPrice(4, 1)
	verifier := signing.NewSecp256k1Verifier()
	warmResolve := func(_, _ string) (bool, error) { return false, nil }

	brHosts := make(map[string]*bridge.HostInfo)
	slots := make([]string, len(group))
	for i, slot := range group {
		slots[i] = slot.ValidatorAddress
	}

	var servers []*transport.Server
	var httpSrvs []*httptest.Server
	for i := range hostSigners {
		smStore := testutil.MustMemoryStore(t, hsAnchorE2EEscrowID, userSigner.Address(), cfg, group, 100_000)
		sm, err := state.NewStateMachine(hsAnchorE2EEscrowID, cfg, group, 100_000, userSigner.Address(), verifier, smStore, state.WithWarmKeyResolver(warmResolve))
		require.NoError(t, err)
		st := storage.NewMemory()
		require.NoError(t, st.CreateSession(storage.CreateSessionParams{
			EscrowID:       hsAnchorE2EEscrowID,
			Version:        testutil.RuntimeTestVersion,
			Config:         cfg,
			Group:          group,
			InitialBalance: 100_000,
		}))
		h, err := host.NewHost(sm, hostSigners[i], stub.NewInferenceEngine(), hsAnchorE2EEscrowID, group, nil,
			host.WithGrace(10_000), host.WithStorage(st), host.WithChainOracle(hostSchedOracle[i]))
		require.NoError(t, err)

		hostSched := heightsync.MustNewAnchorSchedulerFromOracle(8, 4, hostSchedOracle[i])
		srv, err := transport.NewServer(h, st, verifier, userSigner.Address(), transport.WithHeightSync(hostSched, hostLogOracle[i]))
		require.NoError(t, err)
		e := echo.New()
		g := e.Group(hsE2ERoutePrefix)
		registerHeightSyncServer(g, srv)
		ts := httptest.NewServer(e)
		httpSrvs = append(httpSrvs, ts)
		servers = append(servers, srv)
		brHosts[hostSigners[i].Address()] = &bridge.HostInfo{Address: hostSigners[i].Address(), URL: ts.URL}
	}

	br := &scenarioBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID:       hsAnchorE2EEscrowID,
			Amount:         100_000,
			CreatorAddress: userSigner.Address(),
			Slots:          slots,
			TokenPrice:     1,
			AppHash:        make([]byte, 32),
		},
		hosts: brHosts,
	}
	clientSched := heightsync.MustNewAnchorSchedulerFromOracle(8, 4, clientOracle)
	cc := transport.DefaultClientConfig()
	cc.HeightSync = clientSched
	cc.HeightSyncLogOracle = clientLogOracle
	for _, f := range tweak {
		if f != nil {
			f(&cc)
		}
	}
	extra := &cc
	sess, _, err := user.NewHTTPSession(user.HTTPSessionConfig{
		PrivateKeyHex:     userSigner.PrivateKeyHex(),
		EscrowID:          hsAnchorE2EEscrowID,
		Bridge:            br,
		RoutePrefix:       hsE2ERoutePrefix,
		StoragePath:       filepath.Join(t.TempDir(), "session.db"),
		ExtraClientConfig: extra,
	})
	require.NoError(t, err)

	st := &fourHostStack{
		Session:   sess,
		Servers:   servers,
		Oracle:    stackMeta,
		UserAddr:  userSigner.Address(),
		HostAddrs: hostAddrs,
		httpSrvs:  httpSrvs,
	}
	t.Cleanup(func() {
		_ = sess.Close()
		for _, ts := range httpSrvs {
			ts.Close()
		}
	})
	return st
}

func setupFourHostHTTPHeightSyncWithOracles(t *testing.T, hostOracles []*staticOracle, clientOracle *staticOracle, tweak ...func(*transport.ClientConfig)) *fourHostStack {
	t.Helper()
	require.Len(t, hostOracles, 4)
	require.NotNil(t, clientOracle)
	hsched := make([]blocks.BlockOracle, 4)
	hlog := make([]blocks.BlockOracle, 4)
	for i := range hostOracles {
		require.NotNil(t, hostOracles[i].hdr, "host oracle header %d must be non-nil", i)
		hsched[i] = hostOracles[i]
		hlog[i] = hostOracles[i]
	}
	return setupFourHostHTTPHeightSyncFromChainOracles(t, hsched, hlog, clientOracle, clientOracle, clientOracle, tweak...)
}

// setupFourHostHTTPHeightSyncStoppingOracle wires one sharedStoppingOracle into every
// scheduler and log oracle, modelling heightsyncd disappearing for all consumers at once.
func setupFourHostHTTPHeightSyncStoppingOracle(t *testing.T) (*fourHostStack, *sharedStoppingOracle) {
	t.Helper()
	shared := newSharedStoppingOracle(100, []byte{0xab, 0xcd, 0xef, 0x42})
	h := []blocks.BlockOracle{shared, shared, shared, shared}
	st := setupFourHostHTTPHeightSyncFromChainOracles(t, h, h, shared, shared, nil)
	return st, shared
}

func setupFourHostHTTPHeightSync(t *testing.T) *fourHostStack {
	t.Helper()
	base := staticOracleWith(100, []byte{0xab, 0xcd, 0xef, 0x42})
	hostOracles := []*staticOracle{
		staticOracleWith(base.hdr.Height, base.hdr.BlockHash),
		staticOracleWith(base.hdr.Height, base.hdr.BlockHash),
		staticOracleWith(base.hdr.Height, base.hdr.BlockHash),
		staticOracleWith(base.hdr.Height, base.hdr.BlockHash),
	}
	return setupFourHostHTTPHeightSyncWithOracles(t, hostOracles, base)
}

func setupOneHostHTTPHeightSyncRestartStack(t *testing.T) *oneHostRestartStack {
	t.Helper()
	const escrowID = "9002"
	oracle := staticOracleWith(100, []byte{0xab, 0xcd, 0xef, 0x43})
	userSigner := testutil.MustGenerateKey(t)
	hostSigner := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup([]*signing.Secp256k1Signer{hostSigner})
	cfg := types.SessionConfigWithPrice(1, 1)
	verifier := signing.NewSecp256k1Verifier()
	warmResolve := func(_, _ string) (bool, error) { return false, nil }

	smStore := testutil.MustMemoryStore(t, escrowID, userSigner.Address(), cfg, group, 100_000)
	sm, err := state.NewStateMachine(escrowID, cfg, group, 100_000, userSigner.Address(), verifier, smStore,
		state.WithWarmKeyResolver(warmResolve))
	require.NoError(t, err)
	hostStore := storage.NewMemory()
	require.NoError(t, hostStore.CreateSession(storage.CreateSessionParams{
		EscrowID:       escrowID,
		Version:        testutil.RuntimeTestVersion,
		Config:         cfg,
		Group:          group,
		InitialBalance: 100_000,
	}))
	h, err := host.NewHost(sm, hostSigner, stub.NewInferenceEngine(), escrowID, group, nil,
		host.WithGrace(10_000), host.WithStorage(hostStore), host.WithChainOracle(oracle))
	require.NoError(t, err)

	hostSched := heightsync.MustNewAnchorSchedulerFromOracle(8, 1, oracle)
	srv, err := transport.NewServer(h, hostStore, verifier, userSigner.Address(), transport.WithHeightSync(hostSched, oracle))
	require.NoError(t, err)
	e := echo.New()
	g := e.Group(hsE2ERoutePrefix)
	registerHeightSyncServer(g, srv)
	ts := httptest.NewServer(e)

	br := &scenarioBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID:       escrowID,
			Amount:         100_000,
			CreatorAddress: userSigner.Address(),
			Slots:          []string{hostSigner.Address()},
			TokenPrice:     1,
			AppHash:        make([]byte, 32),
		},
		hosts: map[string]*bridge.HostInfo{
			hostSigner.Address(): {Address: hostSigner.Address(), URL: ts.URL},
		},
	}

	st := &oneHostRestartStack{
		Server:        srv,
		Oracle:        oracle,
		UserAddr:      userSigner.Address(),
		PrivateKeyHex: userSigner.PrivateKeyHex(),
		Bridge:        br,
		StoragePath:   filepath.Join(t.TempDir(), "session.db"),
		httpSrv:       ts,
	}
	st.Session = st.newHTTPSession(t)
	t.Cleanup(func() {
		if st.Session != nil {
			_ = st.Session.Close()
		}
		ts.Close()
	})
	return st
}

func (st *oneHostRestartStack) newHTTPSession(t *testing.T) *user.Session {
	t.Helper()
	clientSched := heightsync.MustNewAnchorSchedulerFromOracle(8, 1, st.Oracle)
	cc := transport.DefaultClientConfig()
	cc.HeightSync = clientSched
	cc.HeightSyncLogOracle = st.Oracle
	sess, _, err := user.NewHTTPSession(user.HTTPSessionConfig{
		PrivateKeyHex:     st.PrivateKeyHex,
		EscrowID:          st.Bridge.escrow.EscrowID,
		Bridge:            st.Bridge,
		RoutePrefix:       hsE2ERoutePrefix,
		StoragePath:       st.StoragePath,
		ExtraClientConfig: &cc,
	})
	require.NoError(t, err)
	return sess
}

func setupFourHostHTTPRepairTimingStack(t *testing.T) *repairTimingStack {
	t.Helper()
	const escrowID = "9003"
	const numHosts = 4
	userSigner := testutil.MustGenerateKey(t)
	hostSigners := make([]*signing.Secp256k1Signer, numHosts)
	for i := range hostSigners {
		hostSigners[i] = testutil.MustGenerateKey(t)
	}
	group := testutil.MakeGroup(hostSigners)
	cfg := testutil.DefaultConfig(numHosts)
	verifier := signing.NewSecp256k1Verifier()
	repairCfg := heightsync.RepairConfig{Stagger: 5 * time.Millisecond, MaxProbesPerWindow: 2}

	st := &repairTimingStack{
		user:    userSigner,
		hosts:   hostSigners,
		oracles: make([]*staticOracle, numHosts),
	}
	for i := range hostSigners {
		or := staticOracleWith(500, []byte{0xbb, byte(i)})
		st.oracles[i] = or
		smStore := testutil.MustMemoryStore(t, escrowID, userSigner.Address(), cfg, group, 100_000)
		sm, err := state.NewStateMachine(escrowID, cfg, group, 100_000, userSigner.Address(), verifier, smStore)
		require.NoError(t, err)
		hostStore := storage.NewMemory()
		require.NoError(t, hostStore.CreateSession(storage.CreateSessionParams{
			EscrowID:       escrowID,
			Version:        testutil.RuntimeTestVersion,
			Config:         cfg,
			Group:          group,
			InitialBalance: 100_000,
		}))
		h, err := host.NewHost(sm, hostSigners[i], stub.NewInferenceEngine(), escrowID, group, nil,
			host.WithStorage(hostStore),
			host.WithChainOracle(or),
			host.WithRepairConfig(repairCfg),
		)
		require.NoError(t, err)
		srv, err := transport.NewServer(h, hostStore, verifier, userSigner.Address())
		require.NoError(t, err)
		e := echo.New()
		registerHeightSyncServer(e.Group(hsE2ERoutePrefix), srv)
		ts := httptest.NewServer(e)
		st.hostObjs = append(st.hostObjs, h)
		st.servers = append(st.servers, srv)
		st.httpSrvs = append(st.httpSrvs, ts)
	}
	t.Cleanup(func() {
		for _, ts := range st.httpSrvs {
			ts.Close()
		}
	})
	return st
}

func (st *repairTimingStack) wireRepairPeersFrom(prober int) {
	peers := make(map[int]*transport.HTTPClient, len(st.httpSrvs))
	for slot, ts := range st.httpSrvs {
		peers[slot] = transport.NewHTTPClient(ts.URL, "9003", st.user, transport.ClientConfig{
			QueryTimeout: 200 * time.Millisecond,
			RoutePrefix:  hsE2ERoutePrefix,
		})
	}
	st.servers[prober].SetPeerClients(peers)
}

func (st *repairTimingStack) applyDiffsToHosts(t *testing.T, diffs ...types.Diff) {
	t.Helper()
	ctx := context.Background()
	for i, h := range st.hostObjs {
		resp, err := h.HandleRequest(ctx, host.HostRequest{Diffs: diffs})
		require.NoError(t, err, "host %d", i)
		require.Equal(t, diffs[len(diffs)-1].Nonce, resp.Nonce, "host %d", i)
	}
}

func repairTimingHeartbeatDiff(t *testing.T, signer signing.Signer, nonce, turnSeq, height uint64) types.Diff {
	t.Helper()
	return testutil.SignDiff(t, signer, "9003", nonce, []*types.DevshardTx{{
		Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{
			TurnSeq:           turnSeq,
			ObservedHeight:    height,
			ObservedBlockHash: []byte{0xaa},
			SlotsNum:          4,
			Reason:            string(heightsync.ReasonQuietSession),
		}},
	}})
}

// syncHostsFromSession applies the user's signed diff chain to every host so
// round-robin SendInference stays consistent with gossip-less multi-host tests
// (see protocol/http_test.go catch-up patterns).
func syncHostsFromSession(t *testing.T, st *fourHostStack) {
	t.Helper()
	diffs := st.Session.Diffs()
	for _, srv := range st.Servers {
		srv.Host().ApplyCatchUpDiffs(diffs)
	}
}

func hostInferenceResponseAnchors(atts []heightsync.AnchorAttestation) []heightsync.AnchorAttestation {
	out := make([]heightsync.AnchorAttestation, 0, len(atts))
	for _, a := range atts {
		if a.Direction != "response" || len(a.MainnetBlockHash) == 0 {
			continue
		}
		if strings.Contains(strings.ToLower(a.SourceMessage), "height-sync") {
			continue
		}
		out = append(out, a)
	}
	return out
}

func countOutboundRequestAnchors(t *testing.T, sess *user.Session, userAddr string) int {
	t.Helper()
	n := 0
	for _, cl := range sess.Clients() {
		hc, ok := cl.(*transport.HTTPClient)
		require.True(t, ok)
		ar := hc.HeightSyncAuditRing()
		if ar == nil {
			continue
		}
		for _, a := range ar.List(userAddr) {
			if a.Direction == "request" && len(a.MainnetBlockHash) > 0 {
				n++
			}
		}
	}
	return n
}

func wantRequestAnchorAtNonce(nonce int) bool {
	switch {
	case nonce >= 1 && nonce <= 4:
		return true
	case nonce >= 8 && nonce <= 11:
		return true
	case nonce == 16:
		return true
	default:
		return false
	}
}

func countInboundUserAnchorsOnHost(t *testing.T, srv *transport.Server, userAddr string) int {
	t.Helper()
	ar := srv.HeightSyncAuditRing()
	if ar == nil {
		return 0
	}
	n := 0
	for _, a := range ar.List(userAddr) {
		if a.Direction == "request" && len(a.MainnetBlockHash) > 0 {
			n++
		}
	}
	return n
}

func countInboundUserAnchorsWithTag(t *testing.T, srv *transport.Server, userAddr string, tag heightsync.AnchorCadenceTag) int {
	t.Helper()
	ar := srv.HeightSyncAuditRing()
	if ar == nil {
		return 0
	}
	n := 0
	for _, a := range ar.List(userAddr) {
		if a.Direction == "request" && a.Tag == tag && len(a.MainnetBlockHash) > 0 {
			n++
		}
	}
	return n
}

func hostIdxForNonce(n uint64) int {
	return int(n % 4)
}

// setupFourHostHTTPHeightSyncCourier wires courier-mode user (PeerTipOracleSource, no local follower).
func setupFourHostHTTPHeightSyncCourier(t *testing.T, hostOracles []*staticOracle, tweak ...func(*transport.ClientConfig)) (*fourHostStack, *transport.HeightSyncPeerTips) {
	t.Helper()
	peerTips := transport.NewHeightSyncPeerTips()
	src := heightsync.NewPeerTipOracleSource(peerTips, peerTips.Freshness)
	sched := heightsync.MustNewAnchorScheduler(8, 4, src)
	dummyClient := staticOracleWith(1, []byte{0x01})
	var tweaks []func(*transport.ClientConfig)
	tweaks = append(tweaks, func(cc *transport.ClientConfig) {
		cc.HeightSync = sched
		cc.HeightSyncPeerTips = peerTips
	})
	tweaks = append(tweaks, tweak...)
	st := setupFourHostHTTPHeightSyncWithOracles(t, hostOracles, dummyClient, tweaks...)
	require.Same(t, peerTips, peerTipsFromSession(t, st.Session),
		"courier peer-tip cache must be shared across session HTTP clients")
	return st, peerTips
}

func peerTipsFromSession(t *testing.T, sess *user.Session) *transport.HeightSyncPeerTips {
	t.Helper()
	var shared *transport.HeightSyncPeerTips
	for _, cl := range sess.Clients() {
		hc, ok := cl.(*transport.HTTPClient)
		require.True(t, ok)
		pt := hc.HeightSyncPeerTips()
		require.NotNil(t, pt, "height sync enabled clients must have peer tips")
		if shared == nil {
			shared = pt
			continue
		}
		require.Same(t, shared, pt)
	}
	require.NotNil(t, shared)
	return shared
}

// recordCourierPeerTip seeds the courier cache with a verified-shaped blob so
// MaxFresh/Carry accept the entry under the production RequireVerifiedBlob default.
func recordCourierPeerTip(peerTips *transport.HeightSyncPeerTips, sec *heightsync.HeightSyncSection) {
	peerTips.RecordOriginWithBlob(sec, []byte("e2e-seed-blob"), []byte{1})
}

// seedCourierPeerTipsFromHostOracles seeds the courier cache from each host's oracle tip
// (deterministic setup for e2e after the initial sync turn completes).
func seedCourierPeerTipsFromHostOracles(t *testing.T, st *fourHostStack, hostOracles []*staticOracle, peerTips *transport.HeightSyncPeerTips) {
	t.Helper()
	require.Len(t, hostOracles, len(st.HostAddrs))
	now := time.Now().UnixMilli()
	for i, or := range hostOracles {
		require.NotNil(t, or.hdr, "host oracle %d", i)
		recordCourierPeerTip(peerTips, &heightsync.HeightSyncSection{
			ChainID:               "gonka-testenv-1",
			ProofType:             heightsync.AnchorProofType,
			MainnetHeight:         or.hdr.Height,
			MainnetBlockHashHex:   hex.EncodeToString(or.hdr.BlockHash),
			OriginatorSenderID:    st.HostAddrs[i],
			OriginatorTimestampMs: now,
		})
	}
}

// warmCourierPeerTipsFromResponses copies host response anchors from user audit rings
// into the courier peer-tip cache when SSE ingest recorded them under the host base URL.
func warmCourierPeerTipsFromResponses(t *testing.T, st *fourHostStack, peerTips *transport.HeightSyncPeerTips) {
	t.Helper()
	urlToHost := make(map[string]string, len(st.httpSrvs))
	for i, ts := range st.httpSrvs {
		urlToHost[ts.URL] = st.HostAddrs[i]
	}
	for _, cl := range st.Session.Clients() {
		hc, ok := cl.(*transport.HTTPClient)
		if !ok {
			continue
		}
		ar := hc.HeightSyncAuditRing()
		if ar == nil {
			continue
		}
		for _, peerID := range ar.ListPeers() {
			hostAddr, ok := urlToHost[peerID]
			if !ok {
				continue // user-outbound bucket uses peer_id = user address
			}
			for _, a := range ar.List(peerID) {
				if a.Direction != "response" || a.MainnetHeight <= 0 || len(a.MainnetBlockHash) == 0 {
					continue
				}
				recordCourierPeerTip(peerTips, &heightsync.HeightSyncSection{
					ChainID:               "gonka-testenv-1",
					ProofType:             heightsync.AnchorProofType,
					MainnetHeight:         a.MainnetHeight,
					MainnetBlockHashHex:   hex.EncodeToString(a.MainnetBlockHash),
					OriginatorSenderID:    hostAddr,
					OriginatorTimestampMs: time.Now().UnixMilli(),
				})
			}
		}
	}
}

func ensureHeightSyncPromMetrics(t *testing.T) {
	t.Helper()
	require.NoError(t, heightsync.RegisterAnchorMetrics(prometheus.NewRegistry()))
}

// courierSyncTurnWithHeldResponses runs sync-turn nonces [1, releaseAt] with HTTP
// responses for 1..releaseAt-1 blocked on each host until PrepareInference(releaseAt),
// then releases all holds so peer tips land deterministically before the release nonce send.
// The session-open seed RPC is disabled so the cache stays cold until those responses ingest;
// otherwise the seed tip is MarkPropagated to every slot in the sync turn and omit-window
// lazy carry has nothing left to send. Requires go test -tags=dev (transport.Server
// inference hold hooks).
func courierSyncTurnWithHeldResponses(t *testing.T, ctx context.Context, st *fourHostStack, params user.InferenceParams, releaseAt uint64) {
	t.Helper()
	if !inferenceHoldsEnabled() {
		t.Skip("courierSyncTurnWithHeldResponses requires -tags=dev")
	}
	require.GreaterOrEqual(t, releaseAt, uint64(2))
	for _, srv := range st.Servers {
		srv.SetHeightSyncSeedRPCEnabled(false)
	}

	type sendResult struct {
		hostIdx int
		nonce   uint64
		resp    *host.HostResponse
		err     error
	}

	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		out []sendResult
	)
	for n := uint64(1); n < releaseAt; n++ {
		p, err := st.Session.PrepareInference(params)
		require.NoError(t, err, "prepare nonce=%d", n)
		require.Equal(t, n, p.Nonce())

		hostIdx := p.HostIdx()
		armInferenceResponseHold(t, st.Servers[hostIdx])
		wg.Add(1)
		go func(p *user.PreparedInference, nonce uint64, hi int) {
			defer wg.Done()
			resp, err := st.Session.SendOnly(ctx, p, nil, nil)
			mu.Lock()
			out = append(out, sendResult{hostIdx: hi, nonce: nonce, resp: resp, err: err})
			mu.Unlock()
		}(p, n, hostIdx)
	}

	pRelease, err := st.Session.PrepareInference(params)
	require.NoError(t, err, "prepare releaseAt=%d", releaseAt)
	require.Equal(t, releaseAt, pRelease.Nonce())

	for _, srv := range st.Servers {
		releaseInferenceResponseHold(t, srv)
	}
	wg.Wait()

	results := append([]sendResult(nil), out...)
	require.Len(t, results, int(releaseAt-1))
	sort.Slice(results, func(i, j int) bool { return results[i].nonce < results[j].nonce })
	for _, r := range results {
		require.NoError(t, r.err, "SendOnly nonce=%d", r.nonce)
		require.NoError(t, st.Session.ProcessResponse(r.hostIdx, r.resp, r.nonce),
			"ProcessResponse nonce=%d", r.nonce)
	}

	resp, err := st.Session.SendOnly(ctx, pRelease, nil, nil)
	require.NoError(t, err, "SendOnly releaseAt=%d", releaseAt)
	require.NoError(t, st.Session.ProcessResponse(pRelease.HostIdx(), resp, releaseAt),
		"ProcessResponse releaseAt=%d", releaseAt)
	syncHostsFromSession(t, st)
}

// courierPipelinedSyncTurn prepares every nonce in [from, through] first, then runs
// SendOnly for all of them while each host holds its HTTP response until a single
// release processes the whole wave. The session-open seed runs on the first
// PrepareInference, so Decide in the wave sees a warm cache and Anchors.
func courierPipelinedSyncTurn(t *testing.T, ctx context.Context, st *fourHostStack, params user.InferenceParams, from, through uint64) {
	t.Helper()
	if !inferenceHoldsEnabled() {
		t.Skip("courierPipelinedSyncTurn requires -tags=dev")
	}
	require.GreaterOrEqual(t, through, from)

	type sendResult struct {
		hostIdx int
		nonce   uint64
		resp    *host.HostResponse
		err     error
	}

	prepared := make([]*user.PreparedInference, 0, int(through-from+1))
	for n := from; n <= through; n++ {
		p, err := st.Session.PrepareInference(params)
		require.NoError(t, err, "prepare nonce=%d", n)
		require.Equal(t, n, p.Nonce())
		prepared = append(prepared, p)
	}

	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		out []sendResult
	)
	for _, p := range prepared {
		hostIdx := p.HostIdx()
		armInferenceResponseHold(t, st.Servers[hostIdx])
		wg.Add(1)
		go func(p *user.PreparedInference, nonce uint64, hi int) {
			defer wg.Done()
			resp, err := st.Session.SendOnly(ctx, p, nil, nil)
			mu.Lock()
			out = append(out, sendResult{hostIdx: hi, nonce: nonce, resp: resp, err: err})
			mu.Unlock()
		}(p, p.Nonce(), hostIdx)
	}
	for _, srv := range st.Servers {
		releaseInferenceResponseHold(t, srv)
	}
	wg.Wait()

	results := append([]sendResult(nil), out...)
	require.Len(t, results, len(prepared))
	sort.Slice(results, func(i, j int) bool { return results[i].nonce < results[j].nonce })
	for _, r := range results {
		require.NoError(t, r.err, "SendOnly nonce=%d", r.nonce)
		require.NoError(t, st.Session.ProcessResponse(r.hostIdx, r.resp, r.nonce),
			"ProcessResponse nonce=%d", r.nonce)
	}
	syncHostsFromSession(t, st)
}

func requireInboundUserAnchorOriginator(
	t *testing.T,
	srv *transport.Server,
	userAddr, wantOriginator string,
	wantHeight int64,
	wantHash []byte,
	wantTag heightsync.AnchorCadenceTag,
) {
	t.Helper()
	ar := srv.HeightSyncAuditRing()
	require.NotNil(t, ar)
	var matches []heightsync.AnchorAttestation
	for _, a := range ar.List(userAddr) {
		if a.Direction != "request" || a.MainnetHeight != wantHeight {
			continue
		}
		if !bytes.Equal(a.MainnetBlockHash, wantHash) {
			continue
		}
		matches = append(matches, a)
	}
	require.NotEmpty(t, matches, "host must record inbound user anchor at height=%d", wantHeight)
	best := matches[len(matches)-1]
	require.Equal(t, wantOriginator, best.OriginatorSenderID,
		"inbound anchor must attribute originator %s, not the carrier", wantOriginator)
	require.NotEqual(t, userAddr, best.OriginatorSenderID,
		"originator must not be the user carrier address")
	if wantTag != "" {
		require.Equal(t, wantTag, best.Tag)
	}
}

func requestEmitModeAtNonce(entries []testLogEntry, nonce int) string {
	for _, e := range entries {
		if e.msg != "heightsync: emit" || e.kv["direction"] != "request" {
			continue
		}
		if e.kv["nonce"] == fmt.Sprint(nonce) {
			return strings.ToLower(strings.TrimSpace(e.kv["mode"]))
		}
	}
	return ""
}

// TestHeightSyncAnchor_E2E_CadenceLogsAndAuditTrail validates stack wiring,
// request/response anchor cadence across nonces 1..16, log consistency, and
// audit-ring growth for K=8 / slots=4.
func TestHeightSyncAnchor_E2E_CadenceLogsAndAuditTrail(t *testing.T) {
	ctx := context.Background()
	logs := installCaptureLogger(t)
	st := setupFourHostHTTPHeightSync(t)
	wantHex := hex.EncodeToString(st.Oracle.hdr.BlockHash)
	params := defaultInferenceParams()

	// Point 1: stack + wiring checks.
	require.Len(t, st.Servers, 4)
	require.Len(t, st.HostAddrs, 4)
	require.Len(t, st.httpSrvs, 4)
	require.NotNil(t, st.Oracle)
	require.NotNil(t, st.Oracle.hdr)
	require.Equal(t, int64(100), st.Oracle.hdr.Height)
	require.Equal(t, "gonka-testenv-1", st.Oracle.hdr.ChainID)
	for _, s := range st.Servers {
		require.NotNil(t, s.HeightSyncAuditRing(), "height-sync wiring attaches audit ring")
	}
	for _, cl := range st.Session.Clients() {
		hc, ok := cl.(*transport.HTTPClient)
		require.True(t, ok)
		require.NotNil(t, hc.HeightSyncAuditRing())
	}

	// Point 2 + Point 3: strict per-nonce request cadence checks.
	prevReqAnchors := 0
	for i := 1; i <= 16; i++ {
		_, err := st.Session.SendInference(ctx, params)
		require.NoError(t, err, "inference %d", i)
		syncHostsFromSession(t, st)

		curReqAnchors := countOutboundRequestAnchors(t, st.Session, st.UserAddr)
		delta := curReqAnchors - prevReqAnchors
		if wantRequestAnchorAtNonce(i) {
			require.Equal(t, 1, delta, "nonce %d must include request height payload (Anchor)", i)
		} else {
			require.Equal(t, 0, delta, "nonce %d must omit request height payload", i)
		}
		prevReqAnchors = curReqAnchors
		if i == 4 {
			require.Equal(t, 4, prevReqAnchors, "nonces 1..4 must emit Anchor on every user outbound")
			for hostIdx, s := range st.Servers {
				ar := s.HeightSyncAuditRing()
				require.NotNil(t, ar)
				hostAddr := st.HostAddrs[hostIdx]
				anch := hostInferenceResponseAnchors(ar.List(hostAddr))
				require.Len(t, anch, 1, "after nonce 4 each host should have exactly one inference-response Anchor (seed RPC excluded)")
				require.Equal(t, "response", anch[0].Direction)
				require.Equal(t, wantHex, hex.EncodeToString(anch[0].MainnetBlockHash))
			}
		}
	}
	require.Equal(t, uint64(16), st.Session.Nonce())
	require.Equal(t, 9, prevReqAnchors,
		"K=8 slots=4: Anchors at nonces in {1..4} ∪ {8..11} ∪ {16} → 9 request Anchors")

	// Log scraping: user outgoing modes and host inbound modes.
	entries := logs.snapshot()
	userReqEvents := extractHeightSyncRequestEmitEvents(entries)
	require.GreaterOrEqual(t, len(userReqEvents), 16, "need one user request emit log per nonce")
	userReqEvents = userReqEvents[:16]
	userModes := extractHeightSyncRequestEmitModes(entries)
	require.GreaterOrEqual(t, len(userModes), 16, "need one user request-mode log per nonce")
	userModes = userModes[:16]
	for nonce := 1; nonce <= 16; nonce++ {
		wantMode := "omit"
		if wantRequestAnchorAtNonce(nonce) {
			wantMode = "anchor"
		}
		require.Equal(t, wantMode, userModes[nonce-1], "user outgoing mode at nonce=%d", nonce)
	}

	hostInbound := extractHostInboundPeerEvents(entries, st.UserAddr)
	require.GreaterOrEqual(t, len(hostInbound), 16, "need one host inbound peer-attestation log per served nonce")
	hostInbound = hostInbound[:16]
	hostModesByID := make(map[string][]string)
	for nonce := 1; nonce <= 16; nonce++ {
		ev := hostInbound[nonce-1]
		hostID := ev.kv["host_id"]
		require.NotEmpty(t, hostID, "host inbound logs must include host_id")
		mode := strings.ToLower(strings.TrimSpace(ev.kv["mode"]))
		require.True(t, mode == "anchor" || mode == "omit", "unexpected mode=%q", mode)
		hostModesByID[hostID] = append(hostModesByID[hostID], mode)

		// Prefix parity: user emitted anchor prefix equals host peer prefix for same nonce.
		if wantRequestAnchorAtNonce(nonce) {
			require.Equal(t, "anchor", mode, "host mode at anchored nonce=%d", nonce)
			require.NotEmpty(t, ev.kv["peer_block_hash_prefix"], "anchored inbound host log has peer prefix")
			// user and host lists are both in send order, so same nonce index matches.
			userIdx := nonce - 1
			require.Equal(t, "anchor", userModes[userIdx], "user mode at anchored nonce=%d", nonce)
			require.Equal(t,
				strings.ToLower(strings.TrimSpace(userReqEvents[userIdx].kv["block_hash_prefix"])),
				strings.ToLower(strings.TrimSpace(ev.kv["peer_block_hash_prefix"])),
				"user and host prefix mismatch at nonce=%d", nonce,
			)
		} else {
			require.Equal(t, "omit", mode, "host mode at omitted nonce=%d", nonce)
		}
	}
	// Per-host served subset must match round-robin expectations.
	startHostID := hostInbound[0].kv["host_id"]
	startHostIdx := -1
	for i, addr := range st.HostAddrs {
		if addr == startHostID {
			startHostIdx = i
			break
		}
	}
	require.NotEqual(t, -1, startHostIdx, "first served host must be in host list")
	for hostIdx, hostAddr := range st.HostAddrs {
		got := hostModesByID[hostAddr]
		require.NotEmpty(t, got, "host %d (%s) should serve some nonces", hostIdx, hostAddr)
		var want []string
		for nonce := 1; nonce <= 16; nonce++ {
			servedBy := (startHostIdx + (nonce - 1)) % len(st.HostAddrs)
			if servedBy != hostIdx {
				continue
			}
			if wantRequestAnchorAtNonce(nonce) {
				want = append(want, "anchor")
			} else {
				want = append(want, "omit")
			}
		}
		require.Equal(t, want, got, "served-mode sequence mismatch for host %d", hostIdx)
	}

	// User audit-ring growth across all peers as turns progress.
	seenPeers := make(map[string]struct{})
	for _, cl := range st.Session.Clients() {
		hc, ok := cl.(*transport.HTTPClient)
		require.True(t, ok)
		ar := hc.HeightSyncAuditRing()
		require.NotNil(t, ar)
		for _, pid := range ar.ListPeers() {
			if strings.Contains(pid, "http://") || strings.Contains(pid, "https://") {
				seenPeers[pid] = struct{}{}
			}
		}
	}
	require.GreaterOrEqual(t, len(seenPeers), 4, "user audit ring should accumulate all four host peer_ids")
	respAnchors := 0
	for i, s := range st.Servers {
		ar := s.HeightSyncAuditRing()
		require.NotNil(t, ar)
		hostAddr := st.HostAddrs[i]
		for _, a := range hostInferenceResponseAnchors(ar.List(hostAddr)) {
			respAnchors++
			require.Equal(t, wantHex, hex.EncodeToString(a.MainnetBlockHash))
		}
	}
	// Receipt cadence follows inference nonce (same as user request): 9 Anchors for
	// nonces {1..4} ∪ {8..11} ∪ {16} — seed RPC outbound entries are excluded.
	require.Equal(t, 9, respAnchors, "response Anchors match global cadence over nonces 1..16")
	for _, s := range st.Servers {
		nIn := countInboundUserAnchorsOnHost(t, s, st.UserAddr)
		require.GreaterOrEqual(t, nIn, 1, "each host audit ring records inbound user Anchors")
	}

	inboundTotal := 0
	for _, s := range st.Servers {
		inboundTotal += countInboundUserAnchorsOnHost(t, s, st.UserAddr)
	}
	require.GreaterOrEqual(t, inboundTotal, 5,
		"hosts accumulate inbound user Anchors across the extended nonce range")
}

func TestHeightSyncAnchor_E2E_HTTPRestartSnapshotOnlyRecovery(t *testing.T) {
	ctx := context.Background()
	st := setupOneHostHTTPHeightSyncRestartStack(t)
	params := defaultInferenceParams()

	resp, err := st.Session.SendInference(ctx, params)
	require.NoError(t, err)
	require.Equal(t, uint64(1), resp.Nonce)
	require.Equal(t, uint64(1), st.Session.Nonce())
	require.Equal(t, uint64(1), st.Server.Host().SnapshotState().LatestNonce)
	require.Positive(t, countOutboundRequestAnchors(t, st.Session, st.UserAddr),
		"pre-restart request must use real height-sync HTTP transport")
	rootBefore, err := st.Session.StateMachine().ComputeStateRoot()
	require.NoError(t, err)

	require.NoError(t, st.Session.FlushSnapshot())
	require.NoError(t, st.Session.Close())
	st.Session = nil

	recovered := st.newHTTPSession(t)
	st.Session = recovered
	require.Equal(t, uint64(1), recovered.Nonce())
	require.Empty(t, recovered.Diffs(),
		"single-host cursor is at latest, so restart should take the snapshot-only recovery path")
	rootAfter, err := recovered.StateMachine().ComputeStateRoot()
	require.NoError(t, err)
	require.Equal(t, rootBefore, rootAfter)

	resp, err = recovered.SendInference(ctx, params)
	require.NoError(t, err)
	require.Equal(t, uint64(2), resp.Nonce)
	require.Equal(t, uint64(2), recovered.Nonce())
	require.Equal(t, uint64(2), st.Server.Host().SnapshotState().LatestNonce)
	require.Positive(t, countOutboundRequestAnchors(t, recovered, st.UserAddr),
		"post-restart session should still use fresh real HTTP height-sync clients")
}

func TestHeightSyncAnchor_E2E_MultiHostRepairProbeTimingAndBudget(t *testing.T) {
	st := setupFourHostHTTPRepairTimingStack(t)

	span := make([]types.Diff, 0, len(st.hosts))
	for nonce := uint64(1); nonce <= uint64(len(st.hosts)); nonce++ {
		span = append(span, repairTimingHeartbeatDiff(t, st.user, nonce, 1, 500))
	}
	st.applyDiffsToHosts(t, span...)
	cfg := heightsync.DefaultHeartbeatConfig()
	st.applyDiffsToHosts(t, repairTimingHeartbeatDiff(t, st.user, uint64(len(st.hosts))+1, 2, 500+cfg.AckDeadlineBlocks+1))

	prober := st.hostObjs[0]
	rec := prober.HeightSyncTurnRecord(1)
	require.NotNil(t, rec)
	require.Equal(t, heightsync.TurnDegraded, rec.State)
	require.Empty(t, rec.Acks)
	require.NotEmpty(t, prober.RepairBudget())
	require.False(t, prober.CloseReadyArmed())
	st.wireRepairPeersFrom(0)

	now := time.Unix(1_700_000_000, 0)
	var slept []time.Duration
	prober.RepairBudget().SetClock(func() time.Time { return now }, func(d time.Duration) {
		slept = append(slept, d)
	})
	var httpProbes atomic.Int64
	prober.SetRepairProbe(func(ctx context.Context, targetSlot uint32, req *heightsync.RepairRequest) (*heightsync.RepairResponse, error) {
		httpProbes.Add(1)
		return st.servers[0].RepairProbe(ctx, targetSlot, req)
	})

	prober.MaybeRepair(context.Background())
	require.Equal(t, int64(2), httpProbes.Load())
	require.Equal(t, 2, prober.RepairBudget().Count(heightsync.RepairOutcomeHeight))
	require.Zero(t, prober.RepairBudget().Count(heightsync.RepairOutcomeUnreachable))
	require.Equal(t, 2, prober.RepairBudget().ProbedCount())
	require.Equal(t, 1, prober.RepairBudget().Count(string(heightsync.RepairSkipBudget)))
	require.True(t, prober.PeerSeenHas(1))
	require.True(t, prober.PeerSeenHas(2))
	require.False(t, prober.PeerSeenHas(3))
	require.Equal(t, []time.Duration{15 * time.Millisecond, 10 * time.Millisecond}, slept)
	require.Equal(t, 1, st.hostObjs[1].RepairResponderBudget().ServedCount())
	require.Equal(t, 1, st.hostObjs[2].RepairResponderBudget().ServedCount())
	require.Zero(t, st.hostObjs[3].RepairResponderBudget().ServedCount())

	prober.MaybeRepair(context.Background())
	require.Equal(t, int64(2), httpProbes.Load())
	require.Equal(t, 2, prober.RepairBudget().Count(heightsync.RepairOutcomeHeight))
	require.Equal(t, 2, prober.RepairBudget().ProbedCount())
	require.Equal(t, []time.Duration{15 * time.Millisecond, 10 * time.Millisecond}, slept,
		"immediate second pass must not sleep/probe already spent turn slots")
	require.Zero(t, st.hostObjs[3].RepairResponderBudget().ServedCount())

	now = now.Add(cfg.Interval + time.Millisecond)
	prober.MaybeRepair(context.Background())
	require.Equal(t, int64(3), httpProbes.Load())
	require.Equal(t, 3, prober.RepairBudget().Count(heightsync.RepairOutcomeHeight))
	require.Equal(t, 3, prober.RepairBudget().ProbedCount())
	require.True(t, prober.PeerSeenHas(3))
	require.Equal(t, []time.Duration{15 * time.Millisecond, 10 * time.Millisecond, 5 * time.Millisecond}, slept)
	require.Equal(t, 1, st.hostObjs[3].RepairResponderBudget().ServedCount())
}

// TestHeightSyncAnchor_E2E_ForceAnchorOutsideSyncTurn covers forced sync turn (§5.5):
// InferenceParams.ForceHeightSyncAnchor adds MsgForceHeightSyncTurn so Anchors apply on
// nonces 7..10 (slots=4); periodic tail nonce 11 is swallowed vs overlapping cadence {8..11}.
func TestHeightSyncAnchor_E2E_ForceAnchorOutsideSyncTurn(t *testing.T) {
	ctx := context.Background()
	st := setupFourHostHTTPHeightSync(t)
	params := defaultInferenceParams()

	for i := 1; i <= 6; i++ {
		_, err := st.Session.SendInference(ctx, params)
		require.NoError(t, err, "inference %d", i)
		syncHostsFromSession(t, st)
	}

	require.False(t, wantRequestAnchorAtNonce(7),
		"sanity: nonce 7 must be Omit under cadence without manual force")

	prevAnchors := countOutboundRequestAnchors(t, st.Session, st.UserAddr)
	inboundBefore := 0
	for _, srv := range st.Servers {
		inboundBefore += countInboundUserAnchorsOnHost(t, srv, st.UserAddr)
	}

	forceParams := params
	forceParams.ForceHeightSyncAnchor = true
	_, err := st.Session.SendInference(ctx, forceParams)
	require.NoError(t, err)
	syncHostsFromSession(t, st)

	for _, n := range []int{8, 9, 10} {
		_, err := st.Session.SendInference(ctx, params)
		require.NoError(t, err, "nonce %d", n)
		syncHostsFromSession(t, st)
	}

	curAnchors := countOutboundRequestAnchors(t, st.Session, st.UserAddr)
	require.Equal(t, prevAnchors+4, curAnchors,
		"forced sync turn must Anchor user requests on nonces 7..10")

	inboundMid := 0
	for _, srv := range st.Servers {
		inboundMid += countInboundUserAnchorsOnHost(t, srv, st.UserAddr)
	}
	require.Equal(t, inboundBefore+4, inboundMid, "four hosts serve nonces 7..10 with inbound user Anchors")

	require.True(t, wantRequestAnchorAtNonce(11), "sanity: nonce 11 would Anchor under bare periodic cadence")

	_, err = st.Session.SendInference(ctx, params)
	require.NoError(t, err)
	syncHostsFromSession(t, st)

	require.Equal(t, curAnchors, countOutboundRequestAnchors(t, st.Session, st.UserAddr),
		"nonce 11 must Omit (cadence tail swallowed after forced [7,10] overlapping {8..11})")
}

// toggleableOracle is a staticOracle that can be flipped to return errors,
// emulating a user-side block oracle that goes blind partway through a run
// without dropping the rest of the inference path. Used by Scenario E to
// model a malicious user that opens a forced sync turn (via the diff) but
// strips height_sync from in-window requests on the wire.
type toggleableOracle struct {
	mu       sync.Mutex
	hdr      *blocks.Header
	failNext bool
}

func (o *toggleableOracle) setFail(b bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.failNext = b
}

func (o *toggleableOracle) Latest(ctx context.Context) (*blocks.Header, error) {
	_ = ctx
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.failNext {
		return nil, errors.New("toggleableOracle: forced failure")
	}
	if o.hdr == nil {
		return nil, nil
	}
	h := *o.hdr
	h.BlockHash = append([]byte(nil), o.hdr.BlockHash...)
	return &h, nil
}

func (o *toggleableOracle) At(ctx context.Context, height int64) (*blocks.Header, error) {
	_ = ctx
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.hdr != nil && height == o.hdr.Height {
		h := *o.hdr
		h.BlockHash = append([]byte(nil), o.hdr.BlockHash...)
		return &h, nil
	}
	return nil, errors.New("no header at height")
}

func (o *toggleableOracle) Prove(context.Context, string, int64) (*blocks.Proof, error) {
	return nil, errors.New("not implemented")
}

func (o *toggleableOracle) Subscribe(context.Context, int64) (<-chan *blocks.Header, error) {
	ch := make(chan *blocks.Header)
	close(ch)
	return ch, nil
}

// setupFourHostHTTPHeightSyncToggleableUserOracle is a setupFourHostHTTPHeightSync
// variant that injects a toggleable user-side oracle so a test can make
// AnchorScheduler.Decide error mid-run on the user transport, simulating a
// malicious user that strips height_sync from requests inside an active
// forced sync turn.
func setupFourHostHTTPHeightSyncToggleableUserOracle(t *testing.T) (*fourHostStack, *toggleableOracle) {
	t.Helper()
	base := staticOracleWith(100, []byte{0xab, 0xcd, 0xef, 0x42})
	hostOracles := []*staticOracle{
		staticOracleWith(base.hdr.Height, base.hdr.BlockHash),
		staticOracleWith(base.hdr.Height, base.hdr.BlockHash),
		staticOracleWith(base.hdr.Height, base.hdr.BlockHash),
		staticOracleWith(base.hdr.Height, base.hdr.BlockHash),
	}
	clientOracle := &toggleableOracle{hdr: &blocks.Header{
		Height:    base.hdr.Height,
		ChainID:   base.hdr.ChainID,
		BlockHash: append([]byte(nil), base.hdr.BlockHash...),
	}}
	st := setupFourHostHTTPHeightSyncWithToggleableClient(t, hostOracles, clientOracle)
	return st, clientOracle
}

// setupFourHostHTTPHeightSyncWithToggleableClient duplicates the wiring of
// setupFourHostHTTPHeightSyncWithOracles but threads a toggleableOracle
// (rather than a staticOracle) into the user-side scheduler. Kept inline
// here to avoid widening the public test helper API.
func setupFourHostHTTPHeightSyncWithToggleableClient(t *testing.T, hostOracles []*staticOracle, clientOracle *toggleableOracle) *fourHostStack {
	t.Helper()
	require.Len(t, hostOracles, 4)
	require.NotNil(t, clientOracle)

	userSigner := testutil.MustGenerateKey(t)
	hostSigners := make([]*signing.Secp256k1Signer, 4)
	hostAddrs := make([]string, 4)
	for i := range hostSigners {
		hostSigners[i] = testutil.MustGenerateKey(t)
		hostAddrs[i] = hostSigners[i].Address()
	}
	group := testutil.MakeGroup(hostSigners)
	cfg := types.SessionConfigWithPrice(4, 1)
	verifier := signing.NewSecp256k1Verifier()
	warmResolve := func(_, _ string) (bool, error) { return false, nil }

	brHosts := make(map[string]*bridge.HostInfo)
	slots := make([]string, len(group))
	for i, slot := range group {
		slots[i] = slot.ValidatorAddress
	}

	var servers []*transport.Server
	var httpSrvs []*httptest.Server
	for i := range hostSigners {
		smStore := testutil.MustMemoryStore(t, hsAnchorE2EEscrowID, userSigner.Address(), cfg, group, 100_000)
		sm, err := state.NewStateMachine(hsAnchorE2EEscrowID, cfg, group, 100_000, userSigner.Address(), verifier, smStore, state.WithWarmKeyResolver(warmResolve))
		require.NoError(t, err)
		mst := storage.NewMemory()
		require.NoError(t, mst.CreateSession(storage.CreateSessionParams{
			EscrowID:       hsAnchorE2EEscrowID,
			Version:        testutil.RuntimeTestVersion,
			Config:         cfg,
			Group:          group,
			InitialBalance: 100_000,
		}))
		h, err := host.NewHost(sm, hostSigners[i], stub.NewInferenceEngine(), hsAnchorE2EEscrowID, group, nil,
			host.WithGrace(10_000), host.WithStorage(mst), host.WithChainOracle(hostOracles[i]))
		require.NoError(t, err)

		hostSched := heightsync.MustNewAnchorSchedulerFromOracle(8, 4, hostOracles[i])
		srv, err := transport.NewServer(h, mst, verifier, userSigner.Address(), transport.WithHeightSync(hostSched, hostOracles[i]))
		require.NoError(t, err)
		e := echo.New()
		g := e.Group(hsE2ERoutePrefix)
		registerHeightSyncServer(g, srv)
		ts := httptest.NewServer(e)
		httpSrvs = append(httpSrvs, ts)
		servers = append(servers, srv)
		brHosts[hostSigners[i].Address()] = &bridge.HostInfo{Address: hostSigners[i].Address(), URL: ts.URL}
	}

	br := &scenarioBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID:       hsAnchorE2EEscrowID,
			Amount:         100_000,
			CreatorAddress: userSigner.Address(),
			Slots:          slots,
			TokenPrice:     1,
			AppHash:        make([]byte, 32),
		},
		hosts: brHosts,
	}
	clientSched := heightsync.MustNewAnchorSchedulerFromOracle(8, 4, clientOracle)
	cc := transport.DefaultClientConfig()
	cc.HeightSync = clientSched
	cc.HeightSyncLogOracle = clientOracle
	extra := &cc
	sess, _, err := user.NewHTTPSession(user.HTTPSessionConfig{
		PrivateKeyHex:     userSigner.PrivateKeyHex(),
		EscrowID:          hsAnchorE2EEscrowID,
		Bridge:            br,
		RoutePrefix:       hsE2ERoutePrefix,
		StoragePath:       filepath.Join(t.TempDir(), "session.db"),
		ExtraClientConfig: extra,
	})
	require.NoError(t, err)

	return &fourHostStack{
		Session:   sess,
		Servers:   servers,
		Oracle:    nil,
		UserAddr:  userSigner.Address(),
		HostAddrs: hostAddrs,
		httpSrvs:  httpSrvs,
	}
}

// TestHeightSyncAnchor_E2E_ForcedSyncTurn_HostResponsesAnchorEvenIfUserOmits
// covers Scenario E in SCENARIOS.md "Manual-force forced sync turn":
// the diff alone forces all host responses to Anchor; a malicious user that
// opens the turn (so the diff replicates) but strips height_sync from the
// in-window HTTP envelopes does NOT prevent host alignment, and each host
// records dispute evidence (a TrustForceRequestAnchorMissing audit entry).
func TestHeightSyncAnchor_E2E_ForcedSyncTurn_HostResponsesAnchorEvenIfUserOmits(t *testing.T) {
	ctx := context.Background()
	st, clientOracle := setupFourHostHTTPHeightSyncToggleableUserOracle(t)
	t.Cleanup(func() { _ = st.Session.Close() })

	params := defaultInferenceParams()

	for i := 1; i <= 6; i++ {
		_, err := st.Session.SendInference(ctx, params)
		require.NoError(t, err, "warm-up nonce %d", i)
		syncHostsFromSession(t, st)
	}

	require.False(t, wantRequestAnchorAtNonce(7),
		"sanity: nonce 7 is Omit under cadence without manual force")

	prevReqAnchors := countOutboundRequestAnchors(t, st.Session, st.UserAddr)
	inboundBefore := 0
	for _, srv := range st.Servers {
		inboundBefore += countInboundUserAnchorsOnHost(t, srv, st.UserAddr)
	}
	respBefore := 0
	for i, s := range st.Servers {
		ar := s.HeightSyncAuditRing()
		require.NotNil(t, ar)
		for _, a := range ar.List(st.HostAddrs[i]) {
			if a.Direction == "response" && len(a.MainnetBlockHash) > 0 {
				respBefore++
			}
		}
	}

	// Open the forced turn at nonce 7 (single MsgForceHeightSyncTurn in diff 7
	// with EndNonce=10), and immediately make the user-side oracle blind so
	// AnchorScheduler.Decide returns an error → transport sends plain JSON.
	forceParams := params
	forceParams.ForceHeightSyncAnchor = true
	clientOracle.setFail(true)
	_, err := st.Session.SendInference(ctx, forceParams)
	require.NoError(t, err)
	syncHostsFromSession(t, st)

	for _, n := range []int{8, 9, 10} {
		_, err := st.Session.SendInference(ctx, params)
		require.NoError(t, err, "in-window nonce %d", n)
		syncHostsFromSession(t, st)
	}

	require.Equal(t, prevReqAnchors, countOutboundRequestAnchors(t, st.Session, st.UserAddr),
		"malicious user emits zero request Anchors over forced window 7..10")

	inboundMid := 0
	for _, srv := range st.Servers {
		inboundMid += countInboundUserAnchorsOnHost(t, srv, st.UserAddr)
	}
	require.Equal(t, inboundBefore, inboundMid,
		"hosts must NOT register inbound user Anchors when the user strips height_sync")

	respMid := 0
	for i, s := range st.Servers {
		ar := s.HeightSyncAuditRing()
		for _, a := range ar.List(st.HostAddrs[i]) {
			if a.Direction == "response" && len(a.MainnetBlockHash) > 0 {
				respMid++
			}
		}
	}
	require.Equal(t, respBefore+4, respMid,
		"each of the 4 hosts must Anchor on its in-window response (driven by escrow state, not the inbound HTTP)")

	// Each host that served an in-window request must hold at least one
	// TrustForceRequestAnchorMissing dispute-evidence entry against the user.
	hostsWithMissing := 0
	for _, s := range st.Servers {
		ar := s.HeightSyncAuditRing()
		require.NotNil(t, ar)
		for _, a := range ar.List(st.UserAddr) {
			if a.Trust == heightsync.TrustForceRequestAnchorMissing && a.Direction == "request" {
				hostsWithMissing++
				break
			}
		}
	}
	require.Equal(t, 4, hostsWithMissing,
		"every served host must record a force_request_anchor_missing audit entry against the malicious user")

	clientOracle.setFail(false)
}

func TestHeightSyncAnchor_E2E_CarriesHigherPeerTipAcrossHosts(t *testing.T) {
	ctx := context.Background()
	logs := installCaptureLogger(t)

	xHash := []byte{0xaa, 0xaa, 0xaa, 0xaa}
	x1Hash := []byte{0xbb, 0xbb, 0xbb, 0xbb}
	params := defaultInferenceParams()

	// Discover the first round-robin host and pin the higher-tip oracle there,
	// so the carry-forward expectation is deterministic for the same sync turn.
	probeOracles := []*staticOracle{
		staticOracleWith(100, xHash),
		staticOracleWith(100, xHash),
		staticOracleWith(100, xHash),
		staticOracleWith(100, xHash),
	}
	probeClientOracle := staticOracleWith(100, xHash)
	probe := setupFourHostHTTPHeightSyncWithOracles(t, probeOracles, probeClientOracle)
	p1, err := probe.Session.PrepareInference(params)
	require.NoError(t, err)
	firstHostIdx := p1.HostIdx()
	_ = probe.Session.Close()
	for _, ts := range probe.httpSrvs {
		ts.Close()
	}

	hostOracles := []*staticOracle{
		staticOracleWith(100, xHash),
		staticOracleWith(100, xHash),
		staticOracleWith(100, xHash),
		staticOracleWith(100, xHash),
	}
	hostOracles[firstHostIdx] = staticOracleWith(101, x1Hash)
	clientOracle := staticOracleWith(100, xHash)
	st := setupFourHostHTTPHeightSyncWithOracles(t, hostOracles, clientOracle)

	for nonce := 1; nonce <= 4; nonce++ {
		_, err := st.Session.SendInference(ctx, params)
		require.NoError(t, err, "nonce=%d", nonce)
		syncHostsFromSession(t, st)
	}

	x1Prefix := strings.ToLower(hex.EncodeToString(x1Hash))
	entries := logs.snapshot()
	userReq := extractHeightSyncRequestEmitEvents(entries)
	require.GreaterOrEqual(t, len(userReq), 4)
	userReq = userReq[:4]
	for nonce := 1; nonce <= 4; nonce++ {
		prefix := strings.ToLower(strings.TrimSpace(userReq[nonce-1].kv["block_hash_prefix"]))
		require.Equal(t, x1Prefix, prefix,
			"session-open seed makes X+1 MaxFresh before nonce 1 (nonce=%d)", nonce)
	}
	switchNonce := 1

	hostInbound := extractHostInboundPeerEvents(entries, st.UserAddr)
	require.GreaterOrEqual(t, len(hostInbound), 4)
	hostInbound = hostInbound[:4]
	for nonce := switchNonce; nonce <= 4; nonce++ {
		require.Equal(t, x1Prefix, strings.ToLower(strings.TrimSpace(hostInbound[nonce-1].kv["peer_block_hash_prefix"])),
			"host serving nonce=%d should receive carried X+1 tip from user", nonce)
	}

	// Hosts that receive user requests after carry-forward starts should store X+1.
	for nonce := switchNonce; nonce <= 4; nonce++ {
		hostID := hostInbound[nonce-1].kv["host_id"]
		hostIdx := -1
		for i, addr := range st.HostAddrs {
			if addr == hostID {
				hostIdx = i
				break
			}
		}
		require.NotEqual(t, -1, hostIdx, "inbound host must be known")
		ar := st.Servers[hostIdx].HeightSyncAuditRing()
		require.NotNil(t, ar)
		inbound := ar.List(st.UserAddr)
		require.NotEmpty(t, inbound, "host idx=%d should have inbound user attestation(s)", hostIdx)
		latest := inbound[len(inbound)-1]
		require.Equal(t, "request", latest.Direction)
		require.Equal(t, x1Prefix, strings.ToLower(hex.EncodeToString(latest.MainnetBlockHash)),
			"host idx=%d should store carried X+1 hash in audit ring", hostIdx)
	}
}

func TestHeightSyncAnchor_E2E_LostFirstResponseSelfHealing(t *testing.T) {
	ctx := context.Background()
	logs := installCaptureLogger(t)
	st := setupFourHostHTTPHeightSync(t)
	params := defaultInferenceParams()

	// Prepare nonce=1, then kill the target host before delivery.
	p1, err := st.Session.PrepareInference(params)
	require.NoError(t, err)
	require.Equal(t, uint64(1), p1.Nonce())
	hostForNonce1 := p1.HostIdx()
	st.httpSrvs[hostForNonce1].CloseClientConnections()
	st.httpSrvs[hostForNonce1].Close()
	_, err = st.Session.SendOnly(ctx, p1, nil, nil)
	require.Error(t, err, "nonce=1 should fail when serving host is unavailable")
	require.Equal(t, uint64(1), st.Session.Nonce(), "nonce is advanced by PrepareInference even if send fails")

	// nonce=2 should still carry Anchor and succeed on the next round-robin host.
	_, err = st.Session.SendInference(ctx, params)
	require.NoError(t, err)
	syncHostsFromSession(t, st)

	// Find the host that served nonce=2: it should Anchor on the inference
	// response (seed-RPC outbound entries are ignored).
	foundFirstAnchorResp := false
	for hostIdx, s := range st.Servers {
		if hostIdx == hostForNonce1 {
			continue
		}
		ar := s.HeightSyncAuditRing()
		require.NotNil(t, ar)
		hostAddr := st.HostAddrs[hostIdx]
		resp := ar.List(hostAddr)
		for _, a := range resp {
			if a.Direction == "response" && len(a.MainnetBlockHash) > 0 &&
				!strings.Contains(strings.ToLower(a.SourceMessage), "height-sync") {
				foundFirstAnchorResp = true
				break
			}
		}
	}
	require.True(t, foundFirstAnchorResp, "nonce=2 receiving host should emit Anchor on its first response")

	// Continue bootstrap path through nonce=4 without explicit height-sync RPC.
	for n := 3; n <= 4; n++ {
		_, err = st.Session.SendInference(ctx, params)
		require.NoError(t, err, "nonce=%d", n)
		syncHostsFromSession(t, st)
	}
	require.Equal(t, uint64(4), st.Session.Nonce())

	entries := logs.snapshot()
	userReqEvents := extractHeightSyncRequestEmitEvents(entries)
	require.GreaterOrEqual(t, len(userReqEvents), 2, "need request emit logs for nonce 1 and nonce 2")
	require.Equal(t, "anchor", strings.ToLower(strings.TrimSpace(userReqEvents[0].kv["mode"])), "nonce=1 request mode")
	require.Equal(t, "anchor", strings.ToLower(strings.TrimSpace(userReqEvents[1].kv["mode"])), "nonce=2 request mode")

	// By nonce=4 the user should have at least one host attestation in audit ring.
	hostPeerAttestations := 0
	for _, cl := range st.Session.Clients() {
		hc, ok := cl.(*transport.HTTPClient)
		require.True(t, ok)
		ar := hc.HeightSyncAuditRing()
		if ar == nil {
			continue
		}
		for _, pid := range ar.ListPeers() {
			if !strings.Contains(pid, "http://") && !strings.Contains(pid, "https://") {
				continue
			}
			for _, a := range ar.List(pid) {
				if a.Direction == "response" && len(a.MainnetBlockHash) > 0 {
					hostPeerAttestations++
				}
			}
		}
	}
	require.GreaterOrEqual(t, hostPeerAttestations, 1,
		"user audit ring should contain host attestation(s) by nonce 4")
}

// TestHeightSyncAnchor_E2E_HeightSyncFeedStopped_SyncTurnOmitsWithoutErrors covers spec §9 / §13:
// when the shared height-sync feed fails (heightsyncd stopped), cadence still wants Anchor inside
// the initial sync turn but AnchorScheduler degrades to Omit; inferences must succeed.
func TestHeightSyncAnchor_E2E_HeightSyncFeedStopped_SyncTurnOmitsWithoutErrors(t *testing.T) {
	ctx := context.Background()
	logs := installCaptureLogger(t)
	st, gate := setupFourHostHTTPHeightSyncStoppingOracle(t)
	params := defaultInferenceParams()

	_, err := st.Session.SendInference(ctx, params)
	require.NoError(t, err)
	syncHostsFromSession(t, st)
	prevAnchors := countOutboundRequestAnchors(t, st.Session, st.UserAddr)
	require.Equal(t, 1, prevAnchors)

	gate.SetStopped(true)

	_, err = st.Session.SendInference(ctx, params)
	require.NoError(t, err)
	syncHostsFromSession(t, st)

	require.Equal(t, prevAnchors, countOutboundRequestAnchors(t, st.Session, st.UserAddr),
		"nonce=2 is still in initial sync turn but oracle failure must produce request Omit")

	entries := logs.snapshot()
	modes := extractHeightSyncRequestEmitModes(entries)
	require.GreaterOrEqual(t, len(modes), 2, "need user heightsync: emit lines for two requests")
	require.Equal(t, "anchor", modes[0])
	require.Equal(t, "omit", modes[1])

	hostIn := extractHostInboundPeerEvents(entries, st.UserAddr)
	require.GreaterOrEqual(t, len(hostIn), 2)
	require.Equal(t, "anchor", strings.ToLower(strings.TrimSpace(hostIn[0].kv["mode"])))
	require.Equal(t, "omit", strings.ToLower(strings.TrimSpace(hostIn[1].kv["mode"])))

	require.Equal(t, uint64(2), st.Session.Nonce())
}

// TestHeightSyncAnchor_E2E_HeightSyncFeedStopped_RecoversWhenFeedReturns asserts Anchors return
// after the shared oracle starts succeeding again (optional companion to §9.3 item 8).
func TestHeightSyncAnchor_E2E_HeightSyncFeedStopped_RecoversWhenFeedReturns(t *testing.T) {
	ctx := context.Background()
	logs := installCaptureLogger(t)
	st, gate := setupFourHostHTTPHeightSyncStoppingOracle(t)
	params := defaultInferenceParams()

	_, err := st.Session.SendInference(ctx, params)
	require.NoError(t, err)
	syncHostsFromSession(t, st)

	gate.SetStopped(true)
	for i := 2; i <= 7; i++ {
		_, err := st.Session.SendInference(ctx, params)
		require.NoError(t, err, "nonce=%d", i)
		syncHostsFromSession(t, st)
	}
	require.Equal(t, 1, countOutboundRequestAnchors(t, st.Session, st.UserAddr),
		"nonces 2..7 must all Omit while feed is down")

	gate.SetStopped(false)
	_, err = st.Session.SendInference(ctx, params)
	require.NoError(t, err)
	require.Equal(t, uint64(8), st.Session.Nonce())
	syncHostsFromSession(t, st)

	require.Equal(t, 2, countOutboundRequestAnchors(t, st.Session, st.UserAddr),
		"nonce=8 opens periodic sync turn; oracle healthy → Anchor")

	modes := extractHeightSyncRequestEmitModes(logs.snapshot())
	require.GreaterOrEqual(t, len(modes), 8)
	require.Equal(t, "anchor", modes[0])
	for i := 1; i <= 6; i++ {
		require.Equal(t, "omit", modes[i], "nonce=%d user emit while feed stopped", i+1)
	}
	require.Equal(t, "anchor", modes[7])
}

// TestHeightSyncAnchor_E2E_CheatingTrailStoresBogusUserHash covers spec §16 audit:
// a dishonest user can still sign and send an Anchor at the real mainnet height but
// with a fabricated block hash; the receiving host records that hash verbatim in the
// audit ring so an offline verifier comparing against the canonical oracle tip can flag it.
func TestHeightSyncAnchor_E2E_CheatingTrailStoresBogusUserHash(t *testing.T) {
	ctx := context.Background()
	base := staticOracleWith(100, []byte{0xab, 0xcd, 0xef, 0x42})
	canonical := append([]byte(nil), base.hdr.BlockHash...)
	bogus := append([]byte(nil), canonical...)
	bogus[0] ^= 0xff
	require.False(t, bytes.Equal(bogus, canonical))

	hostOracles := []*staticOracle{
		staticOracleWith(base.hdr.Height, base.hdr.BlockHash),
		staticOracleWith(base.hdr.Height, base.hdr.BlockHash),
		staticOracleWith(base.hdr.Height, base.hdr.BlockHash),
		staticOracleWith(base.hdr.Height, base.hdr.BlockHash),
	}
	st := setupFourHostHTTPHeightSyncWithOracles(t, hostOracles, base, func(cc *transport.ClientConfig) {
		cc.HeightSyncRequestMutateHook = func(sec *heightsync.HeightSyncSection, nonce uint64) {
			if nonce != 1 || sec == nil {
				return
			}
			sec.MainnetBlockHashHex = hex.EncodeToString(bogus)
		}
	})

	_, err := st.Session.SendInference(ctx, defaultInferenceParams())
	require.NoError(t, err)
	syncHostsFromSession(t, st)

	var matches []heightsync.AnchorAttestation
	for _, srv := range st.Servers {
		ar := srv.HeightSyncAuditRing()
		require.NotNil(t, ar)
		for _, a := range ar.List(st.UserAddr) {
			if a.Direction != "request" || a.MainnetHeight != base.hdr.Height {
				continue
			}
			if bytes.Equal(a.MainnetBlockHash, bogus) {
				matches = append(matches, a)
			}
		}
	}
	require.Len(t, matches, 1, "the host that served nonce=1 must store the user Anchor bytes verbatim")
	require.Equal(t, heightsync.TrustPeerAligned, matches[0].Trust,
		"PoC accepts in-window Anchors at oracle height even when the hash disagrees with the local oracle (offline verifier compares)")
	require.False(t, bytes.Equal(matches[0].MainnetBlockHash, canonical),
		"recorded hash must differ from canonical BlockID.Hash at the same height")
}

// TestHeightSyncAnchor_E2E_LazyCarryForwardOutsideSyncTurn covers spec §16:
// omit-window nonces 5–7 lazy-carry a peer tip; per-host dedup skips repeat propagation.
func TestHeightSyncAnchor_E2E_LazyCarryForwardOutsideSyncTurn(t *testing.T) {
	ctx := context.Background()
	logs := installCaptureLogger(t)

	const tipHeight = int64(101)
	tipHash := []byte{0xbb, 0xbb, 0xbb, 0xbb}
	base := staticOracleWith(100, []byte{0xab, 0xcd, 0xef, 0x42})
	hostOracles := []*staticOracle{
		staticOracleWith(100, base.hdr.BlockHash),
		staticOracleWith(100, base.hdr.BlockHash),
		staticOracleWith(100, base.hdr.BlockHash),
		staticOracleWith(tipHeight, tipHash),
	}
	st, peerTips := setupFourHostHTTPHeightSyncCourier(t, hostOracles)
	params := defaultInferenceParams()

	courierSyncTurnWithHeldResponses(t, ctx, st, params, 4)
	peerTips = peerTipsFromSession(t, st.Session)
	tip := peerTips.MaxFresh(time.Now(), peerTips.Freshness)
	require.NotNil(t, tip)
	require.Equal(t, tipHeight, tip.MainnetHeight)
	require.Equal(t, st.HostAddrs[3], tip.OriginatorSenderID)

	for n := 5; n <= 7; n++ {
		_, err := st.Session.SendInference(ctx, params)
		require.NoError(t, err, "omit window nonce=%d", n)
		syncHostsFromSession(t, st)
	}

	entries := logs.snapshot()
	for n := 5; n <= 7; n++ {
		require.Equal(t, "anchor", requestEmitModeAtNonce(entries, n),
			"user must lazy-emit Anchor at omit-window nonce=%d", n)
	}

	for n := 5; n <= 7; n++ {
		hostIdx := hostIdxForNonce(uint64(n))
		require.Equal(t, 1, countInboundUserAnchorsWithTag(t, st.Servers[hostIdx], st.UserAddr, heightsync.TagLazy),
			"host idx=%d (nonce=%d) must record inbound tag=lazy", hostIdx, n)
	}

	// Per-recipient dedup at the same height is asserted below (nonce 5 → host 1, then omit at nonce 13).

	// Advance through periodic sync turn 8–12; nonce 13 (omit) targets same host as nonce 5.
	for n := 8; n <= 12; n++ {
		_, err := st.Session.SendInference(ctx, params)
		require.NoError(t, err, "advance nonce=%d", n)
		syncHostsFromSession(t, st)
	}
	host1 := hostIdxForNonce(5)
	lazyOnHost1Before := countInboundUserAnchorsWithTag(t, st.Servers[host1], st.UserAddr, heightsync.TagLazy)

	_, err := st.Session.SendInference(ctx, params)
	require.NoError(t, err)
	syncHostsFromSession(t, st)

	entries = logs.snapshot()
	require.Equal(t, "omit", requestEmitModeAtNonce(entries, 13),
		"user must Omit lazy re-send to host already propagated at nonce 5")
	require.Equal(t, lazyOnHost1Before, countInboundUserAnchorsWithTag(t, st.Servers[host1], st.UserAddr, heightsync.TagLazy),
		"receiver must not grow lazy audit ring on deduped omit-window nonce")
}

// TestHeightSyncAnchor_E2E_StaleOriginRejected covers spec §16: backdated originator → stale_origin.
func TestHeightSyncAnchor_E2E_StaleOriginRejected(t *testing.T) {
	ctx := context.Background()
	logs := installCaptureLogger(t)
	ensureHeightSyncPromMetrics(t)

	const tipHeight = int64(101)
	tipHash := []byte{0xcc, 0xcc, 0xcc, 0xcc}
	base := staticOracleWith(100, []byte{0xab, 0xcd, 0xef, 0x42})
	hostOracles := []*staticOracle{
		staticOracleWith(100, base.hdr.BlockHash),
		staticOracleWith(100, base.hdr.BlockHash),
		staticOracleWith(100, base.hdr.BlockHash),
		staticOracleWith(tipHeight, tipHash),
	}
	st, _ := setupFourHostHTTPHeightSyncCourier(t, hostOracles, func(cc *transport.ClientConfig) {
		cc.HeightSyncRequestMutateHook = func(sec *heightsync.HeightSyncSection, nonce uint64) {
			if sec == nil || nonce != 5 {
				return
			}
			if sec.OriginatorSenderID != "" || sec.OriginatorTimestampMs > 0 {
				sec.OriginatorTimestampMs = time.Now().Add(-5 * time.Minute).UnixMilli()
			}
		}
	})
	params := defaultInferenceParams()

	courierSyncTurnWithHeldResponses(t, ctx, st, params, 4)
	_ = peerTipsFromSession(t, st.Session) // courier cache warmed by held sync-turn responses

	staleBefore := heightsync.StaleOriginRejectedTotal()
	lazyBefore := heightsync.LazyAnchorTotal()

	_, err := st.Session.SendInference(ctx, params)
	require.NoError(t, err)
	syncHostsFromSession(t, st)

	require.Equal(t, staleBefore+1, heightsync.StaleOriginRejectedTotal())
	require.Equal(t, lazyBefore, heightsync.LazyAnchorTotal(), "stale origin must not count as lazy anchor")

	var sawStaleWarn bool
	for _, e := range logs.snapshot() {
		if e.msg == "heightsync: invalid inbound anchor" && e.kv["reason"] == "stale_origin" {
			sawStaleWarn = true
			break
		}
	}
	require.True(t, sawStaleWarn, "host must warn with stale_origin")

	hostIdx := hostIdxForNonce(5)
	ar := st.Servers[hostIdx].HeightSyncAuditRing()
	require.NotNil(t, ar)
	var sawDispute bool
	for _, a := range ar.List(st.UserAddr) {
		if a.Trust == heightsync.TrustDisputeCarrier {
			sawDispute = true
		}
		require.NotEqual(t, heightsync.TagLazy, a.Tag, "stale carry-forward must not be tagged lazy")
	}
	require.True(t, sawDispute, "audit ring must record dispute_carrier for stale origin")
}

// TestHeightSyncAnchor_E2E_HeldOriginatorReplayRejected covers spec §16: held signed
// originator section replayed after freshness budget F expires.
func TestHeightSyncAnchor_E2E_HeldOriginatorReplayRejected(t *testing.T) {
	if testing.Short() {
		t.Skip("held-originator replay requires 70s wall-clock hold (run without -short)")
	}
	ctx := context.Background()
	logs := installCaptureLogger(t)
	ensureHeightSyncPromMetrics(t)

	const tipHeight int64 = 101
	tipHash := []byte{0xcc, 0xcc, 0xcc, 0xcc}
	base := staticOracleWith(100, []byte{0xab, 0xcd, 0xef, 0x42})
	hostOracles := []*staticOracle{
		staticOracleWith(100, base.hdr.BlockHash),
		staticOracleWith(100, base.hdr.BlockHash),
		staticOracleWith(100, base.hdr.BlockHash),
		staticOracleWith(tipHeight, tipHash),
	}
	st, peerTips := setupFourHostHTTPHeightSyncCourier(t, hostOracles, func(cc *transport.ClientConfig) {
		// Courier cache F is widened so the held originator is still emitted after
		// the 70s wait (the attack is replay). Host inbound F stays 60s and must reject.
		if cc.HeightSyncPeerTips != nil {
			cc.HeightSyncPeerTips.Freshness = 24 * time.Hour
			cc.HeightSync = heightsync.MustNewAnchorScheduler(8, 4,
				heightsync.NewPeerTipOracleSource(cc.HeightSyncPeerTips, cc.HeightSyncPeerTips.Freshness))
		}
	})
	params := defaultInferenceParams()

	courierSyncTurnWithHeldResponses(t, ctx, st, params, 4)
	require.NotNil(t, peerTips.MaxFresh(time.Now(), peerTips.Freshness),
		"sync turn must warm courier cache before hold")

	time.Sleep(70 * time.Second)

	staleBefore := heightsync.StaleOriginRejectedTotal()
	lazyBefore := heightsync.LazyAnchorTotal()

	_, err := st.Session.SendInference(ctx, params)
	require.NoError(t, err)
	syncHostsFromSession(t, st)

	require.Equal(t, staleBefore+1, heightsync.StaleOriginRejectedTotal())
	require.Equal(t, lazyBefore, heightsync.LazyAnchorTotal(), "stale held replay must not count as lazy anchor")

	var sawStaleWarn bool
	for _, e := range logs.snapshot() {
		if e.msg == "heightsync: invalid inbound anchor" && e.kv["reason"] == "stale_origin" {
			sawStaleWarn = true
			break
		}
	}
	require.True(t, sawStaleWarn, "host must warn with stale_origin after held replay")

	hostIdx := hostIdxForNonce(5)
	ar := st.Servers[hostIdx].HeightSyncAuditRing()
	require.NotNil(t, ar)
	var sawDispute bool
	for _, a := range ar.List(st.UserAddr) {
		if a.Trust == heightsync.TrustDisputeCarrier {
			sawDispute = true
		}
		require.NotEqual(t, heightsync.TagLazy, a.Tag, "stale held replay must not be tagged lazy")
	}
	require.True(t, sawDispute, "audit ring must record dispute_carrier for held replay")
}
