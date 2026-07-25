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
)

func newTestBridge(t *testing.T, submitter bridge.Submitter) *bridge.ChainBridge {
	t.Helper()
	conn, err := grpc.NewClient("localhost:9090", grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	client := chain.NewFromConn(conn)

	return bridge.NewChainBridge(client, submitter)
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
