package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"common/chainoracle/blocks"
	"common/chainoracle/blocks/observer"
	"devshard/bridge"
	"devshard/chainoracle/blocks/headertime"
	"devshard/chainoracle/blocks/tipcache"
	"devshard/heightsync"
	"devshard/hostevents"
	"devshard/internal/testutil"
	"devshard/transport"
	"devshard/user"

	abci "github.com/cometbft/cometbft/abci/types"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	cmttypes "github.com/cometbft/cometbft/types"
)

func resetHeightSyncForTest() {
	if hsSt != nil && hsSt.closer != nil {
		hsSt.closer()
	}
	hsOnce = sync.Once{}
	hsSt = nil
	hsErr = nil
}

func unsetHeightSyncSources(t *testing.T) {
	t.Helper()
	t.Setenv("DEVSHARD_CHAIN_RPC", "")
	t.Setenv("NODE_RPC_URL", "")
	t.Setenv("DEVSHARD_COMET_RPC", "")
	t.Setenv("NODE_MANAGER_ADDR", "")
	t.Setenv(envGatewayChainOracle, "")
}

func TestExtraClientConfigFromEnv_EmptyIsNil(t *testing.T) {
	resetHeightSyncForTest()
	unsetHeightSyncSources(t)
	cfg, err := extraClientConfigFromEnv()
	require.NoError(t, err)
	require.Nil(t, cfg)
}

func TestExtraClientConfigFromEnv_InvalidK(t *testing.T) {
	resetHeightSyncForTest()
	unsetHeightSyncSources(t)
	t.Setenv("NODE_MANAGER_ADDR", "api:9400")
	t.Setenv("DEVSHARD_HEIGHTSYNC_K", "xyz")
	cfg, err := extraClientConfigFromEnv()
	require.Error(t, err)
	require.Nil(t, cfg)
}

func TestExtraClientConfigFromEnv_ChainGRPCEnablesCourier(t *testing.T) {
	resetHeightSyncForTest()
	t.Cleanup(resetHeightSyncForTest)
	unsetHeightSyncSources(t)
	t.Setenv("DEVSHARD_CHAIN_GRPC", "genesis-node:9090")
	cfg, err := extraClientConfigFromEnv()
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.NotNil(t, cfg.HeightSync)
	require.NotNil(t, cfg.HeightSyncPeerTips)
	require.Nil(t, cfg.HeightSyncLogOracle, "chain gRPC alone must not start the follower")
	require.Equal(t, "peer_tip_cache", cfg.HeightSync.SourceKind())
}

func TestExtraClientConfigFromEnv_ChainRPCEnablesWithoutOracleURL(t *testing.T) {
	resetHeightSyncForTest()
	t.Cleanup(resetHeightSyncForTest)
	unsetHeightSyncSources(t)
	t.Setenv("NODE_RPC_URL", "http://127.0.0.1:26657")
	cfg, err := extraClientConfigFromEnv()
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.NotNil(t, cfg.HeightSync)
	require.NotNil(t, cfg.HeightSyncPeerTips)
	require.Nil(t, cfg.HeightSyncLogOracle, "chain RPC alone must not start the follower")
	require.Equal(t, "peer_tip_cache", cfg.HeightSync.SourceKind())
}

func TestExtraClientConfigFromEnv_NodeManagerEnablesCourier(t *testing.T) {
	resetHeightSyncForTest()
	t.Cleanup(resetHeightSyncForTest)
	unsetHeightSyncSources(t)
	t.Setenv("NODE_MANAGER_ADDR", "api:9400")
	cfg, err := extraClientConfigFromEnv()
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.NotNil(t, cfg.HeightSync)
	require.Equal(t, "peer_tip_cache", cfg.HeightSync.SourceKind())
}

