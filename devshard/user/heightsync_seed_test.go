package user

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"devshard/chainoracle/blocks/headertime"
	"devshard/heightsync"
	"devshard/host"
	"devshard/internal/statetest"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/transport"

	"common/chainoracle/blocks"
)

func TestHeightSeedQuorum(t *testing.T) {
	cases := []struct {
		seeded, slots int
		want          bool
	}{
		{0, 0, false},
		{0, 1, false},
		{1, 1, true},
		{1, 2, true},
		{1, 3, false},
		{2, 3, true},
		{1, 4, false},
		{2, 4, true},
	}
	for _, tc := range cases {
		got := heightSeedQuorum(tc.seeded, tc.slots)
		if got != tc.want {
			t.Fatalf("heightSeedQuorum(%d, %d)=%v, want %v", tc.seeded, tc.slots, got, tc.want)
		}
	}
}

const seedTestRoutePrefix = "/devshard/v2"

func seedHTTPConfig() transport.ClientConfig {
	cfg := transport.DefaultClientConfig()
	cfg.AllowRetiredHTTPSession = true
	return cfg
}

type seedSlot struct {
	server *httptest.Server
	client *transport.HTTPClient
}

func registerSeedRoutes(g *echo.Group, srv *transport.Server) {
	withAuth := func(recordChatTerminal bool, handler echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			wrapped := srv.RateLimitMiddleware(recordChatTerminal)(handler)
			return srv.AuthMiddleware(wrapped)(c)
		}
	}
	g.POST("/sessions/:id/chat/completions", withAuth(true, srv.HandleInference))
	g.POST("/sessions/:id/height-sync", withAuth(false, srv.HandleHeightSync))
	g.POST("/sessions/:id/heightsync/repair", withAuth(false, srv.HandleHeightSyncRepair))
	g.GET("/sessions/:id/mempool", srv.HandleGetMempool)
}

type seedEnv struct {
	session  *Session
	peerTips *transport.HeightSyncPeerTips
	slots    []seedSlot
}

// setupSeedSession gives each host its own block hash at height 55.
func setupSeedSession(t *testing.T, seedRPC []bool, opts ...SessionOption) *seedEnv {
	t.Helper()
	return setupSeedSessionHashes(t, seedRPC, func(i int) []byte { return []byte{0xaa, byte(i + 1)} }, opts...)
}

// setupSeedSessionHashes is setupSeedSession with host i reporting hash(i).
// Acks raise F only while they agree on the hash at a height, so a test that
// needs several block acks gives every host the same hash.
func setupSeedSessionHashes(t *testing.T, seedRPC []bool, hash func(i int) []byte, opts ...SessionOption) *seedEnv {
	t.Helper()
	n := len(seedRPC)
	hosts := make([]*signing.Secp256k1Signer, n)
	for i := range hosts {
		hosts[i] = testutil.MustGenerateKey(t)
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(n)
	verifier := signing.NewSecp256k1Verifier()

	peerTips := transport.NewHeightSyncPeerTips()
	src := heightsync.NewPeerTipOracleSource(peerTips, peerTips.Freshness)
	clientSched := heightsync.MustNewAnchorScheduler(10, uint64(n), src)

	slots := make([]seedSlot, n)
	clients := make([]HostClient, n)
	for i := range hosts {
		or := &sessionOracle{hash: hash(i)}
		or.height.Store(55)
		sm := statetest.MustStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier, state.WithHeaderTimes(polledSource(t)))
		store := storage.NewMemory()
		require.NoError(t, store.CreateSession(storage.CreateSessionParams{
			EscrowID: "escrow-1", Version: testutil.RuntimeTestVersion, Config: config, Group: group, InitialBalance: 100000,
		}))
		h, err := host.NewHost(sm, hosts[i], stub.NewInferenceEngine(), "escrow-1", group, nil,
			host.WithGrace(100), host.WithStorage(store), host.WithChainOracle(or))
		require.NoError(t, err)
		hostSched := heightsync.MustNewAnchorSchedulerFromOracle(10, uint64(n), or)
		serverOpts := []transport.ServerOption{transport.WithHeightSync(hostSched, or)}
		if !seedRPC[i] {
			serverOpts = append(serverOpts, transport.WithHeightSyncSeedRPC(false))
		}
		srv, err := transport.NewServer(h, store, verifier, user.Address(), serverOpts...)
		require.NoError(t, err)
		e := echo.New()
		e.GET("/devshard/v2/healthz", func(c echo.Context) error {
			return c.NoContent(http.StatusOK)
		})
		g := e.Group(seedTestRoutePrefix)
		registerSeedRoutes(g, srv)
		ts := httptest.NewServer(e)
		t.Cleanup(ts.Close)

		cfg := seedHTTPConfig()
		cfg.RoutePrefix = seedTestRoutePrefix
		cfg.QueryTimeout = 2 * time.Second
		cfg.HeightSync = clientSched
		cfg.HeightSyncPeerTips = peerTips
		c := transport.NewHTTPClient(ts.URL, "escrow-1", user, cfg)
		slots[i] = seedSlot{server: ts, client: c}
		clients[i] = c
	}

	userSM := statetest.MustStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier, state.WithHeaderTimes(polledSource(t)))
	sessionOpts := append([]SessionOption{WithHeightSyncCadence(10, uint64(n))}, opts...)
	session, err := NewSession(userSM, user, "escrow-1", group, clients, verifier, sessionOpts...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	return &seedEnv{session: session, peerTips: peerTips, slots: slots}
}

