package transport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"common/chainoracle/blocks"
	"devshard/heightsync"
	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/types"
)

func setupClientTestEnvWithHeightSync(t *testing.T) (*HTTPClient, *httptest.Server, *signing.Secp256k1Signer, *heightSyncTestOracle) {
	t.Helper()
	hostSigner := testutil.MustGenerateKey(t)
	userSigner := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup([]*signing.Secp256k1Signer{hostSigner})
	config := testutil.DefaultConfig(1)
	verifier := signing.NewSecp256k1Verifier()

	sm, err := state.NewStateMachine("escrow-1", config, group, 100000, userSigner.Address(), verifier, testutil.MustMemoryStore(t, "escrow-1", userSigner.Address(), config, group, 100000))
	require.NoError(t, err)
	engine := stub.NewInferenceEngine()
	store := storage.NewMemory()
	require.NoError(t, store.CreateSession(storage.CreateSessionParams{EscrowID: "escrow-1", Version: testutil.RuntimeTestVersion, Config: config, Group: group, InitialBalance: 100000}))

	h, err := host.NewHost(sm, hostSigner, engine, "escrow-1", group, nil, host.WithGrace(100), host.WithStorage(store))
	require.NoError(t, err)

	or := &heightSyncTestOracle{hdr: &blocks.Header{
		Height:    42,
		ChainID:   "test-chain",
		BlockHash: []byte{0x01, 0x02},
	}}
	// Separate schedulers: host and user must not share one AnchorScheduler instance.
	hostSched := heightsync.MustNewAnchorSchedulerFromOracle(10, 1, or)
	clientSched := heightsync.MustNewAnchorSchedulerFromOracle(10, 1, or)

	srv, err := NewServer(h, store, verifier, userSigner.Address(), WithHeightSync(hostSched, or))
	require.NoError(t, err)

	e := echo.New()
	g := e.Group("/devshard/v2")
	registerServer(g, srv)

	ts := httptest.NewServer(e)
	t.Cleanup(ts.Close)

	cfg := DefaultClientConfig()
	cfg.RoutePrefix = testRoutePrefix
	cfg.HeightSync = clientSched
	cfg.HeightSyncLogOracle = or
	client := NewHTTPClient(ts.URL, "escrow-1", userSigner, cfg)
	return client, ts, userSigner, or
}

