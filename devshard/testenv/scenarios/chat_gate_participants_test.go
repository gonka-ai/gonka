package scenarios

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/heightsync"
	"devshard/types"
	"devshard/user"
)

// The bootstrap chat gate opens on half the participants. A participant is one
// validator address. One block ack from any of that address's slots counts
// once, and the denominator is the number of distinct addresses. Two
// participants open on one ack. Three need two. Slot ids outside the group
// do not count. An ack that carries no block is excluded before this count;
// the session gate test covers that exclusion.
func TestChatGateOpensOnHalfTheParticipants(t *testing.T) {
	const (
		addrA = "gonka1participantA"
		addrB = "gonka1participantB"
		addrC = "gonka1participantC"
	)
	group := []types.SlotAssignment{
		{SlotID: 0, ValidatorAddress: addrA},
		{SlotID: 1, ValidatorAddress: addrA},
		{SlotID: 2, ValidatorAddress: addrA},
		{SlotID: 3, ValidatorAddress: addrB},
		{SlotID: 4, ValidatorAddress: addrC},
	}

	open, acked, participants := user.ChatGateParticipantQuorum(group, []uint32{0, 1, 2})
	require.False(t, open, "three slots of one address are one participant")
	require.Equal(t, 1, acked)
	require.Equal(t, 3, participants)

	open, acked, participants = user.ChatGateParticipantQuorum(group, []uint32{2, 3})
	require.True(t, open, "two of three participants open the gate")
	require.Equal(t, 2, acked)
	require.Equal(t, 3, participants)

	two := []types.SlotAssignment{
		{SlotID: 0, ValidatorAddress: addrA},
		{SlotID: 1, ValidatorAddress: addrA},
		{SlotID: 2, ValidatorAddress: addrB},
	}
	open, acked, participants = user.ChatGateParticipantQuorum(two, []uint32{1})
	require.True(t, open, "two participants open on one")
	require.Equal(t, 1, acked)
	require.Equal(t, 2, participants)

	open, acked, participants = user.ChatGateParticipantQuorum(group, []uint32{0})
	require.False(t, open, "one of three participants stays closed")
	require.Equal(t, 1, acked)

	open, acked, participants = user.ChatGateParticipantQuorum(group, []uint32{1, 99})
	require.False(t, open, "a slot outside the group does not count")
	require.Equal(t, 1, acked)
	require.Equal(t, 3, participants)

	open, acked, participants = user.ChatGateParticipantQuorum(group, nil)
	require.False(t, open)
	require.Equal(t, 0, acked)
	require.Equal(t, 3, participants)

	// An ack with no block never enters the slot list the gate counts.
	tracker := heightsync.NewTurnTracker(5, 2, heightsync.DefaultHeartbeatConfig())
	hash := []byte{0xaa}
	tracker.Observe(1, []*types.DevshardTx{{
		Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{
			ObservedHeight: 50, ObservedBlockHash: hash, SlotsNum: 5,
		}},
	}}, 50)
	tracker.Observe(2, []*types.DevshardTx{
		heightAckTx(1, 0, 0, nil),
		heightAckTx(1, 1, 50, nil),
		heightAckTx(1, 2, 0, hash),
	}, 50)
	require.Empty(t, tracker.BlockAckSlots())
	open, acked, participants = user.ChatGateParticipantQuorum(group, tracker.BlockAckSlots())
	require.False(t, open)
	require.Equal(t, 0, acked)

	tracker.Observe(3, []*types.DevshardTx{
		heightAckTx(1, 0, 50, hash),
		heightAckTx(1, 1, 50, hash),
		heightAckTx(1, 2, 50, hash),
	}, 50)
	require.Equal(t, []uint32{0, 1, 2}, tracker.BlockAckSlots())
	open, acked, participants = user.ChatGateParticipantQuorum(group, tracker.BlockAckSlots())
	require.False(t, open, "three block acks from one address are still one participant")
	require.Equal(t, 1, acked)
	require.Equal(t, 3, participants)

	tracker.Observe(4, []*types.DevshardTx{
		heightAckTx(1, 3, 50, hash),
	}, 50)
	open, acked, participants = user.ChatGateParticipantQuorum(group, tracker.BlockAckSlots())
	require.True(t, open, "the second participant's block ack opens three")
	require.Equal(t, 2, acked)
	require.Equal(t, 3, participants)
}

func heightAckTx(ref uint64, slot uint32, height uint64, hash []byte) *types.DevshardTx {
	return &types.DevshardTx{Tx: &types.DevshardTx_HeightAck{HeightAck: &types.MsgHeightAck{
		RefNonce: ref, SlotId: slot,
		ObservedHeight: height, ObservedBlockHash: hash,
	}}}
}