// TestSeed_PrimesTheEnvelopeNotTheLog is where §18.5 (session-open seed) meets
// §10.3.1 (a user stamp is F or absent).
//
// The seed is a real host-signed reading, and it does its job: the client can
// anchor its request envelopes from the first exchange, which is what admission
// and sync_state need. What it cannot do is put that height in Diff. A verifier
// reading the log sees only the number; the Anchor that justified it never
// entered the log, so a seeded stamp and an invented one are indistinguishable
// there — and that is exactly the P1 hole. The seed itself writes nothing.
// The opening heartbeat is unstamped; a host ack of its tip is what raises F.
func TestBootstrapReply_FillsPeerTipsWithoutSeedRPC(t *testing.T) {
	var seedPosts atomic.Int32
	env := setupSeedSessionHashes(t, []bool{false, false, false},
		func(int) []byte { return []byte{0xaa, 0x01} }, WithRequireHeightSeed(true))
	clients := make([]HostClient, len(env.slots))
	for i := range env.slots {
		inner := env.slots[i].server.Config.Handler
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/height-sync") {
				seedPosts.Add(1)
			}
			inner.ServeHTTP(w, r)
		}))
		t.Cleanup(proxy.Close)
		cfg := seedHTTPConfig()
		cfg.RoutePrefix = seedTestRoutePrefix
		cfg.QueryTimeout = 2 * time.Second
		cfg.HeightSync = heightsync.MustNewAnchorScheduler(10, uint64(len(env.slots)), heightsync.NewPeerTipOracleSource(env.peerTips, env.peerTips.Freshness))
		cfg.HeightSyncPeerTips = env.peerTips
		clients[i] = transport.NewHTTPClient(proxy.URL, "escrow-1", env.session.signer, cfg)
	}
	env.session.clients = clients
	env.session.MarkHeightSeedGate(time.Now().Add(heightSeedGate))
	// Oracle, poller, and floor share one hash at height 55. The poller
	// otherwise only has height 100, so the chat gate never sees time(F).
	_, err := polledSource(t).HeaderTimeMs(55, []byte{0xaa, 0x01})
	require.NoError(t, err)

	require.NoError(t, env.session.MaybeHeartbeat(context.Background()))
	require.NoError(t, env.session.WaitFloorChat(context.Background()))
	require.Zero(t, seedPosts.Load(), "chat and the bootstrap span must not call SeedHeightSync")

	sm := env.session.StateMachine()
	floor, _, known := sm.HeightSyncFloorAsOf(env.session.Nonce() + 1)
	require.True(t, known)
	require.Equal(t, uint64(55), floor)
	acked := sm.HeightSyncSlotsWithBlockAck()
	ackedAddrs := map[string]struct{}{}
	for n := uint64(1); n <= env.session.Nonce(); n++ {
		rec := sm.HeightSyncTurnRecord(n)
		if rec == nil {
			continue
		}
		for slot, ack := range rec.Acks {
			if ack.Height > 0 && heightsync.StampPresent(ack.Hash) {
				ackedAddrs[env.session.group[slot].ValidatorAddress] = struct{}{}
			}
		}
	}
	require.GreaterOrEqual(t, len(ackedAddrs), 2, "half the roster acked a block")
	require.Equal(t, acked, len(ackedAddrs))

	tips := env.peerTips.PerOriginator(time.Now())
	got := map[string]int64{}
	for _, tip := range tips {
		if tip.Height > 0 && tip.Verified {
			got[tip.Originator] = tip.Height
		}
	}
	for addr := range ackedAddrs {
		require.Equal(t, int64(55), got[addr], "acked host %s is missing from the peer-tip cache", addr)
	}

	st := env.session.HeightSeedStatus()
	require.Equal(t, HeightSeedStateOK, st.State)
	require.Equal(t, acked, st.Seeded)

	env.session.mu.Lock()
	ready := env.session.floorChatReadyLocked()
	env.session.mu.Unlock()
	require.True(t, ready)

	stale := time.Now().Add(-(2*heightsync.DefaultHeartbeatInterval + time.Second)).UnixMilli()
	env.session.StateMachine().SetHeaderTimes(&fixedHeaderTimes{ms: stale})
	require.Equal(t, HeightSeedStatePending, env.session.HeightSeedStatus().State)
	env.session.mu.Lock()
	ready = env.session.floorChatReadyLocked()
	env.session.mu.Unlock()
	require.False(t, ready)

	env.session.StateMachine().SetHeaderTimes(&fixedHeaderTimes{ms: time.Now().Add(-time.Second).UnixMilli()})
	require.Equal(t, HeightSeedStateOK, env.session.HeightSeedStatus().State)
}

type fixedHeaderTimes struct {
	ms int64
}

func (f *fixedHeaderTimes) HeaderTimeMs(uint64, []byte) (int64, error) {
	if f == nil || f.ms <= 0 {
		return 0, errors.New("no header time")
	}
	return f.ms, nil
}

