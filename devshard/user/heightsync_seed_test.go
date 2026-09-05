package user

import (
	"context"
	"fmt"
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

func setupSeedSession(t *testing.T, seedRPC []bool, opts ...SessionOption) *seedEnv {
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
		serverOpts := []transport.ServerOption{transport.WithHeightSync(hostSched, or)}
		if !seedRPC[i] {
			serverOpts = append(serverOpts, transport.WithHeightSyncSeedRPC(false))
		}
		srv, err := transport.NewServer(h, store, verifier, user.Address(), serverOpts...)
		require.NoError(t, err)
		e := echo.New()
		e.GET("/v2/healthz", func(c echo.Context) error {
			return c.NoContent(http.StatusOK)
		})
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
// there — and that is exactly the P1 hole. So the log waits for a host-signed
// stamp of its own, and until then the cadence stays disarmed.
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

	require.NoError(t, env.session.MaybeHeartbeat(ctx))
	require.Empty(t, env.session.Diffs(),
		"a seeded height is not F: it may ride the envelope, never the log")
	require.Equal(t, 1, env.session.HeartbeatSkippedNoHeight())

	// One inference, and the executor's own stamp sets F. Now the log has a
	// height the verifier can attribute, and the heartbeat carries that.
	_, err := env.session.SendInference(ctx, InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	})
	require.NoError(t, err)
	require.NoError(t, env.session.SendPendingDiff(ctx))
	base := env.session.Nonce()

	require.NoError(t, env.session.MaybeHeartbeat(ctx))
	var hb *types.MsgHeartbeat
	for _, d := range env.session.Diffs() {
		if d.Nonce <= base {
			continue
		}
		for _, tx := range d.Txs {
			if inner := tx.GetHeartbeat(); inner != nil && hb == nil {
				hb = inner
			}
		}
	}
	require.NotNil(t, hb, "the first host stamp arms the cadence")
	require.Equal(t, uint64(55), hb.ObservedHeight)
	require.True(t, heightsync.StampPresent(hb.ObservedBlockHash),
		"the floor's hash rides the heartbeat, so the stamp is checkable against the log")
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
	// Seed is a transport read: it proves nothing about the log, so it neither
	// completes a turn nor discharges the obligation. The turn it owes simply
	// cannot open yet, because there is no F to stamp — the obligation stays
	// armed and the skip is counted rather than silently forgotten.
	env := setupSeedSession(t, []bool{true, true, true})
	env.session.SeedHeightSync(context.Background())

	require.Equal(t, uint64(0), env.session.Nonce())
	require.Empty(t, env.session.Diffs())
	require.Equal(t, uint64(0), env.session.HeartbeatTurnTracker().LastCompletedHeight())
	require.False(t, env.session.HeartbeatTurnTracker().CompletedAtOrAbove(55))

	require.NoError(t, env.session.MaybeHeartbeat(context.Background()))
	require.Empty(t, env.session.Diffs(), "a seeded height is not F and does not enter the log")
	require.Equal(t, 1, env.session.HeartbeatSkippedNoHeight(),
		"seeded session that never worked still owes a turn; it just has nothing to stamp yet")
	require.Zero(t, env.session.HeartbeatTurnovers())
}

