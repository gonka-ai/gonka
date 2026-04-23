package testutil

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"subnet"
	"subnet/signing"
	"subnet/types"
)

var deterministicMarshal = proto.MarshalOptions{Deterministic: true}

var TestPrompt = []byte(`{"model":"llama","messages":[{"role":"user","content":"prompt"}]}`)
var TestPromptHash = mustCanonicalPromptHash(TestPrompt)

func mustCanonicalPromptHash(prompt []byte) [32]byte {
	h, err := subnet.CanonicalPromptHash(prompt)
	if err != nil {
		panic(err)
	}
	var arr [32]byte
	copy(arr[:], h)
	return arr
}

func MustGenerateKey(t *testing.T) *signing.Secp256k1Signer {
	t.Helper()
	s, err := signing.GenerateKey()
	require.NoError(t, err)
	return s
}

// MustSignerFromHex creates a signer from a fixed hex private key.
// Use for reproducible tests where the derived seed must be deterministic.
func MustSignerFromHex(t *testing.T, hexKey string) *signing.Secp256k1Signer {
	t.Helper()
	s, err := signing.SignerFromHex(hexKey)
	require.NoError(t, err)
	return s
}

func MakeGroup(signers []*signing.Secp256k1Signer) []types.SlotAssignment {
	group := make([]types.SlotAssignment, len(signers))
	for i, s := range signers {
		group[i] = types.SlotAssignment{
			SlotID:           uint32(i),
			ValidatorAddress: s.Address(),
		}
	}
	return group
}

// DefaultConfig returns a SessionConfig with VoteThreshold = numHosts/2
// and ValidationRate = 5000 (50%).
func DefaultConfig(numHosts int) types.SessionConfig {
	return types.SessionConfig{
		RefusalTimeout:   60,
		ExecutionTimeout: 1200,
		TokenPrice:       1,
		VoteThreshold:    uint32(numHosts) / 2,
		ValidationRate:   5000,
	}
}

func SignDiff(t *testing.T, signer signing.Signer, escrowID string, nonce uint64, txs []*types.SubnetTx) types.Diff {
	t.Helper()
	return SignDiffWithRoot(t, signer, escrowID, nonce, txs, nil)
}

func SignDiffWithRoot(t *testing.T, signer signing.Signer, escrowID string, nonce uint64, txs []*types.SubnetTx, postStateRoot []byte) types.Diff {
	t.Helper()
	content := &types.DiffContent{Nonce: nonce, Txs: txs, EscrowId: escrowID, PostStateRoot: postStateRoot}
	data, err := deterministicMarshal.Marshal(content)
	require.NoError(t, err)
	sig, err := signer.Sign(data)
	require.NoError(t, err)
	return types.Diff{Nonce: nonce, Txs: txs, UserSig: sig, PostStateRoot: postStateRoot}
}

func SignProposerTx(t *testing.T, signer signing.Signer, msg proto.Message) []byte {
	t.Helper()
	data, err := deterministicMarshal.Marshal(msg)
	require.NoError(t, err)
	sig, err := signer.Sign(data)
	require.NoError(t, err)
	return sig
}

func SignExecutorReceipt(t *testing.T, signer signing.Signer, escrowID string, inferenceID uint64, promptHash []byte, model string, inputLength, maxTokens uint64, startedAt, confirmedAt int64) []byte {
	t.Helper()
	content := &types.ExecutorReceiptContent{
		InferenceId: inferenceID,
		PromptHash:  promptHash,
		Model:       model,
		InputLength: inputLength,
		MaxTokens:   maxTokens,
		StartedAt:   startedAt,
		EscrowId:    escrowID,
		ConfirmedAt: confirmedAt,
	}
	data, err := deterministicMarshal.Marshal(content)
	require.NoError(t, err)
	sig, err := signer.Sign(data)
	require.NoError(t, err)
	return sig
}

func SignTimeoutVote(t *testing.T, signer signing.Signer, escrowID string, inferenceID uint64, reason types.TimeoutReason, accept bool) *types.TimeoutVote {
	t.Helper()
	content := &types.TimeoutVoteContent{
		EscrowId:    escrowID,
		InferenceId: inferenceID,
		Reason:      reason,
		Accept:      accept,
	}
	data, err := deterministicMarshal.Marshal(content)
	require.NoError(t, err)
	sig, err := signer.Sign(data)
	require.NoError(t, err)
	return &types.TimeoutVote{
		Accept:    accept,
		Signature: sig,
	}
}

// MakeMultiSlotGroup creates a group where some signers own multiple slots.
// slotsPerSigner[i] is the number of slots assigned to signers[i].
// SlotIDs are assigned sequentially starting from 0.
func MakeMultiSlotGroup(signers []*signing.Secp256k1Signer, slotsPerSigner []int) []types.SlotAssignment {
	var group []types.SlotAssignment
	slotID := uint32(0)
	for i, s := range signers {
		n := 1
		if i < len(slotsPerSigner) {
			n = slotsPerSigner[i]
		}
		for j := 0; j < n; j++ {
			group = append(group, types.SlotAssignment{
				SlotID:           slotID,
				ValidatorAddress: s.Address(),
			})
			slotID++
		}
	}
	return group
}

// SignRevealSeed creates a signed MsgRevealSeed. The seed signature is produced
// by signing the escrowID bytes with the signer's key.
func SignRevealSeed(t *testing.T, signer *signing.Secp256k1Signer, escrowID string, slotID uint32) *types.MsgRevealSeed {
	t.Helper()
	seedSig, err := signer.Sign([]byte(escrowID))
	require.NoError(t, err)
	msg := &types.MsgRevealSeed{
		SlotId:    slotID,
		Signature: seedSig,
		EscrowId:  escrowID,
	}
	data, err := deterministicMarshal.Marshal(msg)
	require.NoError(t, err)
	proposerSig, err := signer.Sign(data)
	require.NoError(t, err)
	msg.ProposerSig = proposerSig
	return msg
}

func StartTx(inferenceID uint64) *types.SubnetTx {
	return &types.SubnetTx{Tx: &types.SubnetTx_StartInference{StartInference: &types.MsgStartInference{
		InferenceId: inferenceID,
		PromptHash:  TestPromptHash[:],
		Model:       "llama",
		InputLength: 100,
		MaxTokens:   50,
		StartedAt:   1000,
	}}}
}
