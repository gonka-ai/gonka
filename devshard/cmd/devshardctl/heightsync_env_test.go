package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"common/chainoracle/blocks"
	"github.com/stretchr/testify/require"

	"devshard/heightsync"
	"devshard/internal/testutil"
	"devshard/transport"
)

func resetHeightSyncForTest() {
	if heightSyncState != nil && heightSyncState.closer != nil {
		heightSyncState.closer()
	}
	heightSyncOnce = sync.Once{}
	heightSyncState = nil
	errHeightSyncInit = nil
}

func unsetHeightSyncSources(t *testing.T) {
	t.Helper()
	t.Setenv("DEVSHARD_CHAINORACLE_URL", "")
	t.Setenv("DEVSHARD_CHAIN_RPC", "")
	t.Setenv("NODE_RPC_URL", "")
	t.Setenv("DEVSHARD_COMET_RPC", "")
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
	t.Setenv("DEVSHARD_CHAINORACLE_URL", "http://127.0.0.1:9")
	t.Setenv("DEVSHARD_HEIGHTSYNC_K", "xyz")
	cfg, err := extraClientConfigFromEnv()
	require.Error(t, err)
	require.Nil(t, cfg)
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

func TestGatewayChainOracle_DefaultOffDoesNotMint(t *testing.T) {
	resetHeightSyncForTest()
	t.Cleanup(resetHeightSyncForTest)
	unsetHeightSyncSources(t)
	t.Setenv("DEVSHARD_CHAIN_RPC", "http://127.0.0.1:26657")

	require.NoError(t, initGatewayHeightSync(nil, "http://127.0.0.1:26657"))
	require.Nil(t, heightSyncState, "follower must not start when the flag is unset")

	cfg, err := extraClientConfigFromEnv()
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.Equal(t, "peer_tip_cache", cfg.HeightSync.SourceKind())
	require.Nil(t, cfg.HeightSyncLogOracle)
	require.Nil(t, heightSyncState)

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
	require.NotNil(t, heightSyncState)
	require.NotNil(t, heightSyncState.oracle)

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