func TestHTTPClient_Send_HeightSync_ProtobufRequestAndAudit(t *testing.T) {
	client, _, userSigner, _ := setupClientTestEnvWithHeightSync(t)
	ctx := context.Background()

	diff := testutil.SignDiff(t, userSigner, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	_, err := client.Send(ctx, host.HostRequest{
		Diffs: []types.Diff{diff},
		Nonce: 1,
		Payload: &host.InferencePayload{
			Prompt:      testutil.TestPrompt,
			Model:       "llama",
			InputLength: 100,
			MaxTokens:   testutil.TestMaxTokens,
			StartedAt:   1000,
		},
	}, nil, nil)
	require.NoError(t, err)

	ring := client.HeightSyncAuditRing()
	require.NotNil(t, ring)
	userAddr := userSigner.Address()
	foundReq := false
	foundResp := false
	for _, a := range ring.List(userAddr) {
		if a.Direction == "request" && a.MainnetHeight == 42 {
			foundReq = true
		}
	}
	for _, a := range ring.List(client.baseURL) {
		if a.Direction == "response" && a.MainnetHeight == 42 {
			foundResp = true
		}
	}
	require.True(t, foundReq, "expected outbound user anchor in audit ring")
	require.True(t, foundResp, "expected inbound host anchor from SSE in audit ring")
}

func TestHTTPClient_Send_CourierLazyAnchorMarksPropagated(t *testing.T) {
	t.Helper()
	hostSigner := testutil.MustGenerateKey(t)
	userSigner := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup([]*signing.Secp256k1Signer{hostSigner})
	config := testutil.DefaultConfig(1)
	verifier := signing.NewSecp256k1Verifier()

	sm, err := state.NewStateMachine("escrow-1", config, group, 100000, userSigner.Address(), verifier, testutil.MustMemoryStore(t, "escrow-1", userSigner.Address(), config, group, 100000))
	require.NoError(t, err)
	engine := stub.NewInferenceEngine()
	store := storage.NewMemory()
	require.NoError(t, store.CreateSession(storage.CreateSessionParams{EscrowID: "escrow-1", Version: testutil.RuntimeTestVersion, Config: config, Group: group, InitialBalance: 100000}))

	h, err := host.NewHost(sm, hostSigner, engine, "escrow-1", group, nil, host.WithGrace(100), host.WithStorage(store))
	require.NoError(t, err)

	or := &heightSyncTestOracle{hdr: &blocks.Header{
		Height:    42,
		ChainID:   "test-chain",
		BlockHash: []byte{0x01, 0x02},
	}}
	hostSched := heightsync.MustNewAnchorSchedulerFromOracle(8, 4, or)

	srv, err := NewServer(h, store, verifier, userSigner.Address(), WithHeightSync(hostSched, or))
	require.NoError(t, err)

	e := echo.New()
	g := e.Group("/devshard/v2")
	registerServer(g, srv)
	ts := httptest.NewServer(e)
	t.Cleanup(ts.Close)

	peerTips := NewHeightSyncPeerTips()
	blob, sig := []byte("blob"), []byte{1}
	peerTips.RecordOriginWithBlob(&heightsync.HeightSyncSection{
		ProofType:             heightsync.AnchorProofType,
		MainnetHeight:         51,
		MainnetBlockHashHex:   "aabb",
		OriginatorSenderID:    "gonka1origin",
		OriginatorTimestampMs: time.Now().UnixMilli(),
	}, blob, sig)
	src := heightsync.NewPeerTipOracleSource(peerTips, peerTips.Freshness)
	clientSched := heightsync.MustNewAnchorScheduler(8, 4, src)

	cfg := DefaultClientConfig()
	cfg.RoutePrefix = testRoutePrefix
	cfg.HeightSync = clientSched
	cfg.HeightSyncPeerTips = peerTips
	client := NewHTTPClient(ts.URL, "escrow-1", userSigner, cfg)

	require.True(t, peerTips.ShouldPropagateTo(ts.URL, 51))

	ctx := context.Background()
	plainCfg := DefaultClientConfig()
	plainCfg.RoutePrefix = testRoutePrefix
	plain := NewHTTPClient(ts.URL, "escrow-1", userSigner, plainCfg)
	payload := &host.InferencePayload{
		Prompt:      testutil.TestPrompt,
		Model:       "llama",
		InputLength: 100,
		MaxTokens:   testutil.TestMaxTokens,
		StartedAt:   1000,
	}
	for n := uint64(1); n <= 4; n++ {
		diff := testutil.SignDiff(t, userSigner, "escrow-1", n, []*types.DevshardTx{testutil.StartTx(n)})
		_, err = plain.Send(ctx, host.HostRequest{Diffs: []types.Diff{diff}, Nonce: n, Payload: payload}, nil, nil)
		require.NoError(t, err, "advance session to omit window at nonce=%d", n)
	}

	diff := testutil.SignDiff(t, userSigner, "escrow-1", 5, []*types.DevshardTx{testutil.StartTx(5)})
	_, err = client.Send(ctx, host.HostRequest{
		Diffs:   []types.Diff{diff},
		Nonce:   5,
		Payload: payload,
	}, nil, nil)
	require.NoError(t, err)
	require.False(t, peerTips.ShouldPropagateTo(ts.URL, 51), "successful lazy send must MarkPropagated for recipient baseURL")
}

func TestHostRequest_ForceHeightSyncAnchor_TransportJSONRoundTrip(t *testing.T) {
	hr := host.HostRequest{
		Diffs:                 nil,
		Nonce:                 7,
		ForceHeightSyncAnchor: true,
	}
	ir, err := HostRequestToJSON(hr)
	require.NoError(t, err)
	require.True(t, ir.ForceHeightSyncAnchor)
	hr2, err := HostRequestFromJSON(ir)
	require.NoError(t, err)
	require.True(t, hr2.ForceHeightSyncAnchor)
	require.Equal(t, uint64(7), hr2.Nonce)
}

func TestHTTPClient_ParseSSE_InboundHeightSyncAudit(t *testing.T) {
	cfg := DefaultClientConfig()
	client := &HTTPClient{
		config:          cfg,
		baseURL:         "http://executor-host",
		heightSyncAudit: heightsync.NewAuditRing(4),
	}
	sse := `data: {"devshard_receipt":{"state_sig":"c2ln","state_hash":"aGFzaA==","nonce":1,"receipt":"cmVjZWlwdA==","confirmed_at":1000},"height_sync":{"proof_type":"height-anchor-v1","mainnet_height":99,"mainnet_block_hash_hex":"aabb","timestamp_unix_ms":1,"direction":"response"}}` + "\n\n"

	res, err := client.parseSSEResponse(context.Background(), strings.NewReader(sse), nil, nil)
	require.NoError(t, err)
	require.Equal(t, uint64(1), res.Nonce)

	var saw bool
	for _, a := range client.HeightSyncAuditRing().List("http://executor-host") {
		if a.Direction == "response" && a.MainnetHeight == 99 {
			saw = true
		}
	}
	require.True(t, saw)
}

func TestObservedHeightNow_CacheEmpty(t *testing.T) {
	peerTips := NewHeightSyncPeerTips()
	src := heightsync.NewPeerTipOracleSource(peerTips, peerTips.Freshness)
	sched := heightsync.MustNewAnchorScheduler(8, 4, src)
	cfg := DefaultClientConfig()
	cfg.HeightSync = sched
	cfg.HeightSyncPeerTips = peerTips
	client := NewHTTPClient("http://example.invalid", "escrow-1", testutil.MustGenerateKey(t), cfg)

	h, ok := client.ObservedHeightNow()
	require.False(t, ok)
	require.Equal(t, uint64(0), h)
}

func TestObservedHeightNow_FreshTip(t *testing.T) {
	peerTips := NewHeightSyncPeerTips()
	now := time.Now().UnixMilli()
	peerTips.RecordOriginWithBlob(&heightsync.HeightSyncSection{
		ProofType:             heightsync.AnchorProofType,
		MainnetHeight:         99,
		MainnetBlockHashHex:   "aabb",
		OriginatorSenderID:    "gonka1host",
		OriginatorTimestampMs: now,
	}, []byte("blob"), []byte{1})
	src := heightsync.NewPeerTipOracleSource(peerTips, peerTips.Freshness)
	sched := heightsync.MustNewAnchorScheduler(8, 4, src)
	cfg := DefaultClientConfig()
	cfg.HeightSync = sched
	cfg.HeightSyncPeerTips = peerTips
	client := NewHTTPClient("http://example.invalid", "escrow-1", testutil.MustGenerateKey(t), cfg)

	h, ok := client.ObservedHeightNow()
	require.True(t, ok)
	require.Equal(t, uint64(99), h)
}

func TestObservedHeightNow_IgnoresLogOracle(t *testing.T) {
	peerTips := NewHeightSyncPeerTips()
	src := heightsync.NewPeerTipOracleSource(peerTips, peerTips.Freshness)
	sched := heightsync.MustNewAnchorScheduler(8, 4, src)
	or := &heightSyncTestOracle{hdr: &blocks.Header{
		Height: 99, ChainID: "test-chain", BlockHash: []byte{0x01},
	}}
	cfg := DefaultClientConfig()
	cfg.HeightSync = sched
	cfg.HeightSyncPeerTips = peerTips
	cfg.HeightSyncLogOracle = or
	client := NewHTTPClient("http://example.invalid", "escrow-1", testutil.MustGenerateKey(t), cfg)

	hdr, err := or.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(99), hdr.Height)

	h, ok := client.ObservedHeightNow()
	require.False(t, ok)
	require.Equal(t, uint64(0), h)
}