// StoredAt is the row the chat gate reads. The hash is the one the oracle,
// the poller, and the floor share in TestBootstrapReply_FillsPeerTipsWithoutSeedRPC.
func (f *fixedHeaderTimes) StoredAt(height uint64) (headertime.Record, bool) {
	if f == nil || f.ms <= 0 || height == 0 {
		return headertime.Record{}, false
	}
	return headertime.Record{Hash: []byte{0xaa, 0x01}, TimeMs: f.ms}, true
}

func TestSeed_PrimesTheEnvelopeNotTheLog(t *testing.T) {
	env := setupSeedSession(t, []bool{true, true, true})
	ctx := context.Background()

	h, ok := env.slots[0].client.ObservedHeightNow()
	require.False(t, ok)
	require.Equal(t, uint64(0), h)
	require.Equal(t, uint64(0), env.session.Nonce())

	env.session.SeedHeightSync(ctx)
	obs, ok := env.slots[0].client.ObservedHeightNow()
	require.True(t, ok, "seed must prime ObservedHeightNow before any diff")
	require.Equal(t, uint64(55), obs)
	require.Equal(t, uint64(0), env.session.Nonce(), "seed consumes no nonce")
	require.Empty(t, env.session.Diffs())

	rememberJournal(env.session)
	require.NoError(t, env.session.MaybeHeartbeat(ctx))
	require.NotEmpty(t, unstampedHeartbeats(seenDiffs(env.session)),
		"the opening span carries no height; the seed anchor is not copied onto it")
	for _, d := range seenDiffs(env.session) {
		for _, tx := range d.Txs {
			if hb := tx.GetHeartbeat(); hb != nil {
				require.Zero(t, hb.ObservedHeight)
				require.Empty(t, hb.ObservedBlockHash)
			}
		}
	}
	floor, hash, known := env.session.StateMachine().HeightSyncFloorAsOf(env.session.Nonce() + 1)
	require.True(t, known)
	require.Equal(t, uint64(55), floor, "the host ack of its tip raises F; the seed anchor does not")
	require.NotEmpty(t, hash)
	require.Zero(t, env.session.HeartbeatSkippedNoHeight())
}

func TestSeed_FanOutSurvivesDeadSlot(t *testing.T) {
	// Slot 0 404s; slots 1 and 2 seed (2/3 ≥ half). Shared peer-tip cache holds both.
	env := setupSeedSession(t, []bool{false, true, true})
	env.session.SeedHeightSync(context.Background())

	h, ok := env.slots[1].client.ObservedHeightNow()
	require.True(t, ok)
	require.Equal(t, uint64(55), h)
	require.False(t, env.session.HeightSeedMissed())

	st := env.peerTips.Snapshot(time.Now())
	require.GreaterOrEqual(t, st.VerifiedOriginators, 2,
		"every valid Anchor must land in the shared HeightSyncPeerTips")
	require.True(t, st.CacheReady)
}

func TestSeed_BelowHalfMisses(t *testing.T) {
	// One Anchor of three is below half; seed_missed even though a tip exists.
	env := setupSeedSession(t, []bool{true, false, false})
	env.session.SeedHeightSync(context.Background())
	require.True(t, env.session.HeightSeedMissed())
	h, ok := env.slots[0].client.ObservedHeightNow()
	require.True(t, ok, "the successful slot still records its Anchor")
	require.Equal(t, uint64(55), h)
}

func TestSeed_HalfOfTwoSucceeds(t *testing.T) {
	env := setupSeedSession(t, []bool{true, false})
	env.session.SeedHeightSync(context.Background())
	require.False(t, env.session.HeightSeedMissed())
	h, ok := env.slots[0].client.ObservedHeightNow()
	require.True(t, ok)
	require.Equal(t, uint64(55), h)
}

func TestSeed_TotalMissDegradesNeverFails(t *testing.T) {
	// All slots unseedable. Session stays usable; first due check skips.
	env := setupSeedSession(t, []bool{false, false, false})
	env.session.SeedHeightSync(context.Background())
	require.True(t, env.session.HeightSeedMissed())
	require.Equal(t, uint64(0), env.session.Nonce())
	_, ok := env.slots[0].client.ObservedHeightNow()
	require.False(t, ok)

	rememberJournal(env.session)
	require.NoError(t, env.session.MaybeHeartbeat(context.Background()))
	require.NotEmpty(t, unstampedHeartbeats(seenDiffs(env.session)))
	require.Zero(t, env.session.HeartbeatSkippedNoHeight())

	_, err := env.session.SendInference(context.Background(), InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	})
	require.NoError(t, err, "total seed miss must not fail the session")
	require.Greater(t, env.session.Nonce(), uint64(0))
}

