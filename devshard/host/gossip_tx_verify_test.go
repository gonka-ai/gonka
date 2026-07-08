package host

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/stub"
	"devshard/types"
)

// signMsg signs the canonical proposer preimage for a message using signer.
// Mirrors host.signProposer: marshal the message (with ProposerSig already zero
// or unset) and Sign(data).
func signMsg(t *testing.T, signer *signing.Secp256k1Signer, msg proto.Message) []byte {
	t.Helper()
	data, err := proto.Marshal(msg)
	require.NoError(t, err)
	sig, err := signer.Sign(data)
	require.NoError(t, err)
	return sig
}

// newVerifyTestHost builds a Host with a writable state-machine the test can
// observe. Reuses the same shape as newTestHost in host_test.go but renamed so
// this file can be read in isolation.
func newVerifyTestHost(t *testing.T, ownIdx int) (*Host, []*signing.Secp256k1Signer) {
	t.Helper()
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	store := testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 100000)
	sm, err := state.NewStateMachine("escrow-1", config, group, 100000, user.Address(), verifier, store)
	require.NoError(t, err)
	engine := stub.NewInferenceEngine()
	h, err := NewHost(sm, hosts[ownIdx], engine, "escrow-1", group, nil, WithGrace(10))
	require.NoError(t, err)
	return h, hosts
}

// VerifyGossipedTx must reject a MsgValidationVote whose ProposerSig does not
// recover to the claimed VoterSlot's address. Forged bytes are exactly the
// payload a byzantine group member can serve via POST /sessions/:id/gossip/txs.
func TestVerifyGossipedTx_RejectsForgedValidationVote(t *testing.T) {
	t.Parallel()
	h, _ := newVerifyTestHost(t, 0)

	const victimSlot = uint32(0)
	forged := &types.DevshardTx{Tx: &types.DevshardTx_ValidationVote{
		ValidationVote: &types.MsgValidationVote{
			InferenceId: 42,
			VoterSlot:   victimSlot,
			VoteValid:   true,
			ProposerSig: []byte("GARBAGE"),
			EscrowId:    "escrow-1",
		},
	}}

	err := h.VerifyGossipedTx(forged)
	require.Error(t, err, "forged ProposerSig must be rejected before mempool ingestion")
}

// VerifyGossipedTx must accept a MsgValidationVote whose ProposerSig recovers
// to the actual owner of the claimed VoterSlot. Without this assertion the fix
// could be silently over-rejecting and breaking honest validation — exactly
// the gap a reject-only test pair would hide.
func TestVerifyGossipedTx_AcceptsValidValidationVote(t *testing.T) {
	t.Parallel()
	h, hosts := newVerifyTestHost(t, 0)

	const voterSlot = uint32(1)
	msg := &types.MsgValidationVote{
		InferenceId: 42,
		VoterSlot:   voterSlot,
		VoteValid:   true,
		EscrowId:    "escrow-1",
	}
	msg.ProposerSig = signMsg(t, hosts[voterSlot], msg)

	tx := &types.DevshardTx{Tx: &types.DevshardTx_ValidationVote{ValidationVote: msg}}
	require.NoError(t, h.VerifyGossipedTx(tx),
		"properly-signed gossiped vote must pass content verification")
}

func TestVerifyGossipedTx_RejectsForgedValidation(t *testing.T) {
	t.Parallel()
	h, _ := newVerifyTestHost(t, 0)

	forged := &types.DevshardTx{Tx: &types.DevshardTx_Validation{
		Validation: &types.MsgValidation{
			InferenceId:   42,
			ValidatorSlot: 1,
			Valid:         true,
			ProposerSig:   []byte("GARBAGE"),
			EscrowId:      "escrow-1",
		},
	}}
	require.Error(t, h.VerifyGossipedTx(forged),
		"forged ProposerSig on a MsgValidation must be rejected before mempool ingestion")
}