func TestObservedHeightNow_NoHeightSync(t *testing.T) {
	client := NewHTTPClient("http://example.invalid", "escrow-1", testutil.MustGenerateKey(t))
	h, ok := client.ObservedHeightNow()
	require.False(t, ok)
	require.Equal(t, uint64(0), h)
}

func TestHTTPClient_SeedHeightSync_RecordsOrigin(t *testing.T) {
	hostSigner := testutil.MustGenerateKey(t)
	userSigner := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup([]*signing.Secp256k1Signer{hostSigner})
	config := testutil.DefaultConfig(1)
	verifier := signing.NewSecp256k1Verifier()

	sm, err := state.NewStateMachine("escrow-1", config, group, 100000, userSigner.Address(), verifier, testutil.MustMemoryStore(t, "escrow-1", userSigner.Address(), config, group, 100000))
	require.NoError(t, err)
	store := storage.NewMemory()
	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID: "escrow-1", Version: testutil.RuntimeTestVersion, Config: config, Group: group, InitialBalance: 100000,
	}))
	hst, err := host.NewHost(sm, hostSigner, stub.NewInferenceEngine(), "escrow-1", group, nil,
		host.WithGrace(100), host.WithStorage(store))
	require.NoError(t, err)

	or := &heightSyncTestOracle{hdr: &blocks.Header{
		Height: 55, ChainID: "chain-x", BlockHash: []byte{0xde, 0xad},
	}}
	sched := heightsync.MustNewAnchorSchedulerFromOracle(8, 4, or)
	srv, err := NewServer(hst, store, verifier, userSigner.Address(),
		WithHeightSync(sched, or), WithHeightSyncSeedRPC(true))
	require.NoError(t, err)
	e := echo.New()
	g := e.Group("/devshard/v2")
	registerServer(g, srv)
	ts := httptest.NewServer(e)
	t.Cleanup(ts.Close)

	peerTips := NewHeightSyncPeerTips()
	src := heightsync.NewPeerTipOracleSource(peerTips, peerTips.Freshness)
	clientSched := heightsync.MustNewAnchorScheduler(8, 4, src)
	cfg := DefaultClientConfig()
	cfg.RoutePrefix = testRoutePrefix
	cfg.HeightSync = clientSched
	cfg.HeightSyncPeerTips = peerTips
	client := NewHTTPClient(ts.URL, "escrow-1", userSigner, cfg)

	_, ok := client.ObservedHeightNow()
	require.False(t, ok)

	seeded, err := client.SeedHeightSync(context.Background())
	require.NoError(t, err)
	require.True(t, seeded)

	obsH, ok := client.ObservedHeightNow()
	require.True(t, ok)
	require.Equal(t, uint64(55), obsH)
}