func TestRequireHeightSeedFromEnv(t *testing.T) {
	t.Setenv(envRequireHeightSeed, "")
	require.True(t, requireHeightSeedFromEnv())

	t.Setenv(envRequireHeightSeed, "true")
	require.True(t, requireHeightSeedFromEnv())

	for _, v := range []string{"0", "false", "off", "no", "FALSE", " Off "} {
		t.Setenv(envRequireHeightSeed, v)
		require.False(t, requireHeightSeedFromEnv(), v)
	}
}

func TestParseUintEnv(t *testing.T) {
	t.Setenv("DEVSHARD_HEIGHTSYNC_K", "")
	v, err := parseUintEnv("DEVSHARD_HEIGHTSYNC_K")
	require.NoError(t, err)
	require.Equal(t, uint64(0), v)

	t.Setenv("DEVSHARD_HEIGHTSYNC_K", "10")
	v, err = parseUintEnv("DEVSHARD_HEIGHTSYNC_K")
	require.NoError(t, err)
	require.Equal(t, uint64(10), v)
}

func TestHeightSyncHeartbeatFromEnv(t *testing.T) {
	t.Setenv(envHeightSyncHeartbeat, "")
	require.False(t, heightSyncHeartbeatFromEnv())
	require.True(t, !heightSyncHeartbeatFromEnv(), "unset is the bit gateway.go stores in DisableHeightSyncHeartbeat")

	for _, v := range []string{"true", "1", "on", "TRUE", " On "} {
		t.Setenv(envHeightSyncHeartbeat, v)
		require.True(t, heightSyncHeartbeatFromEnv(), v)
		require.False(t, !heightSyncHeartbeatFromEnv(), v)
	}
	for _, v := range []string{"false", "0", "off", "no", "yes", "maybe", "TRUEISH"} {
		t.Setenv(envHeightSyncHeartbeat, v)
		require.False(t, heightSyncHeartbeatFromEnv(), v)
	}
}

func TestQuietSettleDelayFromEnv(t *testing.T) {
	t.Setenv(envQuietSettleDelay, "")
	require.Equal(t, user.DefaultQuietSettleDelay, quietSettleDelayFromEnv())

	t.Setenv(envQuietSettleDelay, "5s")
	require.Equal(t, user.TestQuietSettleDelay, quietSettleDelayFromEnv())

	t.Setenv(envQuietSettleDelay, "2m")
	require.Equal(t, 2*time.Minute, quietSettleDelayFromEnv())

	t.Setenv(envQuietSettleDelay, "0s")
	require.Equal(t, user.DefaultQuietSettleDelay, quietSettleDelayFromEnv())

	t.Setenv(envQuietSettleDelay, "not-a-duration")
	require.Equal(t, user.DefaultQuietSettleDelay, quietSettleDelayFromEnv())
}

func TestGatewayChainOracle_FlagValues(t *testing.T) {
	t.Setenv(envGatewayChainOracle, "")
	require.False(t, gatewayChainOracleFromEnv())

	for _, v := range []string{"true", "1", "on", "TRUE", " On "} {
		t.Setenv(envGatewayChainOracle, v)
		require.True(t, gatewayChainOracleFromEnv(), v)
	}
	for _, v := range []string{"false", "0", "off", "no", "yes", "maybe", "TRUEISH"} {
		t.Setenv(envGatewayChainOracle, v)
		require.False(t, gatewayChainOracleFromEnv(), v)
	}
}