func TestSeed_DoesNotAdvanceHLastOrConsumeNonce(t *testing.T) {
	// Seed is a transport read: it proves nothing about the log, so it neither
	// completes a turn nor consumes a nonce. The opening span that follows is
	// unstamped; the seed anchor is not the height it carries.
	env := setupSeedSession(t, []bool{true, true, true})
	env.session.SeedHeightSync(context.Background())

	require.Equal(t, uint64(0), env.session.Nonce())
	require.Empty(t, env.session.Diffs())
	require.Equal(t, uint64(0), env.session.HeartbeatTurnTracker().LastCompletedHeight())
	require.False(t, env.session.HeartbeatTurnTracker().CompletedAtOrAbove(55))

	rememberJournal(env.session)
	require.NoError(t, env.session.MaybeHeartbeat(context.Background()))
	require.NotEmpty(t, unstampedHeartbeats(seenDiffs(env.session)),
		"the seed anchor stays off the heartbeat; the opening span carries no height")
	require.Zero(t, env.session.HeartbeatSkippedNoHeight())
}

func TestSeed_DeclinedSlotsAreReprobed(t *testing.T) {
	env := setupSeedSession(t, []bool{false}, WithRequireHeightSeed(true))
	var hits atomic.Int32
	inner := env.slots[0].server.Config.Handler
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/devshard/v2/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		hits.Add(1)
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(proxy.Close)
	cfg := seedHTTPConfig()
	cfg.RoutePrefix = seedTestRoutePrefix
	cfg.QueryTimeout = 2 * time.Second
	cfg.HeightSync = heightsync.MustNewAnchorScheduler(10, 1, heightsync.NewPeerTipOracleSource(env.peerTips, env.peerTips.Freshness))
	cfg.HeightSyncPeerTips = env.peerTips
	env.session.clients = []HostClient{
		transport.NewHTTPClient(proxy.URL, "escrow-1", env.session.signer, cfg),
	}

	env.session.SeedHeightSync(context.Background())
	require.True(t, env.session.HeightSeedMissed())
	first := hits.Load()
	require.Greater(t, first, int32(0))
	require.Eventually(t, func() bool {
		return hits.Load() > first
	}, 8*time.Second, 50*time.Millisecond, "declined slots must be re-probed")
}

func TestSeed_Catalog503RetriesUntilCallerCtxThenDoesNotMiss(t *testing.T) {
	var hits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "version v2 is not present in the governance routing catalog", http.StatusServiceUnavailable)
	}))
	t.Cleanup(ts.Close)

	hostKey := testutil.MustGenerateKey(t)
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup([]*signing.Secp256k1Signer{hostKey})
	config := testutil.DefaultConfig(1)
	verifier := signing.NewSecp256k1Verifier()
	cfg := seedHTTPConfig()
	cfg.RoutePrefix = "/"
	cfg.QueryTimeout = 80 * time.Millisecond
	cfg.HeightSyncPeerTips = transport.NewHeightSyncPeerTips()
	client := transport.NewHTTPClient(ts.URL, "escrow-1", user, cfg)
	userSM := statetest.MustStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier)
	session, err := NewSession(userSM, user, "escrow-1", group, []HostClient{client}, verifier)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	session.SeedHeightSync(ctx)
	require.False(t, session.HeightSeedMissed(),
		"caller ctx expiry is not a terminal miss; 503 is retry-later")
	require.Greater(t, hits.Load(), int32(1), "catalog 503 must retry until the caller ctx is done")
}

func TestSeed_PrepareInferenceDoesNotSeed(t *testing.T) {
	var hits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "version v2 is not present in the governance routing catalog", http.StatusServiceUnavailable)
	}))
	t.Cleanup(ts.Close)

	hostKey := testutil.MustGenerateKey(t)
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup([]*signing.Secp256k1Signer{hostKey})
	config := testutil.DefaultConfig(1)
	verifier := signing.NewSecp256k1Verifier()
	cfg := seedHTTPConfig()
	cfg.RoutePrefix = "/"
	cfg.QueryTimeout = 80 * time.Millisecond
	cfg.HeightSyncPeerTips = transport.NewHeightSyncPeerTips()
	client := transport.NewHTTPClient(ts.URL, "escrow-1", user, cfg)
	userSM := statetest.MustStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier)
	session, err := NewSession(userSM, user, "escrow-1", group, []HostClient{client}, verifier)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })

	start := time.Now()
	prepared, err := session.PrepareInference(context.Background(), InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	})
	require.NoError(t, err)
	require.NotNil(t, prepared)
	require.Less(t, time.Since(start), time.Second,
		"PrepareInference must not wait on the seed")
	require.Zero(t, hits.Load(), "PrepareInference must not POST /height-sync")
	require.False(t, session.HeightSeedMissed())
}