func TestHTTPClient_SeedHeightSync_DoesNotRetry503(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "version v2 is not present in the governance routing catalog", http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)

	client := NewHTTPClient(server.URL, "escrow-1", signer, ClientConfig{
		QueryTimeout:       5 * time.Second,
		RoutePrefix:        "/",
		HeightSyncPeerTips: NewHeightSyncPeerTips(),
	})
	ok, err := client.SeedHeightSync(context.Background())
	require.Error(t, err)
	require.False(t, ok)
	require.Equal(t, int32(1), hits.Load(),
		"SeedHeightSync must not nest doPostRaw's 5s 429/503 retry")
}

func TestHTTPClient_SeedHeightSync_UsesHeightSeedTimeout(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	release := make(chan struct{})
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })

	client := NewHTTPClient(server.URL, "escrow-1", signer, ClientConfig{
		QueryTimeout:       30 * time.Second,
		HeightSeedTimeout:  50 * time.Millisecond,
		RoutePrefix:        "/",
		HeightSyncPeerTips: NewHeightSyncPeerTips(),
	})
	start := time.Now()
	ok, err := client.SeedHeightSync(context.Background())
	require.Error(t, err)
	require.False(t, ok)
	require.Less(t, time.Since(start), 5*time.Second,
		"a hung seed POST must not wait out QueryTimeout")
	select {
	case <-started:
	default:
		t.Fatal("seed POST never reached the handler")
	}
}

