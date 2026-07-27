package keeper_test

import (
	"testing"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func setupSchedule(t testing.TB, currentEpoch uint64) (keeper.Keeper, types.MsgServer, sdk.Context, sdk.AccAddress) {
	k, ms, ctx := setupMsgServer(t)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, currentEpoch))
	addr, err := sdk.AccAddressFromBech32(testutil.Creator)
	require.NoError(t, err)
	// Register the participant so ParticipantPermission passes in CheckPermission.
	k.Participants.Set(ctx, addr, types.Participant{Index: testutil.Creator, Address: testutil.Creator, Status: types.ParticipantStatus_ACTIVE})
	return k, ms, ctx, addr
}

func TestSetClaimRecipients_HappyPath(t *testing.T) {
	k, ms, ctx, creatorAddr := setupSchedule(t, 100)
	recipient := testutil.Executor

	_, err := ms.SetClaimRecipients(ctx, &types.MsgSetClaimRecipients{
		Creator: testutil.Creator,
		Entries: []types.ClaimRecipientEntry{
			{Epoch: 101, Recipient: recipient},
			{Epoch: 105, Recipient: recipient},
			{Epoch: 140, Recipient: recipient},
		},
	})
	require.NoError(t, err)

	for _, epoch := range []uint64{101, 105, 140} {
		got, found, err := k.GetClaimRecipientForEpoch(ctx, creatorAddr, epoch)
		require.NoError(t, err)
		require.True(t, found, "epoch %d should be scheduled", epoch)
		require.Equal(t, recipient, got)
		hasIndex, err := k.ClaimRecipientsByEpoch.Has(ctx, collections.Join(epoch, creatorAddr))
		require.NoError(t, err)
		require.True(t, hasIndex, "epoch %d should be indexed", epoch)
	}
}

func TestSetClaimRecipients_RejectsCurrentOrPastEpoch(t *testing.T) {
	k, ms, ctx, creatorAddr := setupSchedule(t, 100)

	// current epoch
	_, err := ms.SetClaimRecipients(ctx, &types.MsgSetClaimRecipients{
		Creator: testutil.Creator,
		Entries: []types.ClaimRecipientEntry{{Epoch: 100, Recipient: testutil.Executor}},
	})
	require.Error(t, err)

	// past epoch, mixed with future — full batch must roll back
	_, err = ms.SetClaimRecipients(ctx, &types.MsgSetClaimRecipients{
		Creator: testutil.Creator,
		Entries: []types.ClaimRecipientEntry{
			{Epoch: 101, Recipient: testutil.Executor},
			{Epoch: 50, Recipient: testutil.Executor2},
		},
	})
	require.Error(t, err)

	_, found, err := k.GetClaimRecipientForEpoch(ctx, creatorAddr, 101)
	require.NoError(t, err)
	require.False(t, found, "future entry in rejected batch must not persist")
}

func TestSetClaimRecipients_RejectsTooFarAhead(t *testing.T) {
	_, ms, ctx, _ := setupSchedule(t, 100)

	_, err := ms.SetClaimRecipients(ctx, &types.MsgSetClaimRecipients{
		Creator: testutil.Creator,
		Entries: []types.ClaimRecipientEntry{
			{Epoch: 100 + keeper.MaxClaimRecipientLookahead + 1, Recipient: testutil.Executor},
		},
	})
	require.Error(t, err)

	// Exactly at the cap is allowed.
	_, err = ms.SetClaimRecipients(ctx, &types.MsgSetClaimRecipients{
		Creator: testutil.Creator,
		Entries: []types.ClaimRecipientEntry{
			{Epoch: 100 + keeper.MaxClaimRecipientLookahead, Recipient: testutil.Executor},
		},
	})
	require.NoError(t, err)
}

func TestSetClaimRecipients_RejectsInvalidRecipient(t *testing.T) {
	k, ms, ctx, creatorAddr := setupSchedule(t, 100)

	_, err := ms.SetClaimRecipients(ctx, &types.MsgSetClaimRecipients{
		Creator: testutil.Creator,
		Entries: []types.ClaimRecipientEntry{
			{Epoch: 101, Recipient: testutil.Executor},
			{Epoch: 102, Recipient: "not-a-bech32-address"},
		},
	})
	require.Error(t, err)

	_, found, err := k.GetClaimRecipientForEpoch(ctx, creatorAddr, 101)
	require.NoError(t, err)
	require.False(t, found, "valid entry in rejected batch must not persist")
}

func TestSetClaimRecipients_RejectsInvalidCreator(t *testing.T) {
	_, ms, ctx, _ := setupSchedule(t, 100)

	_, err := ms.SetClaimRecipients(ctx, &types.MsgSetClaimRecipients{
		Creator: "not-a-bech32-address",
		Entries: []types.ClaimRecipientEntry{{Epoch: 101, Recipient: testutil.Executor}},
	})
	require.Error(t, err)
}

