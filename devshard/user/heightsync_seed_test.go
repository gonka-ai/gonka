package user

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"devshard/heightsync"
	"devshard/host"
	"devshard/internal/statetest"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/storage"
	"devshard/stub"
	"devshard/transport"
	"devshard/types"
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

func setupSeedSession(t *testing.T, seedRPC []bool) *seedEnv {
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
		or := &sessionOracle{hash: []byte{0xaa, byte(i + 1)}}
		or.height.Store(55)
		sm := statetest.MustStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier)
		store := storage.NewMemory()
		require.NoError(t, store.CreateSession(storage.CreateSessionParams{
			EscrowID: "escrow-1", Version: testutil.RuntimeTestVersion, Config: config, Group: group, InitialBalance: 100000,
		}))
		h, err := host.NewHost(sm, hosts[i], stub.NewInferenceEngine(), "escrow-1", group, nil,
			host.WithGrace(100), host.WithStorage(store), host.WithChainOracle(or))
		require.NoError(t, err)
		hostSched := heightsync.MustNewAnchorSchedulerFromOracle(10, uint64(n), or)
		opts := []transport.ServerOption{transport.WithHeightSync(hostSched, or)}
		if !seedRPC[i] {
			opts = append(opts, transport.WithHeightSyncSeedRPC(false))
		}
		srv, err := transport.NewServer(h, store, verifier, user.Address(), opts...)
		require.NoError(t, err)
		e := echo.New()
		g := e.Group(seedTestRoutePrefix)
		registerSeedRoutes(g, srv)
		ts := httptest.NewServer(e)
		t.Cleanup(ts.Close)

		cfg := transport.DefaultClientConfig()
		cfg.RoutePrefix = seedTestRoutePrefix
		cfg.QueryTimeout = 2 * time.Second
		cfg.HeightSync = clientSched
		cfg.HeightSyncPeerTips = peerTips
		c := transport.NewHTTPClient(ts.URL, "escrow-1", user, cfg)
		slots[i] = seedSlot{server: ts, client: c}
		clients[i] = c
	}

	userSM := statetest.MustStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier)
	session, err := NewSession(userSM, user, "escrow-1", group, clients, verifier,
		WithHeightSyncCadence(10, uint64(n)),
	)
	require.NoError(t, err)
	return &seedEnv{session: session, peerTips: peerTips, slots: slots}
}

func TestSeed_SessionOpenStampsNonceOne(t *testing.T) {
	// Cold-start seed primes ObservedHeightNow before the first outbound, so
	// nonce 1's MsgHeartbeat carries the seeded height (spec §18.5).
	// StartInference stamps are a later nonce and are not on this bump.
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

	require.NoError(t, env.session.MaybeHeartbeat(ctx))
	diffs := env.session.Diffs()
	require.NotEmpty(t, diffs)
	require.Equal(t, uint64(1), diffs[0].Nonce)
	var hb *types.MsgHeartbeat
	for _, tx := range diffs[0].Txs {
		if inner := tx.GetHeartbeat(); inner != nil {
			hb = inner
		}
		require.Nil(t, tx.GetStartInference(), "first outbound after seed is not an inference")
	}
	require.NotNil(t, hb)
	require.Equal(t, uint64(55), hb.ObservedHeight)
	require.True(t, heightsync.StampPresent(hb.ObservedBlockHash), "seeded hash must ride the first heartbeat")
	require.Equal(t, 0, env.session.HeartbeatSkippedNoHeight())
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

	require.NoError(t, env.session.MaybeHeartbeat(context.Background()))
	require.Empty(t, env.session.Diffs())
	require.Equal(t, 1, env.session.HeartbeatSkippedNoHeight())

	_, err := env.session.SendInference(context.Background(), InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	})
	require.NoError(t, err, "total seed miss must not fail the session")
	require.Equal(t, uint64(1), env.session.Nonce())
}

func TestSeed_DoesNotAdvanceHLastOrConsumeNonce(t *testing.T) {
	// Seed is a transport read. Heartbeat obligation stays armed.
	env := setupSeedSession(t, []bool{true, true, true})
	env.session.SeedHeightSync(context.Background())

	require.Equal(t, uint64(0), env.session.Nonce())
	require.Empty(t, env.session.Diffs())
	require.Equal(t, uint64(0), env.session.HeartbeatTurnTracker().LastCompletedHeight())
	require.False(t, env.session.HeartbeatTurnTracker().CompletedAtOrAbove(55))

	require.NoError(t, env.session.MaybeHeartbeat(context.Background()))
	require.NotEmpty(t, env.session.Diffs(), "seeded session that never worked still owes a turn within K_hb")
	require.Equal(t, uint64(1), env.session.Diffs()[0].Nonce)
}

func TestSeed_OnceDoesNotRetryAfterMiss(t *testing.T) {
	env := setupSeedSession(t, []bool{false, false, false})
	env.session.SeedHeightSync(context.Background())
	require.True(t, env.session.HeightSeedMissed())
	env.session.SeedHeightSync(context.Background())
	require.True(t, env.session.HeightSeedMissed())
}

func TestSeed_Catalog503RetriesUntilDeadlineThenMisses(t *testing.T) {
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
	cfg := transport.DefaultClientConfig()
	cfg.RoutePrefix = "/"
	cfg.QueryTimeout = 80 * time.Millisecond
	cfg.HeightSyncPeerTips = transport.NewHeightSyncPeerTips()
	client := transport.NewHTTPClient(ts.URL, "escrow-1", user, cfg)
	userSM := statetest.MustStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier)
	session, err := NewSession(userSM, user, "escrow-1", group, []HostClient{client}, verifier)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	session.SeedHeightSync(ctx)
	require.True(t, session.HeightSeedMissed())
	require.Greater(t, hits.Load(), int32(1), "catalog 503 must retry until the seed deadline")
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
	cfg := transport.DefaultClientConfig()
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
	require.GreaterOrEqual(t, hits.Load(), int32(3))
}
