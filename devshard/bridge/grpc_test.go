package bridge_test

import (
	"testing"

	"common/chain"
	"devshard/bridge"
	"devshard/testenv/mockchain/grpcface"
	"devshard/testenv/mockchain/seed"
	"devshard/testenv/mockchain/store"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func startGRPCBridge(t *testing.T) *bridge.GRPCBridge {
	t.Helper()
	return startGRPCBridgeWithStore(t, seed.Defaults())
}

func startGRPCBridgeWithStore(t *testing.T, st *store.Store) *bridge.GRPCBridge {
	t.Helper()
	srv, lis, err := grpcface.NewInProcessServer(grpcface.Deps{Store: st})
	require.NoError(t, err)
	t.Cleanup(func() {
		srv.Stop()
		_ = lis.Close()
	})
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return bridge.NewGRPCBridge(chain.NewFromConn(conn))
}

func TestGRPCBridge_GetEscrow_HappyPath(t *testing.T) {
	b := startGRPCBridge(t)
	info, err := b.GetEscrow("1")
	require.NoError(t, err)
	assert.Equal(t, "1", info.EscrowID)
	assert.NotEmpty(t, info.CreatorAddress)
	assert.NotEmpty(t, info.Slots)
	assert.NotEmpty(t, info.AppHash)
}

func TestGRPCBridge_GetEscrow_MapsSessionConfigFields(t *testing.T) {
	st := seed.Defaults()
	escrow := st.GetEscrow(1)
	require.NotNil(t, escrow)
	escrow.TokenPrice = 7
	escrow.CreateDevshardFee = 12_345
	escrow.FeePerNonce = 19
	escrow.InferenceSealGraceNonces = 9
	escrow.InferenceSealGraceSeconds = 77
	escrow.AutoSealEveryNNonces = 21
	escrow.ValidationRate = 7_777
	escrow.VoteThresholdFactor = 67
	escrow.RefusalTimeout = 5
	escrow.ExecutionTimeout = 17
	st.PutEscrow(escrow)

	info, err := startGRPCBridgeWithStore(t, st).GetEscrow("1")
	require.NoError(t, err)
	require.Equal(t, uint64(7), info.TokenPrice)
	require.Equal(t, uint64(12_345), info.CreateDevshardFee)
	require.Equal(t, uint64(19), info.FeePerNonce)
	require.Equal(t, uint32(9), info.InferenceSealGraceNonces)
	require.Equal(t, uint32(77), info.InferenceSealGraceSeconds)
	require.Equal(t, uint32(21), info.AutoSealEveryNNonces)
	require.Equal(t, uint32(7_777), info.ValidationRate)
	require.Equal(t, uint32(67), info.VoteThresholdFactor)
	require.Equal(t, int64(5), info.RefusalTimeout)
	require.Equal(t, int64(17), info.ExecutionTimeout)
}

func TestGRPCBridge_GetEscrow_NotFound(t *testing.T) {
	b := startGRPCBridge(t)
	_, err := b.GetEscrow("999999")
	require.ErrorIs(t, err, bridge.ErrEscrowNotFound)
}

func TestGRPCBridge_GetHostInfo(t *testing.T) {
	b := startGRPCBridge(t)
	host := "gonka1host000000000000000000000000000000000"
	info, err := b.GetHostInfo(host)
	require.NoError(t, err)
	assert.Equal(t, host, info.Address)
	assert.Contains(t, info.URL, "versiond-router")
}

func TestGRPCBridge_GetValidationThreshold(t *testing.T) {
	b := startGRPCBridge(t)
	threshold, err := b.GetValidationThreshold(1, "test-model")
	require.NoError(t, err)
	require.NotNil(t, threshold)
	assert.Equal(t, int64(50), threshold.Value)
}

func TestGRPCBridge_GetValidationThreshold_MissingReturnsError(t *testing.T) {
	b := startGRPCBridge(t)
	_, err := b.GetValidationThreshold(1, "missing-model")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "validation threshold not found")
}

func TestGRPCBridge_VerifyWarmKey(t *testing.T) {
	b := startGRPCBridge(t)
	host := "gonka1host000000000000000000000000000000000"
	warm := "gonka1warm000000000000000000000000000000000"
	ok, err := b.VerifyWarmKey(warm, host)
	require.NoError(t, err)
	assert.True(t, ok)

	ok, err = b.VerifyWarmKey("gonka1unknown000000000000000000000000000", host)
	require.NoError(t, err)
	assert.False(t, ok)
}
