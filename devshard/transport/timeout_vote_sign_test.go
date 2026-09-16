package transport

import (
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

func TestSignTimeoutVote_UsesDeterministicMarshal(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	verifier := signing.NewSecp256k1Verifier()

	sig, slot, err := signTimeoutVote("escrow-1", 7, types.TimeoutReason_TIMEOUT_REASON_REFUSED, signer, 3)
	require.NoError(t, err)
	require.Equal(t, uint32(3), slot)

	content := &types.TimeoutVoteContent{
		EscrowId:    "escrow-1",
		InferenceId: 7,
		Reason:      types.TimeoutReason_TIMEOUT_REASON_REFUSED,
		Accept:      true,
	}
	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(content)
	require.NoError(t, err)
	recovered, err := verifier.RecoverAddress(data, sig)
	require.NoError(t, err)
	require.Equal(t, signer.Address(), recovered)

	plain, err := proto.Marshal(content)
	require.NoError(t, err)
	require.Equal(t, data, plain, "all-scalar REFUSED content must be encoding-stable")
}

func TestSignErrorMissVote_BindsResponseHash(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	verifier := signing.NewSecp256k1Verifier()
	hash := []byte{0xab, 0xcd}

	sig, _, err := signErrorMissVote("escrow-1", 9, signer, 1, hash)
	require.NoError(t, err)

	bound := &types.ErrorMissVoteContent{
		EscrowId:     "escrow-1",
		InferenceId:  9,
		Accept:       true,
		ResponseHash: hash,
	}
	boundBytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(bound)
	require.NoError(t, err)
	recovered, err := verifier.RecoverAddress(boundBytes, sig)
	require.NoError(t, err)
	require.Equal(t, signer.Address(), recovered)

	unbound := &types.ErrorMissVoteContent{
		EscrowId:    "escrow-1",
		InferenceId: 9,
		Accept:      true,
	}
	unboundBytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(unbound)
	require.NoError(t, err)
	unboundAddr, err := verifier.RecoverAddress(unboundBytes, sig)
	require.NoError(t, err)
	require.NotEqual(t, signer.Address(), unboundAddr, "vote signed over a hash must not verify against empty response_hash")
}

func TestSignTimeoutVote_RefusedMatchesPreChangeBytes(t *testing.T) {
	content := &types.TimeoutVoteContent{
		EscrowId:    "escrow-1",
		InferenceId: 1,
		Reason:      types.TimeoutReason_TIMEOUT_REASON_REFUSED,
		Accept:      true,
	}
	got, err := proto.MarshalOptions{Deterministic: true}.Marshal(content)
	require.NoError(t, err)
	want, err := hex.DecodeString("0a08657363726f772d31100118012001")
	require.NoError(t, err)
	require.Equal(t, want, got)
}
