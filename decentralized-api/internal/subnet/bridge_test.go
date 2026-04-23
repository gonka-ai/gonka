package subnet

import (
	"testing"

	"subnet/bridge"

	"github.com/stretchr/testify/assert"
)

func TestChainBridgeStubs(t *testing.T) {
	cb := NewChainBridge(nil)

	assert.ErrorIs(t, cb.OnEscrowCreated(bridge.EscrowInfo{}), bridge.ErrNotImplemented)
	assert.ErrorIs(t, cb.OnSettlementProposed("1", nil, 0), bridge.ErrNotImplemented)
	assert.ErrorIs(t, cb.OnSettlementFinalized("1"), bridge.ErrNotImplemented)
	assert.ErrorIs(t, cb.SubmitDisputeState("1", nil, 0, nil), bridge.ErrNotImplemented)
}

func TestChainBridgeImplementsInterface(t *testing.T) {
	var _ bridge.MainnetBridge = (*ChainBridge)(nil)
}
