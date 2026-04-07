package bridge_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"common/chain"
	"devshard/bridge"
)

func newTestBridge(t *testing.T, submitter bridge.Submitter) *bridge.GRPCBridge {
	t.Helper()
	// grpc.NewClient is non-blocking — safe to use with an unreachable address in unit tests.
	conn, err := grpc.NewClient("localhost:9090", grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	client, err := chain.NewFromConn(conn)
	require.NoError(t, err)

	return bridge.NewGRPCBridge(client, submitter)
}

func TestBridge_NotificationsDispatchToHandlers(t *testing.T) {
	b := newTestBridge(t, nil)

	var gotEscrow bridge.EscrowInfo
	b.OnEscrowCreatedHandler(func(e bridge.EscrowInfo) error {
		gotEscrow = e
		return nil
	})

	var gotProposedID string
	b.OnSettlementProposedHandler(func(escrowID string, _ []byte, _ uint64) error {
		gotProposedID = escrowID
		return nil
	})

	var gotFinalizedID string
	b.OnSettlementFinalizedHandler(func(escrowID string) error {
		gotFinalizedID = escrowID
		return nil
	})

	require.NoError(t, b.OnEscrowCreated(bridge.EscrowInfo{EscrowID: "42"}))
	require.NoError(t, b.OnSettlementProposed("42", []byte{1}, 1))
	require.NoError(t, b.OnSettlementFinalized("42"))

	assert.Equal(t, "42", gotEscrow.EscrowID)
	assert.Equal(t, "42", gotProposedID)
	assert.Equal(t, "42", gotFinalizedID)
}

func TestBridge_NotificationsNoopWithoutHandlers(t *testing.T) {
	b := newTestBridge(t, nil)
	assert.NoError(t, b.OnEscrowCreated(bridge.EscrowInfo{}))
	assert.NoError(t, b.OnSettlementProposed("1", nil, 0))
	assert.NoError(t, b.OnSettlementFinalized("1"))
}

func TestBridge_SubmitDisputeState_DelegatesToSubmitter(t *testing.T) {
	var called bool
	submitter := &stubSubmitter{fn: func(escrowID string, _ []byte, _ uint64, _ map[uint32][]byte) error {
		called = true
		assert.Equal(t, "99", escrowID)
		return nil
	}}

	b := newTestBridge(t, submitter)
	require.NoError(t, b.SubmitDisputeState("99", nil, 0, nil))
	assert.True(t, called)
}

func TestBridge_SubmitDisputeState_NilSubmitterReturnsError(t *testing.T) {
	b := newTestBridge(t, nil)
	err := b.SubmitDisputeState("1", nil, 0, nil)
	assert.True(t, errors.Is(err, bridge.ErrNotImplemented))
}

type stubSubmitter struct {
	fn func(string, []byte, uint64, map[uint32][]byte) error
}

func (s *stubSubmitter) SubmitDisputeState(id string, root []byte, nonce uint64, sigs map[uint32][]byte) error {
	return s.fn(id, root, nonce, sigs)
}
