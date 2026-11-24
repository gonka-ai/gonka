package registry

import (
	"context"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

// Test flow:
//  1. Build a four-slot session where `gonka1aaa` holds three slots and `gonka1bbb` one, with a vote threshold of two.
//  2. Build the nonce stream over it.
//  3. Assert only the host holding three slots is unvoteable, since a timeout of its own nonce leaves one voter, and that the host holding one slot is not.
func TestANonceStreamNamesTheHostsNoTimeoutVoteCanReach(t *testing.T) {
	t.Parallel()
	session := newFakeSession("gonka1aaa", "gonka1aaa", "gonka1aaa", "gonka1bbb")
	session.escrowState.Group = []types.SlotAssignment{
		{SlotID: 0, ValidatorAddress: "gonka1aaa"},
		{SlotID: 1, ValidatorAddress: "gonka1aaa"},
		{SlotID: 2, ValidatorAddress: "gonka1aaa"},
		{SlotID: 3, ValidatorAddress: "gonka1bbb"},
	}
	session.escrowState.Config.VoteThreshold = 2

	hosts := newNonceStream(session, "qwen", fixedClock()).UnvoteableHosts()

	if want := []string{"gonka1aaa"}; !reflect.DeepEqual(hosts, want) {
		t.Errorf("unvoteable hosts = %v, want %v", hosts, want)
	}
}

// Test flow:
//  1. Build a four-slot session where every host holds one slot, with a vote threshold of two.
//  2. Build the nonce stream over it.
//  3. Assert no host is unvoteable.
func TestANonceStreamOfEvenHostsHasNoUnvoteableHost(t *testing.T) {
	t.Parallel()
	session := newFakeSession("gonka1aaa", "gonka1bbb", "gonka1ccc", "gonka1ddd")
	session.escrowState.Group = []types.SlotAssignment{
		{SlotID: 0, ValidatorAddress: "gonka1aaa"},
		{SlotID: 1, ValidatorAddress: "gonka1bbb"},
		{SlotID: 2, ValidatorAddress: "gonka1ccc"},
		{SlotID: 3, ValidatorAddress: "gonka1ddd"},
	}
	session.escrowState.Config.VoteThreshold = 2

	if hosts := newNonceStream(session, "qwen", fixedClock()).UnvoteableHosts(); len(hosts) != 0 {
		t.Errorf("unvoteable hosts = %v, want none", hosts)
	}
}

func assignSlots(session *fakeSession, holders ...string) {
	for slot, holder := range holders {
		session.escrowState.Group = append(session.escrowState.Group, types.SlotAssignment{SlotID: uint32(slot), ValidatorAddress: holder})
	}
	session.escrowState.Config.VoteThreshold = 2
}

// Test flow:
//  1. Build a registry with two open escrows of one model, one where `gonka1aaa` holds three of four slots and one of even hosts, and a third escrow of another model with its own unvoteable host.
//  2. Ask the registry for the unvoteable hosts of each model and of an unknown model.
//  3. Assert each model names only the hosts of its own escrows and the unknown model names none.
func TestTheRegistryNamesTheUnvoteableHostsOfEachModel(t *testing.T) {
	t.Parallel()
	skewed := newFakeSession("gonka1aaa", "gonka1aaa", "gonka1aaa", "gonka1bbb")
	assignSlots(skewed, "gonka1aaa", "gonka1aaa", "gonka1aaa", "gonka1bbb")
	even := newFakeSession("gonka1aaa", "gonka1bbb", "gonka1ccc", "gonka1ddd")
	assignSlots(even, "gonka1aaa", "gonka1bbb", "gonka1ccc", "gonka1ddd")
	other := newFakeSession("gonka1zzz", "gonka1zzz", "gonka1zzz", "gonka1yyy")
	assignSlots(other, "gonka1zzz", "gonka1zzz", "gonka1zzz", "gonka1yyy")
	registry := New(Deps{
		ServingSessions: newSessions(map[string]*fakeSession{"1": skewed, "2": even, "3": other}).open, Membership: newRecordingMembership(), Now: fixedClock(),
	})
	require.NoError(t, registry.Add(context.Background(), "1", "qwen"))
	require.NoError(t, registry.Add(context.Background(), "2", "qwen"))
	require.NoError(t, registry.Add(context.Background(), "3", "llama"))

	require.Equal(t, map[string]bool{"gonka1aaa": true}, registry.UnvoteableHosts("qwen"))
	require.Equal(t, map[string]bool{"gonka1zzz": true}, registry.UnvoteableHosts("llama"))
	require.Empty(t, registry.UnvoteableHosts("mistral"))
}