func TestGatewayHeaderTimes_OneSource(t *testing.T) {
	resetHeightSyncForTest()
	t.Cleanup(resetHeightSyncForTest)
	unsetHeightSyncSources(t)

	var commits atomic.Int64
	var lastHeight atomic.Int64
	lastHeight.Store(-1)
	rpc := observer.ServeCometJSONRPC(nil, func(h int64) {
		lastHeight.Store(h)
		commits.Add(1)
	})
	t.Cleanup(rpc.Close)
	t.Setenv("DEVSHARD_CHAIN_RPC", rpc.URL)

	require.NoError(t, initGatewayHeightSync(nil, rpc.URL))
	require.NotNil(t, hsSt)
	require.NotNil(t, hsSt.headerTimes)
	require.NotNil(t, hsSt.cometHeader)

	reader := gatewayHeaderTimes()
	require.True(t, reader == hsSt.headerTimes)
	require.True(t, gatewayHeaderTimes() == reader, "every gateway reader calls the same source")

	require.NotNil(t, hsSt.verifier, "a Comet RPC builds the commit verifier")

	before := commits.Load()
	at := time.Unix(1_700_000_040, 0).UTC()
	hash := []byte{0x28}
	hsSt.cometHeader(blocks.HashOnlyHeader(40, at, "gonka-test", hash))
	require.Eventually(t, func() bool { return commits.Load() > before }, 5*time.Second, 10*time.Millisecond,
		"the verifier must hit the stub RPC for the queued height")
	require.True(t, lastHeight.Load() == 0 || lastHeight.Load() == 40,
		"the queued header's commit is height 40, or the bootstrap commit for that verify")
	_, ok := hsSt.headerTimes.StoredAt(40)
	require.False(t, ok, "a socket header reaches the map only after its commit checks out")

	// A second construction is the same process state, not a second map.
	require.NoError(t, initGatewayHeightSync(nil, rpc.URL))
	require.True(t, gatewayHeaderTimes() == reader)
}

func TestGatewayChainOracle_DefaultOffDoesNotMint(t *testing.T) {
	resetHeightSyncForTest()
	t.Cleanup(resetHeightSyncForTest)
	unsetHeightSyncSources(t)
	t.Setenv("DEVSHARD_CHAIN_RPC", "http://127.0.0.1:26657")

	require.NoError(t, initGatewayHeightSync(nil, "http://127.0.0.1:26657"))
	require.NotNil(t, hsSt, "the follower always starts: timeouts read block times through it")
	require.NotNil(t, gatewayHeaderTimes())

	cfg, err := extraClientConfigFromEnv()
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.Equal(t, "peer_tip_cache", cfg.HeightSync.SourceKind())
	require.Nil(t, cfg.HeightSyncLogOracle, "the log oracle stays behind the flag")

	got, err, miss := cfg.HeightSync.Decide(context.Background(), heightsync.DecideHints{Nonce: 1})
	require.NoError(t, err)
	require.Nil(t, got, "empty peer tips must Omit, never mint a gateway Anchor")
	require.True(t, miss)

	client := transport.NewHTTPClient("http://127.0.0.1:9", "escrow-1", testutil.MustGenerateKey(t), *cfg)
	h, ok := client.ObservedHeightNow()
	require.False(t, ok)
	require.Equal(t, uint64(0), h)
}

func TestGatewayChainOracle_OnStillSeeds(t *testing.T) {
	resetHeightSyncForTest()
	t.Cleanup(resetHeightSyncForTest)
	unsetHeightSyncSources(t)
	t.Setenv("DEVSHARD_CHAIN_RPC", "http://127.0.0.1:26657")
	t.Setenv(envGatewayChainOracle, "true")

	cfg, err := extraClientConfigFromEnv()
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.Equal(t, "peer_tip_cache", cfg.HeightSync.SourceKind())
	require.NotNil(t, cfg.HeightSyncLogOracle, "flag on wires the follower as log oracle")
	require.NotNil(t, hsSt)
	require.NotNil(t, hsSt.oracle)

	local := &staticBlockOracle{hdr: &blocks.Header{
		Height: 99, ChainID: "test-chain", BlockHash: []byte{0x11, 0x22},
	}}
	hdr, err := local.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(99), hdr.Height)
	cfg.HeightSyncLogOracle = local

	got, err, miss := cfg.HeightSync.Decide(context.Background(), heightsync.DecideHints{Nonce: 1})
	require.NoError(t, err)
	require.Nil(t, got, "a warm local Latest must not feed Decide")
	require.True(t, miss)

	client := transport.NewHTTPClient("http://127.0.0.1:9", "escrow-1", testutil.MustGenerateKey(t), *cfg)
	h, ok := client.ObservedHeightNow()
	require.False(t, ok, "ObservedHeightNow stays peer-tip-only")
	require.Equal(t, uint64(0), h)
}