func TestClient_ResponseAnchor_VerifiesOriginSignature(t *testing.T) {
	hostSigner := testutil.MustGenerateKey(t)
	userSigner := testutil.MustGenerateKey(t)
	sec := &heightsync.HeightSyncSection{
		ProofType:             heightsync.AnchorProofType,
		MainnetHeight:         90,
		MainnetBlockHashHex:   "beef",
		Direction:             "response",
		OriginatorSenderID:    hostSigner.Address(),
		OriginatorTimestampMs: time.Now().UnixMilli(),
	}
	_, sig, err := heightsync.SignOrigin(hostSigner, sec)
	require.NoError(t, err)
	sec.SenderSignature = sig

	peerTips := NewHeightSyncPeerTips()
	cfg := DefaultClientConfig()
	cfg.HeightSyncPeerTips = peerTips
	client := NewHTTPClient("http://host", "escrow-1", userSigner, cfg)

	client.ingestResponseHeightSync(sec, 1, "test")
	_, _, ok := peerTips.OriginSignedBlobFor(hostSigner.Address(), 90)
	require.True(t, ok)
}

func TestClient_ResponseAnchor_DropsOnInvalidSig(t *testing.T) {
	hostSigner := testutil.MustGenerateKey(t)
	userSigner := testutil.MustGenerateKey(t)
	heightsync.RegisterAnchorMetrics(nil)
	before := heightsync.OriginSigInvalidTotal()

	sec := &heightsync.HeightSyncSection{
		ProofType:             heightsync.AnchorProofType,
		MainnetHeight:         91,
		MainnetBlockHashHex:   "beef",
		Direction:             "response",
		OriginatorSenderID:    hostSigner.Address(),
		OriginatorTimestampMs: time.Now().UnixMilli(),
	}
	sec.SenderSignature = []byte{0, 1, 2}

	peerTips := NewHeightSyncPeerTips()
	cfg := DefaultClientConfig()
	cfg.HeightSyncPeerTips = peerTips
	client := NewHTTPClient("http://host", "escrow-1", userSigner, cfg)

	client.ingestResponseHeightSync(sec, 1, "test")
	_, _, ok := peerTips.OriginSignedBlobFor(hostSigner.Address(), 91)
	require.False(t, ok)
	require.Equal(t, before+1, heightsync.OriginSigInvalidTotal())
}

func TestClient_ResponseAnchor_ZeroTimestampNotCached(t *testing.T) {
	hostSigner := testutil.MustGenerateKey(t)
	userSigner := testutil.MustGenerateKey(t)
	sec := &heightsync.HeightSyncSection{
		ProofType:           heightsync.AnchorProofType,
		MainnetHeight:       90,
		MainnetBlockHashHex: "beef",
		Direction:           "response",
		OriginatorSenderID:  hostSigner.Address(),
	}
	_, sig, err := heightsync.SignOrigin(hostSigner, sec)
	require.NoError(t, err)
	sec.SenderSignature = sig

	peerTips := NewHeightSyncPeerTips()
	cfg := DefaultClientConfig()
	cfg.HeightSyncPeerTips = peerTips
	client := NewHTTPClient("http://host", "escrow-1", userSigner, cfg)

	client.ingestResponseHeightSync(sec, 1, "test")
	_, _, ok := peerTips.OriginSignedBlobFor(hostSigner.Address(), 90)
	require.False(t, ok)
	require.Nil(t, peerTips.MaxFresh(time.Now(), time.Minute))
}

func TestClient_RequestLeg_OmitsSenderSignature(t *testing.T) {
	peerTips := NewHeightSyncPeerTips()
	now := time.Now().UnixMilli()
	peerTips.RecordOriginWithBlob(&heightsync.HeightSyncSection{
		ProofType:             heightsync.AnchorProofType,
		MainnetHeight:         100,
		MainnetBlockHashHex:   "aa",
		OriginatorSenderID:    "gonka1origin",
		OriginatorTimestampMs: now,
	}, []byte("blob"), []byte{1})

	outbound := &heightsync.HeightSyncSection{
		ProofType:           heightsync.AnchorProofType,
		MainnetHeight:       10,
		MainnetBlockHashHex: "local",
		SenderSignature:     []byte{99},
	}
	peerTips.Carry(outbound)
	require.Nil(t, outbound.SenderSignature)
}