func TestSetClaimRecipients_RejectsEmptyEntries(t *testing.T) {
	_, ms, ctx, _ := setupSchedule(t, 100)

	_, err := ms.SetClaimRecipients(ctx, &types.MsgSetClaimRecipients{
		Creator: testutil.Creator,
		Entries: nil,
	})
	require.Error(t, err)
}

func TestSetClaimRecipients_EmptyRecipientDeletesEntry(t *testing.T) {
	k, ms, ctx, creatorAddr := setupSchedule(t, 100)

	// Seed an entry.
	_, err := ms.SetClaimRecipients(ctx, &types.MsgSetClaimRecipients{
		Creator: testutil.Creator,
		Entries: []types.ClaimRecipientEntry{{Epoch: 101, Recipient: testutil.Executor}},
	})
	require.NoError(t, err)
	_, found, err := k.GetClaimRecipientForEpoch(ctx, creatorAddr, 101)
	require.NoError(t, err)
	require.True(t, found)

	// Now clear it with an empty recipient.
	_, err = ms.SetClaimRecipients(ctx, &types.MsgSetClaimRecipients{
		Creator: testutil.Creator,
		Entries: []types.ClaimRecipientEntry{{Epoch: 101, Recipient: ""}},
	})
	require.NoError(t, err)

	_, found, err = k.GetClaimRecipientForEpoch(ctx, creatorAddr, 101)
	require.NoError(t, err)
	require.False(t, found)
	hasIndex, err := k.ClaimRecipientsByEpoch.Has(ctx, collections.Join(uint64(101), creatorAddr))
	require.NoError(t, err)
	require.False(t, hasIndex)
}

func TestSetClaimRecipients_NoEffectiveEpoch(t *testing.T) {
	_, ms, ctx := setupMsgServer(t)
	// Intentionally skip SetEffectiveEpochIndex.

	_, err := ms.SetClaimRecipients(ctx, &types.MsgSetClaimRecipients{
		Creator: testutil.Creator,
		Entries: []types.ClaimRecipientEntry{{Epoch: 1, Recipient: testutil.Executor}},
	})
	require.Error(t, err)
}

func TestGetClaimRecipientForEpoch(t *testing.T) {
	k, _, ctx := setupMsgServer(t)
	addr, err := sdk.AccAddressFromBech32(testutil.Creator)
	require.NoError(t, err)

	_, found, err := k.GetClaimRecipientForEpoch(ctx, addr, 42)
	require.NoError(t, err)
	require.False(t, found, "empty map returns not found")

	require.NoError(t, k.SetClaimRecipientForEpoch(ctx, addr, 42, testutil.Executor))
	got, found, err := k.GetClaimRecipientForEpoch(ctx, addr, 42)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, testutil.Executor, got)
}

func TestGetClaimRecipientsByParticipant(t *testing.T) {
	k, _, ctx := setupMsgServer(t)
	addr, err := sdk.AccAddressFromBech32(testutil.Creator)
	require.NoError(t, err)

	require.NoError(t, k.SetClaimRecipientForEpoch(ctx, addr, 10, testutil.Executor))
	require.NoError(t, k.SetClaimRecipientForEpoch(ctx, addr, 20, testutil.Executor2))
	// Entry for a different participant — must not leak into result.
	otherAddr, err := sdk.AccAddressFromBech32(testutil.Executor)
	require.NoError(t, err)
	require.NoError(t, k.SetClaimRecipientForEpoch(ctx, otherAddr, 15, testutil.Creator))

	entries, err := k.GetClaimRecipientsByParticipant(ctx, addr)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	require.Equal(t, uint64(10), entries[0].Epoch)
	require.Equal(t, testutil.Executor, entries[0].Recipient)
	require.Equal(t, uint64(20), entries[1].Epoch)
	require.Equal(t, testutil.Executor2, entries[1].Recipient)
}

func TestClaimRecipientPruningRemovesPrimaryAndIndex(t *testing.T) {
	k, _, ctx, creatorAddr := setupSchedule(t, 100)
	require.NoError(t, k.PruningState.Set(ctx, types.PruningState{}))

	require.NoError(t, k.SetClaimRecipientForEpoch(ctx, creatorAddr, 95, testutil.Executor))
	require.NoError(t, k.SetClaimRecipientForEpoch(ctx, creatorAddr, 96, testutil.Executor2))

	require.NoError(t, k.Prune(ctx, 100))

	_, found, err := k.GetClaimRecipientForEpoch(ctx, creatorAddr, 95)
	require.NoError(t, err)
	require.False(t, found)
	hasIndex, err := k.ClaimRecipientsByEpoch.Has(ctx, collections.Join(uint64(95), creatorAddr))
	require.NoError(t, err)
	require.False(t, hasIndex)

	got, found, err := k.GetClaimRecipientForEpoch(ctx, creatorAddr, 96)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, testutil.Executor2, got)
	hasIndex, err = k.ClaimRecipientsByEpoch.Has(ctx, collections.Join(uint64(96), creatorAddr))
	require.NoError(t, err)
	require.True(t, hasIndex)
}