func TestGatewayChainOracle_PreservesOriginator(t *testing.T) {
	resetHeightSyncForTest()
	t.Cleanup(resetHeightSyncForTest)
	unsetHeightSyncSources(t)
	t.Setenv("NODE_RPC_URL", "http://127.0.0.1:26657")
	t.Setenv(envGatewayChainOracle, "on")

	cfg, err := extraClientConfigFromEnv()
	require.NoError(t, err)
	require.NotNil(t, cfg.HeightSyncLogOracle)

	const originator = "gonka1seeded-host"
	now := time.Now().UnixMilli()
	cfg.HeightSyncPeerTips.RecordOriginWithBlob(&heightsync.HeightSyncSection{
		ProofType:             heightsync.AnchorProofType,
		MainnetHeight:         55,
		MainnetBlockHashHex:   "aabb",
		OriginatorSenderID:    originator,
		OriginatorTimestampMs: now,
	}, []byte("host-blob"), []byte{0x01, 0x02})

	got, err, miss := cfg.HeightSync.Decide(context.Background(), heightsync.DecideHints{Nonce: 1})
	require.NoError(t, err)
	require.False(t, miss)
	require.NotNil(t, got)
	require.Equal(t, originator, got.OriginatorSenderID)
	require.Equal(t, now, got.OriginatorTimestampMs)
	require.NotEmpty(t, got.OriginatorSenderID)

	client := transport.NewHTTPClient("http://127.0.0.1:9", "escrow-1", testutil.MustGenerateKey(t), *cfg)
	blob, sig, ok := client.HeightSyncEvidenceFor(originator, 55)
	require.True(t, ok)
	require.Equal(t, []byte("host-blob"), blob)
	require.Equal(t, []byte{0x01, 0x02}, sig)
	_, _, gatewayID := client.HeightSyncEvidenceFor("", 55)
	require.False(t, gatewayID, "no attestation is stored under an empty/gateway identity")
}

type staticBlockOracle struct {
	hdr *blocks.Header
}

func (o *staticBlockOracle) Latest(context.Context) (*blocks.Header, error) {
	if o == nil || o.hdr == nil {
		return nil, nil
	}
	h := *o.hdr
	h.BlockHash = append([]byte(nil), o.hdr.BlockHash...)
	return &h, nil
}

func (o *staticBlockOracle) At(context.Context, int64) (*blocks.Header, error) { return nil, nil }

func (o *staticBlockOracle) Prove(context.Context, string, int64) (*blocks.Proof, error) {
	return nil, blocks.ErrProveNotImplemented
}

func (o *staticBlockOracle) Subscribe(context.Context, int64) (<-chan *blocks.Header, error) {
	ch := make(chan *blocks.Header)
	close(ch)
	return ch, nil
}

