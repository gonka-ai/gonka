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
	conn, err := grpc.NewClient("localhost:9090", grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	client := chain.NewFromConn(conn)

	return bridge.NewChainBridge(client, submitter)
}

// startBridgesWithStore serves st over an in-process mockchain and returns the
// host bridge (ChainBridge) and the gateway bridge (GRPCBridge) on the same conn.
func startBridgesWithStore(t *testing.T, st *store.Store) (*bridge.ChainBridge, *shardbridge.GRPCBridge) {
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
	client := chain.NewFromConn(conn)
	return bridge.NewChainBridge(client, nil), shardbridge.NewGRPCBridge(client)
}

// Host and gateway build SessionConfig from the same escrow row through the
// same mapper, so a field that only one bridge copies desynchronises the
// auto-seal clock gate and timeout votes between them (issue #1762).
func TestChainBridge_GetEscrow_MatchesGatewayBridge(t *testing.T) {
	st := seed.Defaults()
	escrow := st.GetEscrow(1)
	require.NotNil(t, escrow)
	escrow.RefusalTimeout = 90
	escrow.ExecutionTimeout = 1300
	st.PutEscrow(escrow)

	host, gateway := startBridgesWithStore(t, st)
	hostInfo, err := host.GetEscrow("1")
	require.NoError(t, err)
	gatewayInfo, err := gateway.GetEscrow("1")
	require.NoError(t, err)

	require.Equal(t, int64(90), hostInfo.RefusalTimeout)
	require.Equal(t, int64(1300), hostInfo.ExecutionTimeout)
	require.Equal(t, gatewayInfo, hostInfo, "host and gateway bridges must map the escrow row identically")

	groupSize := len(hostInfo.Slots)
	require.Equal(t,
		shardbridge.SessionConfigAtBind(groupSize, gatewayInfo),
		shardbridge.SessionConfigAtBind(groupSize, hostInfo))
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