func TestSeed_Catalog503ThenServingSucceeds(t *testing.T) {
	env := setupSeedSession(t, []bool{true})
	var hits atomic.Int32
	inner := env.slots[0].server.Config.Handler
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if n < 3 {
			http.Error(w, "version v2 is not present in the governance routing catalog", http.StatusServiceUnavailable)
			return
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(proxy.Close)

	user := env.session.signer
	cfg := seedHTTPConfig()
	cfg.RoutePrefix = seedTestRoutePrefix
	cfg.QueryTimeout = 5 * time.Second
	cfg.HeightSync = heightsync.MustNewAnchorScheduler(10, 1, heightsync.NewPeerTipOracleSource(env.peerTips, env.peerTips.Freshness))
	cfg.HeightSyncPeerTips = env.peerTips
	client := transport.NewHTTPClient(proxy.URL, "escrow-1", user, cfg)
	env.session.clients = []HostClient{client}

	env.session.SeedHeightSync(context.Background())
	require.False(t, env.session.HeightSeedMissed())
	h, ok := client.ObservedHeightNow()
	require.True(t, ok)
	require.Equal(t, uint64(55), h)
	require.Eventually(t, func() bool { return hits.Load() >= 4 }, 5*time.Second, 10*time.Millisecond,
		"3 one-shot retries until serving plus 1 post-quorum sweep, which runs after the seed reports ready")
}

func TestSeed_RetriesOnlyMissingThenSweepsAll(t *testing.T) {
	// Slot 0 serves immediately. Slots 1 and 2 503 until later hits. Retry
	// rounds must skip slot 0; once 2/3 quorum is reached the leftover slot
	// is attempted once more as part of a full-roster sweep, not until it
	// itself succeeds.
	env := setupSeedSession(t, []bool{true, true, true})
	var hits [3]atomic.Int32
	clients := make([]HostClient, 3)
	const catalog503 = "version v2 is not present in the governance routing catalog"
	for i := 0; i < 3; i++ {
		i := i
		inner := env.slots[i].server.Config.Handler
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n := hits[i].Add(1)
			switch i {
			case 0:
				inner.ServeHTTP(w, r)
			case 1:
				if n < 3 {
					http.Error(w, catalog503, http.StatusServiceUnavailable)
					return
				}
				inner.ServeHTTP(w, r)
			default:
				if n < 5 {
					http.Error(w, catalog503, http.StatusServiceUnavailable)
					return
				}
				inner.ServeHTTP(w, r)
			}
		}))
		t.Cleanup(proxy.Close)
		cfg := seedHTTPConfig()
		cfg.RoutePrefix = seedTestRoutePrefix
		cfg.QueryTimeout = 2 * time.Second
		cfg.HeightSync = heightsync.MustNewAnchorScheduler(10, 3, heightsync.NewPeerTipOracleSource(env.peerTips, env.peerTips.Freshness))
		cfg.HeightSyncPeerTips = env.peerTips
		clients[i] = transport.NewHTTPClient(proxy.URL, "escrow-1", env.session.signer, cfg)
	}
	env.session.clients = clients

	env.session.SeedHeightSync(context.Background())
	require.False(t, env.session.HeightSeedMissed())
	require.Eventually(t, func() bool { return hits[0].Load() == 2 && hits[1].Load() == 4 && hits[2].Load() == 4 }, 5*time.Second, 10*time.Millisecond,
		"slot 0 is skipped on retry rounds and hit once more by the post-quorum sweep; slots 1 and 2 retry until quorum, then one sweep each, which runs after the seed reports ready")
}

func TestHeightSeedAttemptTimeout_ClampsToCallerDeadline(t *testing.T) {
	ctx := context.Background()
	got := heightSeedAttemptTimeout(ctx)
	if got != transport.DefaultHeightSeedTimeout {
		t.Fatalf("heightSeedAttemptTimeout(no deadline)=%s, want %s", got, transport.DefaultHeightSeedTimeout)
	}

	short, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	got = heightSeedAttemptTimeout(short)
	if got > 80*time.Millisecond {
		t.Fatalf("heightSeedAttemptTimeout(ctx 80ms)=%s, want <= 80ms", got)
	}

	past, pastCancel := context.WithTimeout(context.Background(), time.Nanosecond)
	pastCancel()
	got = heightSeedAttemptTimeout(past)
	if got != 0 {
		t.Fatalf("heightSeedAttemptTimeout(expired ctx)=%s, want 0", got)
	}
}

func TestSeed_HungSlotDoesNotWaitQueryTimeout(t *testing.T) {
	env := setupSeedSession(t, []bool{true, true})
	release := make(chan struct{})
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(hang.Close)
	t.Cleanup(func() { close(release) })

	cfg := seedHTTPConfig()
	cfg.RoutePrefix = seedTestRoutePrefix
	cfg.QueryTimeout = 30 * time.Second
	cfg.HeightSeedTimeout = 80 * time.Millisecond
	cfg.HeightSync = heightsync.MustNewAnchorScheduler(10, 2, heightsync.NewPeerTipOracleSource(env.peerTips, env.peerTips.Freshness))
	cfg.HeightSyncPeerTips = env.peerTips
	env.session.clients = []HostClient{
		transport.NewHTTPClient(hang.URL, "escrow-1", env.session.signer, cfg),
		transport.NewHTTPClient(env.slots[1].server.URL, "escrow-1", env.session.signer, cfg),
	}

	start := time.Now()
	env.session.SeedHeightSync(context.Background())
	require.Less(t, time.Since(start), 5*time.Second,
		"one hung seed target must not stall the round for QueryTimeout")
	require.False(t, env.session.HeightSeedMissed())
	st := env.peerTips.Snapshot(time.Now())
	require.GreaterOrEqual(t, st.VerifiedOriginators, 1,
		"the live slot must still record its Anchor")
}

