package user

import (
	"crypto/sha256"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/types"
)

const bindingNonce = 1

var (
	storedSum   = sha256.Sum256([]byte("stored"))
	servedSum   = sha256.Sum256([]byte("served"))
	tamperedSum = sha256.Sum256([]byte("tampered"))
)

func bindingStateMachine(t *testing.T) (*state.StateMachine, []*signing.Secp256k1Signer) {
	t.Helper()
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	userKey := testutil.MustGenerateKey(t)
	stateMachine := newTestStateMachine(t, "escrow-1", testutil.DefaultConfig(len(hosts)), testutil.MakeGroup(hosts), 10000, userKey.Address(), signing.NewSecp256k1Verifier())
	return stateMachine, hosts
}

func finishFrom(t *testing.T, signer *signing.Secp256k1Signer, servedHash []byte) *types.DevshardTx {
	t.Helper()
	msg := &types.MsgFinishInference{
		InferenceId: bindingNonce, ResponseHash: storedSum[:], ServedHash: servedHash,
		ExecutorSlot: 1, EscrowId: "escrow-1",
	}
	msg.ProposerSig = testutil.SignProposerTx(t, signer, msg)
	return &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: msg}}
}

// Test flow:
//  1. Build a host response from the case's mempool of Finishes and the hashes the gateway received.
//  2. Check the binding against local proposer-signature verification.
//  3. Assert the verdict: bound on either signed view, mismatch otherwise even when the Finish signed no served hash, a decoy Finish skipped, an unverified Finish reported, nothing received reported.
func TestCheckServedBinding(t *testing.T) {
	stateMachine, hosts := bindingStateMachine(t)
	outsider := testutil.MustGenerateKey(t)
	rejectUnverified := func(tx *types.DevshardTx) error {
		return stateMachine.RejectFinishProposerSigLocal(tx.GetFinishInference())
	}

	for _, testCase := range []struct {
		name     string
		mempool  []*types.DevshardTx
		received [][32]byte
		want     ServedBinding
	}{
		{name: "no finish yet", received: [][32]byte{servedSum}, want: ServedBindingNoFinish},
		{name: "the served view arrived", mempool: []*types.DevshardTx{finishFrom(t, hosts[1], servedSum[:])}, received: [][32]byte{servedSum}, want: ServedBindingBound},
		{name: "the stored view arrived", mempool: []*types.DevshardTx{finishFrom(t, hosts[1], servedSum[:])}, received: [][32]byte{tamperedSum, storedSum}, want: ServedBindingBound},
		{name: "another answer arrived", mempool: []*types.DevshardTx{finishFrom(t, hosts[1], servedSum[:])}, received: [][32]byte{tamperedSum}, want: ServedBindingMismatch},
		{name: "nothing arrived", mempool: []*types.DevshardTx{finishFrom(t, hosts[1], servedSum[:])}, want: ServedBindingNothingReceived},
		{name: "the finish binds no served view, the stored one arrived", mempool: []*types.DevshardTx{finishFrom(t, hosts[1], nil)}, received: [][32]byte{storedSum}, want: ServedBindingBound},
		{name: "the finish binds no served view, another answer arrived", mempool: []*types.DevshardTx{finishFrom(t, hosts[1], nil)}, received: [][32]byte{servedSum}, want: ServedBindingMismatch},
		{name: "a decoy finish ahead of the executor's is skipped", mempool: []*types.DevshardTx{finishFrom(t, outsider, tamperedSum[:]), finishFrom(t, hosts[1], servedSum[:])}, received: [][32]byte{tamperedSum}, want: ServedBindingMismatch},
		{name: "the finish is not the executor's", mempool: []*types.DevshardTx{finishFrom(t, outsider, tamperedSum[:])}, received: [][32]byte{tamperedSum}, want: ServedBindingUnverifiedFinish},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			response := &host.HostResponse{Mempool: testCase.mempool, ReceivedResponseHashes: testCase.received}
			require.Equal(t, testCase.want, checkServedBinding(response, bindingNonce, rejectUnverified))
		})
	}
}