func TestVerifyGossipedTx_AcceptsValidValidation(t *testing.T) {
	t.Parallel()
	h, hosts := newVerifyTestHost(t, 0)

	const validatorSlot = uint32(1)
	msg := &types.MsgValidation{
		InferenceId:   42,
		ValidatorSlot: validatorSlot,
		Valid:         true,
		EscrowId:      "escrow-1",
	}
	msg.ProposerSig = signMsg(t, hosts[validatorSlot], msg)

	tx := &types.DevshardTx{Tx: &types.DevshardTx_Validation{Validation: msg}}
	require.NoError(t, h.VerifyGossipedTx(tx),
		"properly-signed gossiped validation must pass content verification")
}

// Slots outside the group must be rejected immediately — neither a forged nor
// a legitimately-signed tx for an unknown slot has a place in the mempool.
func TestVerifyGossipedTx_RejectsUnknownSlot(t *testing.T) {
	t.Parallel()
	h, _ := newVerifyTestHost(t, 0)

	forged := &types.DevshardTx{Tx: &types.DevshardTx_ValidationVote{
		ValidationVote: &types.MsgValidationVote{
			InferenceId: 42,
			VoterSlot:   999, // out of group
			VoteValid:   true,
			ProposerSig: []byte("GARBAGE"),
			EscrowId:    "escrow-1",
		},
	}}
	require.ErrorIs(t, h.VerifyGossipedTx(forged), types.ErrSlotNotInGroup)
}

// Tx kinds outside the verification scope (e.g. ConfirmStart, FinishInference,
// StartInference) carry different signature shapes and pass through this
// content check unchanged.
func TestVerifyGossipedTx_PassesThroughUnverifiedKinds(t *testing.T) {
	t.Parallel()
	h, _ := newVerifyTestHost(t, 0)

	other := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{
		ConfirmStart: &types.MsgConfirmStart{InferenceId: 42},
	}}
	require.NoError(t, h.VerifyGossipedTx(other))
}

// Documents the suppression oracle's pre-condition: hasMempoolValidationOrVote
// trusts that mempool entries have already been content-verified. If a forged
// tx ever bypasses the ingestion gate, the oracle silently suppresses honest
// validation for that inference. This is the in-process anchor for the fix at
// transport ingestion — it stays here as a regression guard so any future
// caller adding txs to the mempool without going through VerifyGossipedTx will
// be on notice.
func TestSuppressionOracle_NaiveOnRawMempoolInjection(t *testing.T) {
	t.Parallel()
	h, _ := newVerifyTestHost(t, 0)
	const infID = uint64(42)
	const ownSlot = uint32(0)
	h.slotIDs = map[uint32]bool{ownSlot: true}

	require.False(t, h.hasMempoolValidationOrVote(infID))

	forged := &types.DevshardTx{Tx: &types.DevshardTx_ValidationVote{
		ValidationVote: &types.MsgValidationVote{
			InferenceId: infID,
			VoterSlot:   ownSlot,
			VoteValid:   true,
			ProposerSig: []byte("GARBAGE"),
			EscrowId:    "escrow-1",
		},
	}}
	h.mempool.AddTx(forged)

	require.True(t, h.hasMempoolValidationOrVote(infID),
		"oracle trusts mempool content — verification must happen at gossip ingestion")
}

// A oneof wrapper with a NIL inner message must be rejected, not panic. (Defensive:
// proto.Unmarshal can't produce this over gossip, but a programmatic caller could.)
func TestVerifyGossipedTx_RejectsNilInner(t *testing.T) {
	t.Parallel()
	h, _ := newVerifyTestHost(t, 0)
	require.Error(t, h.VerifyGossipedTx(&types.DevshardTx{Tx: &types.DevshardTx_Validation{Validation: nil}}))
	require.Error(t, h.VerifyGossipedTx(&types.DevshardTx{Tx: &types.DevshardTx_ValidationVote{ValidationVote: nil}}))
}