// Test flow:
//  1. Seed a session over two live slots and one that hangs until its attempt times out.
//  2. Require the seed to finish as soon as the live slots make quorum, not after the hung slot's attempt timeout.
func TestSeed_QuorumFinishesWithoutWaitingForAHungSlot(t *testing.T) {
	env := setupSeedSession(t, []bool{true, true, true})
	release := make(chan struct{})
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(hang.Close)
	t.Cleanup(func() { close(release) })
	cfg := seedHTTPConfig()
	cfg.RoutePrefix = seedTestRoutePrefix
	cfg.HeightSeedTimeout = 3 * time.Second
	cfg.HeightSync = heightsync.MustNewAnchorScheduler(10, 3, heightsync.NewPeerTipOracleSource(env.peerTips, env.peerTips.Freshness))
	cfg.HeightSyncPeerTips = env.peerTips
	env.session.clients = []HostClient{
		transport.NewHTTPClient(env.slots[0].server.URL, "escrow-1", env.session.signer, cfg),
		transport.NewHTTPClient(env.slots[1].server.URL, "escrow-1", env.session.signer, cfg),
		transport.NewHTTPClient(hang.URL, "escrow-1", env.session.signer, cfg),
	}

	start := time.Now()
	env.session.SeedHeightSync(context.Background())

	require.Less(t, time.Since(start), time.Second, "quorum was met by the live slots; the hung slot must not hold the seed")
	require.False(t, env.session.HeightSeedMissed())
}

func TestClassifySeedVerdict(t *testing.T) {
	v, reason := classifySeedVerdict(true, nil)
	require.Equal(t, seedAnchored, v)
	require.Equal(t, "ok", reason)

	v, reason = classifySeedVerdict(false, nil)
	require.Equal(t, seedRetryLater, v)
	require.Equal(t, "omit", reason)

	v, _ = classifySeedVerdict(false, &transport.UpstreamStatusError{StatusCode: http.StatusNotFound})
	require.Equal(t, seedDeclined, v)

	v, _ = classifySeedVerdict(false, &transport.UpstreamStatusError{StatusCode: http.StatusNotImplemented})
	require.Equal(t, seedDeclined, v)

	v, _ = classifySeedVerdict(false, &transport.UpstreamStatusError{
		StatusCode:    http.StatusBadRequest,
		DevshardError: transport.DevshardErrorNotImplemented,
	})
	require.Equal(t, seedDeclined, v)

	v, _ = classifySeedVerdict(false, transport.ErrHTTPSessionRetired)
	require.Equal(t, seedDeclined, v)

	v, _ = classifySeedVerdict(false, &transport.UpstreamStatusError{
		StatusCode:    http.StatusGone,
		DevshardError: transport.DevshardErrorHTTPSessionRetired,
	})
	require.Equal(t, seedDeclined, v)

	v, _ = classifySeedVerdict(false, &transport.UpstreamStatusError{StatusCode: http.StatusServiceUnavailable})
	require.Equal(t, seedRetryLater, v)

	v, _ = classifySeedVerdict(false, &transport.UpstreamStatusError{StatusCode: http.StatusTooManyRequests})
	require.Equal(t, seedRetryLater, v)

	v, _ = classifySeedVerdict(false, context.DeadlineExceeded)
	require.Equal(t, seedRetryLater, v)

	v, _ = classifySeedVerdict(false, fmt.Errorf("participant request budget exhausted"))
	require.Equal(t, seedRetryLater, v)

	v, _ = classifySeedVerdict(false, connect.NewError(connect.CodeNotFound, transport.ErrHeightSyncSeedDisabled))
	require.Equal(t, seedDeclined, v)

	v, _ = classifySeedVerdict(false, connect.NewError(connect.CodeUnimplemented, errors.New("method is not implemented")))
	require.Equal(t, seedDeclined, v)

	unavailable := connect.NewError(connect.CodeUnavailable, errors.New("requests disabled"))
	unavailable.Meta().Set(transport.HeaderDevshardError, transport.DevshardErrorRequestsDisabled)
	v, _ = classifySeedVerdict(false, unavailable)
	require.Equal(t, seedRetryLater, v)
}

func TestWaitHeightSeed_GateOffIsNil(t *testing.T) {
	env := setupSeedSession(t, []bool{false, false, false})
	require.NoError(t, env.session.WaitHeightSeed(context.Background()))
}

func TestSeed_Gap1DeclinedMakesMissedThenReprobeSucceeds(t *testing.T) {
	env := setupSeedSession(t, []bool{true, true, true}, WithRequireHeightSeed(true))
	var serve [3]atomic.Bool
	serve[0].Store(true)
	var hits [3]atomic.Int32
	clients := make([]HostClient, 3)
	for i := 0; i < 3; i++ {
		i := i
		inner := env.slots[i].server.Config.Handler
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/devshard/v2/healthz" {
				w.WriteHeader(http.StatusOK)
				return
			}
			hits[i].Add(1)
			if !serve[i].Load() {
				http.NotFound(w, r)
				return
			}
			inner.ServeHTTP(w, r)
		}))
		t.Cleanup(proxy.Close)
		cfg := seedHTTPConfig()
		cfg.RoutePrefix = seedTestRoutePrefix
		cfg.QueryTimeout = 2 * time.Second
		cfg.HeightSync = heightsync.MustNewAnchorScheduler(10, 3, heightsync.NewPeerTipOracleSource(env.peerTips, env.peerTips.Freshness))
		cfg.HeightSyncPeerTips = env.peerTips
		clients[i] = transport.NewHTTPClient(proxy.URL, "escrow-1", env.session.signer, cfg)
	}
	env.session.clients = clients

	start := time.Now()
	env.session.SeedHeightSync(context.Background())
	require.Less(t, time.Since(start), 5*time.Second, "gap 1 must miss immediately, not after the 30s gate")
	require.True(t, env.session.HeightSeedMissed())
	st := env.session.SeedRPCStatus()
	require.Equal(t, HeightSeedStateMissed, st.State)
	require.Equal(t, 1, st.Seeded)
	declined := 0
	for _, rec := range st.SlotOutcomes {
		if rec.Verdict == seedDeclined.String() {
			declined++
		}
	}
	require.Equal(t, 2, declined)

	serve[1].Store(true)
	require.Eventually(t, func() bool {
		return env.session.SeedRPCStatus().State == HeightSeedStateOK
	}, 12*time.Second, 50*time.Millisecond, "re-probing a declined slot must be able to reach ok")
	require.False(t, env.session.HeightSeedMissed())
	require.Greater(t, hits[1].Load(), int32(1))
}

