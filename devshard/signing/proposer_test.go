package signing

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard/types"
)

func TestCanonicalProposerBytes_SeparatesValidationAndVote(t *testing.T) {
	val := &types.MsgValidation{
		InferenceId: 1, ValidatorSlot: 2, Valid: false, EscrowId: "escrow-1",
	}
	vote := &types.MsgValidationVote{
		InferenceId: 1, VoterSlot: 2, VoteValid: false, EscrowId: "escrow-1",
	}
	a, err := CanonicalProposerBytes(val)
	require.NoError(t, err)
	b, err := CanonicalProposerBytes(vote)
	require.NoError(t, err)
	require.False(t, bytes.Equal(a, b), "identical fields must not share a preimage")
	require.True(t, bytes.HasPrefix(a, []byte(DomainValidation+"\x00")))
	require.True(t, bytes.HasPrefix(b, []byte(DomainValidationVote+"\x00")))
}

func TestCanonicalProposerBytes_IgnoresExistingSig(t *testing.T) {
	msg := &types.MsgValidation{InferenceId: 1, ValidatorSlot: 2, EscrowId: "e"}
	bare, err := CanonicalProposerBytes(msg)
	require.NoError(t, err)
	msg.ProposerSig = []byte{1, 2, 3}
	withSig, err := CanonicalProposerBytes(msg)
	require.NoError(t, err)
	require.Equal(t, bare, withSig)
}

func TestCanonicalProposerBytes_RejectsUnknown(t *testing.T) {
	_, err := CanonicalProposerBytes(&types.MsgHeartbeat{})
	require.Error(t, err)
	_, err = CanonicalProposerBytes(nil)
	require.Error(t, err)
}

func TestCanonicalProposerBytes_AllProposerTypesHaveDistinctDomains(t *testing.T) {
	msgs := []proto.Message{
		&types.MsgFinishInference{InferenceId: 1, ExecutorSlot: 2, EscrowId: "e"},
		&types.MsgValidation{InferenceId: 1, ValidatorSlot: 2, EscrowId: "e"},
		&types.MsgValidationVote{InferenceId: 1, VoterSlot: 2, EscrowId: "e"},
		&types.MsgRevealSeed{SlotId: 2, EscrowId: "e"},
	}
	domains := []string{DomainFinishInference, DomainValidation, DomainValidationVote, DomainRevealSeed}
	seen := make(map[string]struct{}, len(msgs))
	for i, msg := range msgs {
		b, err := CanonicalProposerBytes(msg)
		require.NoError(t, err)
		require.True(t, bytes.HasPrefix(b, []byte(domains[i]+"\x00")))
		key := string(b)
		_, dup := seen[key]
		require.False(t, dup, "preimage collision for %T", msg)
		seen[key] = struct{}{}
	}
}
