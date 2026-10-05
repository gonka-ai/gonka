package user

import (
	"testing"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/types"
)

func finishTx(nonce uint64) *types.DevshardTx {
	return &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{
		FinishInference: &types.MsgFinishInference{ServedHash: testutil.TestServedHash, InferenceId: nonce},
	}}
}

// A raw mempool Finish is only an untrusted claim until a diff applies it.
func TestAGossipedFinishDoesNotMarkNonceFinished(t *testing.T) {
	session := &Session{
		nonceStates:   map[uint64]*nonceOutcome{7: {}, 11: {}},
		pendingTxKeys: map[string]struct{}{},
	}

	session.mu.Lock()
	session.processResponse(0, &host.HostResponse{Mempool: []*types.DevshardTx{finishTx(11)}}, 7)
	session.mu.Unlock()

	if session.IsNonceFinished(11) {
		t.Error("raw mempool Finish must not suppress a timeout")
	}
	if session.IsNonceFinished(7) {
		t.Error("nonce 7 was not finished by this mempool, so nothing may mark it")
	}
}