func TestHeartbeatDisabled_SeedGateStillBlocks(t *testing.T) {
	env := setupSeedSession(t, []bool{true}, WithRequireHeightSeed(true), WithDisableHeightSyncHeartbeat(true))
	inner := env.slots[0].server.Config.Handler
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/devshard/v2/healthz" {
			http.Error(w, "undeclared", http.StatusServiceUnavailable)
			return
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(proxy.Close)
	cfg := seedHTTPConfig()
	cfg.RoutePrefix = seedTestRoutePrefix
	cfg.QueryTimeout = 2 * time.Second
	cfg.HeightSync = heightsync.MustNewAnchorScheduler(10, 1, heightsync.NewPeerTipOracleSource(env.peerTips, env.peerTips.Freshness))
	cfg.HeightSyncPeerTips = env.peerTips
	env.session.clients = []HostClient{
		transport.NewHTTPClient(proxy.URL, "escrow-1", env.session.signer, cfg),
	}

	logs := captureInfoLog(t)
	base := env.session.Nonce()
	env.session.StartHeartbeatLoop()
	require.True(t, logs.contains("heartbeat loop disabled"))
	require.Nil(t, env.session.heartbeatStop)
	require.Nil(t, env.session.heightSeedStop, "the gateway does not start the seed loop")
	require.NotNil(t, env.session.bootstrapStop, "the bootstrap span still waits for catalog")

	require.Equal(t, HeightSeedStateCatalogPending, env.session.HeightSeedStatus().State)
	require.Equal(t, base, env.session.Nonce())

	// captureInfoLog restores logging.current in a cleanup, and cleanups run
	// LIFO, so that write happens before setupSeedSession closes the session.
	// The bootstrap span is still in WaitRouterCatalog calling logging.Debug
	// until Close. Stop it here so the restore does not race that read.
	require.NoError(t, env.session.Close())
}

