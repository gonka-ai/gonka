package quorum

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

const (
	groupSize       = 16
	threshold       = uint32(8)
	executorAddress = "gonka1executor"
	otherAddress    = "gonka1other"
	executorNonce   = uint64(0)
)

func groupWithExecutorSlots(executorSlots int) []types.SlotAssignment {
	group := make([]types.SlotAssignment, groupSize)
	for slot := range group {
		address := otherAddress + fmt.Sprint(slot)
		if slot < executorSlots {
			address = executorAddress
		}
		group[slot] = types.SlotAssignment{SlotID: uint32(slot), ValidatorAddress: address}
	}
	return group
}

// Test flow:
//  1. Build a 16-slot group with threshold 8 where the executor's address holds 8 slots, then 7.
//  2. Ask whether a vote on a nonce that executor runs can pass.
//  3. Assert 8 slots leave exactly the threshold in voters, so the vote cannot pass, and 7 leave one more than it, so it can.
func TestAVoteIsUnreachableOnlyWhenTheExecutorHoldsHalfTheSlots(t *testing.T) {
	require.False(t, Voteable(groupWithExecutorSlots(8), threshold, executorNonce))
	require.True(t, Voteable(groupWithExecutorSlots(7), threshold, executorNonce))
}

// Test flow:
//  1. Build a group where one address holds 8 slots and take a nonce another address executes.
//  2. Ask whether a vote on it can pass, then on a nonce the unvoteable host executes in the same group.
//  3. Assert the other executor's nonce is voteable and the unvoteable host's is not: the result follows the executor of the nonce.
func TestTheVerdictFollowsTheExecutorOfTheNonce(t *testing.T) {
	group := groupWithExecutorSlots(8)

	require.True(t, Voteable(group, threshold, groupSize-1))
	require.False(t, Voteable(group, threshold, groupSize))
}

// Test flow:
//  1. Build a group whose first 8 slots share one validator address under different slot ids.
//  2. Ask whether a vote on a nonce the third of those slots executes can pass.
//  3. Assert it cannot: slots are counted by address, the key the voter dedup uses.
func TestSlotsAreCountedByValidatorAddress(t *testing.T) {
	require.False(t, Voteable(groupWithExecutorSlots(8), threshold, 2))
}

// Test flow:
//  1. Ask about a group with no slots.
//  2. Assert the vote is judged voteable, since nothing can be proven about it.
func TestAnEmptyGroupIsNeverCalledUnvoteable(t *testing.T) {
	require.True(t, Voteable(nil, threshold, executorNonce))
}
