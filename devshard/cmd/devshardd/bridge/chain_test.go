package bridge_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"common/chain"
	shardbridge "devshard/bridge"
	"devshard/cmd/devshardd/bridge"
	"devshard/testenv/mockchain/grpcface"
	"devshard/testenv/mockchain/seed"
	"devshard/testenv/mockchain/store"
)

func newTestBridge(t *testing.T, submitter bridge.Submitter) *bridge.ChainBridge {
	t.Helper()
	return newTestBridgeWithStore(t, seed.Defaults(), submitter)
}

func newTestBridgeWithStore(t *testing.T, st *store.Store, submitter bridge.Submitter) *bridge.ChainBridge {
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
	return bridge.NewChainBridge(chain.NewFromConn(conn), submitter)
}

func TestChainBridge_GetEscrow_MapsSessionConfigFields(t *testing.T) {
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

	info, err := newTestBridgeWithStore(t, st, nil).GetEscrow("1")
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

func TestBridge_NotificationsNoop(t *testing.T) {
	b := newTestBridge(t, nil)
	assert.NoError(t, b.OnEscrowCreated(shardbridge.EscrowInfo{}))
	assert.NoError(t, b.OnSettlementProposed("1", nil, 0))
	assert.NoError(t, b.OnSettlementFinalized("1"))
}

func TestBridge_SubmitDisputeState_DelegatesToSubmitter(t *testing.T) {
	var called bool
	submitter := &stubSubmitter{fn: func(escrowID uint64, _ []byte, _ uint64, _ map[uint32][]byte) error {
		called = true
		assert.Equal(t, uint64(99), escrowID)
		return nil
	}}

	b := newTestBridge(t, submitter)
	require.NoError(t, b.SubmitDisputeState("99", nil, 0, nil))
	assert.True(t, called)
}

func TestBridge_SubmitDisputeState_NilSubmitterReturnsError(t *testing.T) {
	b := newTestBridge(t, nil)
	err := b.SubmitDisputeState("1", nil, 0, nil)
	assert.True(t, errors.Is(err, shardbridge.ErrNotImplemented))
}

type stubSubmitter struct {
	fn func(uint64, []byte, uint64, map[uint32][]byte) error
}

func (s *stubSubmitter) SubmitDisputeState(id uint64, root []byte, nonce uint64, sigs map[uint32][]byte) error {
	return s.fn(id, root, nonce, sigs)
}