func TestPublishVerifiedKeepsTheDAPIOrigin(t *testing.T) {
	src := headertime.New(headertime.WithGateway())
	publish := publishVerified(src)
	at := time.Unix(1_700_000_000, 0).UTC()
	publish(blocks.HashOnlyHeader(10, at, "gonka-test", []byte{10}), true)
	publish(blocks.HashOnlyHeader(11, at.Add(time.Second), "gonka-test", []byte{11}), false)

	rec, ok := src.StoredAt(10)
	require.True(t, ok)
	require.Equal(t, headertime.OriginDAPI, rec.Origin)
	require.True(t, rec.Verified)
	rec, ok = src.StoredAt(11)
	require.True(t, ok)
	require.Equal(t, headertime.OriginComet, rec.Origin)
	require.True(t, rec.Verified)
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func captureSlog(t *testing.T) *lockedBuffer {
	t.Helper()
	buf := &lockedBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

func TestGatewayWithoutRPCTrustsDAPI(t *testing.T) {
	resetHeightSyncForTest()
	t.Cleanup(resetHeightSyncForTest)
	unsetHeightSyncSources(t)
	t.Setenv("NODE_MANAGER_ADDR", "127.0.0.1:9")
	logs := captureSlog(t)

	_, err := loadHeightSyncProcessState(nil, "", "")
	require.NoError(t, err)
	require.NotNil(t, hsSt)
	require.Nil(t, hsSt.verifier)
	require.NotContains(t, logs.String(), "dapi blocks are not written")
	require.Contains(t, logs.String(), "trusted source of warm-key events and block times")
	require.True(t, gatewayBlockVerifier(nil) == nil)
}

func TestGatewayWithoutDAPIDoesNotFillTheMap(t *testing.T) {
	src := headertime.New(headertime.WithGateway())
	require.False(t, seedGatewayMap(context.Background(), nil, nil, src))
	require.Equal(t, uint64(0), src.HighWater())
}

func TestSeedGatewayTrustedDAPIStoresLatest(t *testing.T) {
	at := time.Unix(1_700_000_000, 0).UTC()
	oracle := &staticBlockOracle{hdr: blocks.HashOnlyHeader(1000, at, "gonka-test", []byte{1, 2, 3, 4})}
	src := headertime.New(headertime.WithGateway())
	require.False(t, seedGatewayMap(context.Background(), nil, oracle, src), "dapi seed is not a comet seed")

	rec, ok := src.StoredAt(1000)
	require.True(t, ok)
	require.Equal(t, headertime.OriginDAPI, rec.Origin)
	require.True(t, rec.Verified)
	ms, err := src.HeaderTimeMs(1000, []byte{1, 2, 3, 4})
	require.NoError(t, err)
	require.Equal(t, at.UnixMilli(), ms)
	require.Equal(t, uint64(1000), src.HighWater())
}

func TestGatewayDebugHeightSync_HeaderTimeBlockFeedLive(t *testing.T) {
	resetHeightSyncForTest()
	t.Cleanup(resetHeightSyncForTest)
	src := headertime.New()
	p := hostevents.NewPoller(hostevents.PollerConfig{Source: src, BlocksVerified: true})
	hsSt = &heightSyncProcessState{headerTimes: src, poller: p}

	g := &Gateway{}
	req := httptest.NewRequest(http.MethodGet, "/v1/debug/heightsync", nil)
	rec := httptest.NewRecorder()
	g.handleDebugHeightSync(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		HeaderTime hostevents.HeaderTimeView `json:"header_time"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.False(t, body.HeaderTime.BlockFeedLive)
	require.Equal(t, uint64(0), body.HeaderTime.HighWater)
}

func TestGatewayCometHooks_WarmQueriesOnTheHeightSocket(t *testing.T) {
	warm := bridge.NewWarmKeyLogCopy(nil, time.Hour)
	p := hostevents.NewPoller(hostevents.PollerConfig{Warm: warm})
	var connected atomic.Bool
	hooks := gatewayCometHooks(func(up bool) { connected.Store(up) }, nil, p)
	require.Equal(t, hostevents.WarmKeyQueries(), hooks.ExtraQueries)
	require.Equal(t, append([]string{"tm.event='NewBlock'"}, hostevents.WarmKeyQueries()...), tipcache.SubscribeQueries(hooks))

	hooks.OnConnected()
	require.True(t, connected.Load())
	hooks.OnEvent(ctypes.ResultEvent{Data: cmttypes.EventDataTx{TxResult: abci.TxResult{
		Height: 20,
		Result: abci.ExecTxResult{Events: []abci.Event{{
			Type: "warm_key_granted",
			Attributes: []abci.EventAttribute{
				{Key: "granter", Value: "cold"},
				{Key: "grantee", Value: "warm"},
				{Key: "grant_id", Value: "4"},
				{Key: "height", Value: "20"},
			},
		}}},
	}}})
	e, found, err := warm.WarmKeyEntry("cold", 4)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "warm", e.Grantee)
	hooks.OnDisconnected()
	require.False(t, connected.Load())
}