func TestSeed_Gap2ClockStartsAfterCatalog(t *testing.T) {
	env := setupSeedSession(t, []bool{true}, WithRequireHeightSeed(true))
	var catalogReady atomic.Bool
	var seedHits atomic.Int32
	inner := env.slots[0].server.Config.Handler
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/devshard/v2/healthz" {
			if !catalogReady.Load() {
				http.Error(w, "undeclared", http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		seedHits.Add(1)
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(proxy.Close)
	cfg := seedHTTPConfig()
	cfg.RoutePrefix = seedTestRoutePrefix
	cfg.QueryTimeout = 2 * time.Second
	cfg.HeightSync = heightsync.MustNewAnchorScheduler(10, 1, heightsync.NewPeerTipOracleSource(env.peerTips, env.peerTips.Freshness))
	cfg.HeightSyncPeerTips = env.peerTips
	env.session.clients = []HostClient{
		transport.NewHTTPClient(proxy.URL, "escrow-1", env.session.signer, cfg),
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	err := env.session.WaitHeightSeed(waitCtx)
	cancel()
	var se *HeightSeedError
	require.ErrorAs(t, err, &se)
	require.Equal(t, transport.DevshardErrorCatalogPending, se.Code)
	require.False(t, env.session.HeightSeedMissed())
	require.Equal(t, HeightSeedStateCatalogPending, env.session.SeedRPCStatus().State)
	require.Zero(t, env.session.heightSeedGateUntil.Load(), "30s clock must not start during catalog wait")
	require.Zero(t, seedHits.Load(), "seed POSTs must wait for catalog admission")

	catalogReady.Store(true)
	require.Eventually(t, func() bool {
		return env.session.SeedRPCStatus().State == HeightSeedStateOK
	}, 5*time.Second, 50*time.Millisecond)
	require.Greater(t, env.session.heightSeedGateUntil.Load(), int64(0))
	require.False(t, env.session.HeightSeedMissed())
	require.Greater(t, seedHits.Load(), int32(0))
}

func TestSeed_Gap4AdmissionErrorIsRetryLater(t *testing.T) {
	env := setupSeedSession(t, []bool{true}, WithRequireHeightSeed(true))
	admission := &seedTestAdmission{err: fmt.Errorf("participant request budget exhausted")}
	inner := env.slots[0].server.Config.Handler
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/devshard/v2/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(proxy.Close)
	cfg := seedHTTPConfig()
	cfg.RoutePrefix = seedTestRoutePrefix
	cfg.QueryTimeout = 2 * time.Second
	cfg.ParticipantKey = "shared-host"
	cfg.Admission = admission
	cfg.HeightSync = heightsync.MustNewAnchorScheduler(10, 1, heightsync.NewPeerTipOracleSource(env.peerTips, env.peerTips.Freshness))
	cfg.HeightSyncPeerTips = env.peerTips
	env.session.clients = []HostClient{
		transport.NewHTTPClient(proxy.URL, "escrow-1", env.session.signer, cfg),
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	err := env.session.WaitHeightSeed(waitCtx)
	cancel()
	require.Error(t, err)
	require.False(t, env.session.HeightSeedMissed(),
		"local admission rejection is retry-later, not an immediate gap-1 miss")
	require.Greater(t, admission.hits.Load(), int32(1), "seed loop must keep attempting throttled slots")
	st := env.session.SeedRPCStatus()
	require.Equal(t, HeightSeedStatePending, st.State)
	require.Equal(t, 1, len(st.SlotOutcomes))
	require.Equal(t, seedRetryLater.String(), st.SlotOutcomes[0].Verdict)
}

type seedTestAdmission struct {
	hits atomic.Int32
	err  error
}

func (a *seedTestAdmission) AllowRequest(string, string) error {
	a.hits.Add(1)
	return a.err
}

func (a *seedTestAdmission) ObserveResult(string, string, int) {}

func (a *seedTestAdmission) ObserveTransportFailure(string, string, error) {}

func rebuildSeedClientsWithLogOracle(t *testing.T, env *seedEnv, logOracle blocks.BlockOracle) {
	t.Helper()
	n := len(env.slots)
	src := heightsync.NewPeerTipOracleSource(env.peerTips, env.peerTips.Freshness)
	sched := heightsync.MustNewAnchorScheduler(10, uint64(n), src)
	clients := make([]HostClient, n)
	for i := range env.slots {
		cfg := seedHTTPConfig()
		cfg.RoutePrefix = seedTestRoutePrefix
		cfg.QueryTimeout = 2 * time.Second
		cfg.HeightSync = sched
		cfg.HeightSyncPeerTips = env.peerTips
		cfg.HeightSyncLogOracle = logOracle
		clients[i] = transport.NewHTTPClient(env.slots[i].server.URL, "escrow-1", env.session.signer, cfg)
	}
	env.session.clients = clients
}

type seedLogOracle struct {
	hdr *blocks.Header
}

func (o *seedLogOracle) Latest(context.Context) (*blocks.Header, error) {
	if o == nil || o.hdr == nil {
		return nil, nil
	}
	h := *o.hdr
	h.BlockHash = append([]byte(nil), o.hdr.BlockHash...)
	return &h, nil
}

func (o *seedLogOracle) At(context.Context, int64) (*blocks.Header, error) { return nil, nil }

func (o *seedLogOracle) Prove(context.Context, string, int64) (*blocks.Proof, error) {
	return nil, blocks.ErrProveNotImplemented
}

func (o *seedLogOracle) Subscribe(context.Context, int64) (<-chan *blocks.Header, error) {
	ch := make(chan *blocks.Header)
	close(ch)
	return ch, nil
}

func TestSeed_WarmLogOracleDoesNotCountTowardQuorum(t *testing.T) {
	env := setupSeedSession(t, []bool{false, false, false}, WithRequireHeightSeed(true))
	rebuildSeedClientsWithLogOracle(t, env, &seedLogOracle{hdr: &blocks.Header{
		Height: 999, ChainID: "test-chain", BlockHash: []byte{0x99},
	}})

	env.session.SeedHeightSync(context.Background())
	require.True(t, env.session.HeightSeedMissed())
	require.Equal(t, 0, env.session.SeedRPCStatus().Seeded)

	c := env.session.clients[0].(*transport.HTTPClient)
	h, ok := c.ObservedHeightNow()
	require.False(t, ok, "local follower tip must not count as a seeded host Anchor")
	require.Equal(t, uint64(0), h)

	waitCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	err := env.session.WaitHeightSeed(waitCtx)
	cancel()
	var se *HeightSeedError
	require.ErrorAs(t, err, &se)
	require.Equal(t, transport.DevshardErrorHeightSeedIncomplete, se.Code)
}

func TestSeed_CourierWorksWithWarmLogOracle(t *testing.T) {
	env := setupSeedSession(t, []bool{true, true, true}, WithRequireHeightSeed(true))
	rebuildSeedClientsWithLogOracle(t, env, &seedLogOracle{hdr: &blocks.Header{
		Height: 999, ChainID: "test-chain", BlockHash: []byte{0x99},
	}})

	env.session.SeedHeightSync(context.Background())
	require.False(t, env.session.HeightSeedMissed())
	require.Equal(t, HeightSeedStateOK, env.session.SeedRPCStatus().State)

	c := env.session.clients[0].(*transport.HTTPClient)
	h, ok := c.ObservedHeightNow()
	require.True(t, ok)
	require.Equal(t, uint64(55), h, "stamp must come from the host seed, not the local follower")
}