func TestSeed_DeclinedSlotsAreReprobed(t *testing.T) {
	env := setupSeedSession(t, []bool{false}, WithRequireHeightSeed(true))
	var hits atomic.Int32
	inner := env.slots[0].server.Config.Handler
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		hits.Add(1)
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(proxy.Close)
	cfg := transport.DefaultClientConfig()
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
	cfg := transport.DefaultClientConfig()
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
	cfg := transport.DefaultClientConfig()
	cfg.RoutePrefix = "/"
	cfg.QueryTimeout = 80 * time.Millisecond
	cfg.HeightSyncPeerTips = transport.NewHeightSyncPeerTips()
	client := transport.NewHTTPClient(ts.URL, "escrow-1", user, cfg)
	userSM := statetest.MustStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier)
	session, err := NewSession(userSM, user, "escrow-1", group, []HostClient{client}, verifier)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })

	start := time.Now()
	prepared, err := session.PrepareInference(InferenceParams{
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
	require.GreaterOrEqual(t, hits.Load(), int32(4),
		"3 one-shot retries until serving plus 1 post-quorum sweep")
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
		cfg := transport.DefaultClientConfig()
		cfg.RoutePrefix = seedTestRoutePrefix
		cfg.QueryTimeout = 2 * time.Second
		cfg.HeightSync = heightsync.MustNewAnchorScheduler(10, 3, heightsync.NewPeerTipOracleSource(env.peerTips, env.peerTips.Freshness))
		cfg.HeightSyncPeerTips = env.peerTips
		clients[i] = transport.NewHTTPClient(proxy.URL, "escrow-1", env.session.signer, cfg)
	}
	env.session.clients = clients

	env.session.SeedHeightSync(context.Background())
	require.False(t, env.session.HeightSeedMissed())
	require.Equal(t, int32(2), hits[0].Load(),
		"already-seeded slot is skipped on retry rounds and hit once more on the post-quorum sweep")
	require.Equal(t, int32(4), hits[1].Load(),
		"slot 1: 3 attempts to succeed + 1 sweep")
	require.Equal(t, int32(4), hits[2].Load(),
		"slot 2 keeps retrying until quorum, then one sweep, not until it itself succeeds")
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

	cfg := transport.DefaultClientConfig()
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

	v, _ = classifySeedVerdict(false, &transport.UpstreamStatusError{StatusCode: http.StatusServiceUnavailable})
	require.Equal(t, seedRetryLater, v)

	v, _ = classifySeedVerdict(false, &transport.UpstreamStatusError{StatusCode: http.StatusTooManyRequests})
	require.Equal(t, seedRetryLater, v)

	v, _ = classifySeedVerdict(false, context.DeadlineExceeded)
	require.Equal(t, seedRetryLater, v)

	v, _ = classifySeedVerdict(false, fmt.Errorf("participant request budget exhausted"))
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
			if r.URL.Path == "/v2/healthz" {
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
		cfg := transport.DefaultClientConfig()
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
	st := env.session.HeightSeedStatus()
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
		return env.session.HeightSeedStatus().State == HeightSeedStateOK
	}, 12*time.Second, 50*time.Millisecond, "re-probing a declined slot must be able to reach ok")
	require.False(t, env.session.HeightSeedMissed())
	require.Greater(t, hits[1].Load(), int32(1))
}

func TestSeed_Gap2ClockStartsAfterCatalog(t *testing.T) {
	env := setupSeedSession(t, []bool{true}, WithRequireHeightSeed(true))
	var catalogReady atomic.Bool
	var seedHits atomic.Int32
	inner := env.slots[0].server.Config.Handler
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/healthz" {
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
	cfg := transport.DefaultClientConfig()
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
	require.Equal(t, HeightSeedStateCatalogPending, env.session.HeightSeedStatus().State)
	require.Zero(t, env.session.heightSeedGateUntil.Load(), "30s clock must not start during catalog wait")
	require.Zero(t, seedHits.Load(), "seed POSTs must wait for catalog admission")

	catalogReady.Store(true)
	require.Eventually(t, func() bool {
		return env.session.HeightSeedStatus().State == HeightSeedStateOK
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
		if r.URL.Path == "/v2/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(proxy.Close)
	cfg := transport.DefaultClientConfig()
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
	st := env.session.HeightSeedStatus()
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
		cfg := transport.DefaultClientConfig()
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
	require.Equal(t, 0, env.session.HeightSeedStatus().Seeded)

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
	require.Equal(t, HeightSeedStateOK, env.session.HeightSeedStatus().State)

	c := env.session.clients[0].(*transport.HTTPClient)
	h, ok := c.ObservedHeightNow()
	require.True(t, ok)
	require.Equal(t, uint64(55), h, "stamp must come from the host seed, not the local follower")
}
