package user

import (
	"testing"

	"devshard/host"
	"devshard/types"
)

func finishTx(nonce uint64) *types.DevshardTx {
	return &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{
		FinishInference: &types.MsgFinishInference{InferenceId: nonce},
	}}
}

// A gossiped finish closes its record on chain, so leaving it unfinished here raises a rejected timeout.
func TestAGossipedFinishMarksItsOwnNonceFinished(t *testing.T) {
	session := &Session{
		nonceStates:   map[uint64]*nonceOutcome{7: {}, 11: {}},
		pendingTxKeys: map[string]struct{}{},
	}

	session.mu.Lock()
	session.processResponse(0, &host.HostResponse{Mempool: []*types.DevshardTx{finishTx(11)}}, 7)
	session.mu.Unlock()

	if !session.IsNonceFinished(11) {
		t.Error("nonce 11 finished on chain but not here: its timeout round would be raised against a closed record")
	}
	if session.IsNonceFinished(7) {
		t.Error("nonce 7 was not finished by this mempool, so nothing may mark it")
	}
}
