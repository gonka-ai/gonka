package session

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"common/chainoracle/blocks"
	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/stub"
)

func unsetHeightSyncSources(t *testing.T) {
	t.Helper()
	t.Setenv("DEVSHARD_CHAINORACLE_URL", "")
	t.Setenv("DEVSHARD_CHAIN_RPC", "")
	t.Setenv("NODE_RPC_URL", "")
	t.Setenv("DEVSHARD_COMET_RPC", "")
}

func TestSetHeightSyncFromEnv_EmptyIsNoop(t *testing.T) {
	unsetHeightSyncSources(t)
	mgr := NewHostManager(newManagerTestStore(t), mustGenerateKey(t), stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, &mockBridge{}, nil, nil)
	require.NoError(t, mgr.SetHeightSyncFromEnv(context.Background(), nil))
	require.Nil(t, mgr.heightSync)
	require.Nil(t, mgr.chainOracle)
	require.Len(t, mgr.transportServerOpts(), 3)
}

func TestSetHeightSyncFromEnv_InvalidK(t *testing.T) {
	unsetHeightSyncSources(t)
	t.Setenv("DEVSHARD_CHAINORACLE_URL", "http://127.0.0.1:9")
	t.Setenv("DEVSHARD_HEIGHTSYNC_K", "nope")
	mgr := NewHostManager(newManagerTestStore(t), mustGenerateKey(t), stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, &mockBridge{}, nil, nil)
	err := mgr.SetHeightSyncFromEnv(context.Background(), nil)
	require.Error(t, err)
	require.Nil(t, mgr.heightSync)
}

func TestSetHeightSyncFromEnv_WiresScheduler(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(ts.Close)

	unsetHeightSyncSources(t)
	t.Setenv("DEVSHARD_CHAINORACLE_URL", ts.URL)
	t.Setenv("DEVSHARD_HEIGHTSYNC_K", "10")
	t.Setenv("DEVSHARD_HEIGHTSYNC_SLOTS", "1")

	mgr := NewHostManager(newManagerTestStore(t), mustGenerateKey(t), stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, &mockBridge{}, nil, nil)
	require.NoError(t, mgr.SetHeightSyncFromEnv(context.Background(), nil))
	require.NotNil(t, mgr.heightSync)
	require.NotNil(t, mgr.chainOracle)
	require.Equal(t, uint64(10), mgr.heightSync.K())
	require.Equal(t, uint64(1), mgr.heightSync.SlotsNum())
	require.Len(t, mgr.transportServerOpts(), 4)
	mgr.ObserveChainHeader(&blocks.Header{
		Height:    7,
		Time:      time.Unix(1_700_000_000, 0).UTC(),
		ChainID:   "gonka-test",
		BlockHash: []byte{1, 2, 3, 4},
	})
	hdr, err := mgr.chainOracle.Latest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(7), hdr.Height)
	mgr.CloseHeightSync()
	require.Nil(t, mgr.heightSync)
}

func TestSetHeightSyncFromEnv_ChainRPCEnablesWithoutOracleURL(t *testing.T) {
	unsetHeightSyncSources(t)
	t.Setenv("NODE_RPC_URL", "http://127.0.0.1:26657")
	mgr := NewHostManager(newManagerTestStore(t), mustGenerateKey(t), stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, &mockBridge{}, nil, nil)
	require.NoError(t, mgr.SetHeightSyncFromEnv(context.Background(), nil))
	require.NotNil(t, mgr.heightSync)
	require.NotNil(t, mgr.chainOracle)
}
