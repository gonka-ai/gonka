package keeper_test

import (
	"context"
	"testing"

	"cosmossdk.io/collections"
	"cosmossdk.io/log"
	"cosmossdk.io/store"
	"cosmossdk.io/store/metrics"
	storetypes "cosmossdk.io/store/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/baseapp"
	addresscodec "github.com/cosmos/cosmos-sdk/codec/address"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	"github.com/cosmos/cosmos-sdk/x/group"
	groupkeeper "github.com/cosmos/cosmos-sdk/x/group/keeper"
	groupmodule "github.com/cosmos/cosmos-sdk/x/group/module"
	grouptestutil "github.com/cosmos/cosmos-sdk/x/group/testutil"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// realGroup runs the real (forked) x/group keeper on its own store; the ctx the
// inference keeper passes is replaced by the group ctx.
type realGroup struct {
	k   groupkeeper.Keeper
	ctx sdk.Context
}

func (g realGroup) CreateGroup(_ context.Context, m *group.MsgCreateGroup) (*group.MsgCreateGroupResponse, error) {
	return g.k.CreateGroup(g.ctx, m)
}
func (g realGroup) CreateGroupWithPolicy(_ context.Context, m *group.MsgCreateGroupWithPolicy) (*group.MsgCreateGroupWithPolicyResponse, error) {
	return g.k.CreateGroupWithPolicy(g.ctx, m)
}
func (g realGroup) UpdateGroupMembers(_ context.Context, m *group.MsgUpdateGroupMembers) (*group.MsgUpdateGroupMembersResponse, error) {
	return g.k.UpdateGroupMembers(g.ctx, m)
}
func (g realGroup) UpdateGroupMetadata(_ context.Context, m *group.MsgUpdateGroupMetadata) (*group.MsgUpdateGroupMetadataResponse, error) {
	return g.k.UpdateGroupMetadata(g.ctx, m)
}
func (g realGroup) SubmitProposal(_ context.Context, m *group.MsgSubmitProposal) (*group.MsgSubmitProposalResponse, error) {
	return g.k.SubmitProposal(g.ctx, m)
}
func (g realGroup) Vote(_ context.Context, m *group.MsgVote) (*group.MsgVoteResponse, error) {
	return g.k.Vote(g.ctx, m)
}
func (g realGroup) GroupInfo(_ context.Context, r *group.QueryGroupInfoRequest) (*group.QueryGroupInfoResponse, error) {
	return g.k.GroupInfo(g.ctx, r)
}
func (g realGroup) GroupMembers(_ context.Context, r *group.QueryGroupMembersRequest) (*group.QueryGroupMembersResponse, error) {
	return g.k.GroupMembers(g.ctx, r)
}
func (g realGroup) ProposalsByGroupPolicy(_ context.Context, r *group.QueryProposalsByGroupPolicyRequest) (*group.QueryProposalsByGroupPolicyResponse, error) {
	return g.k.ProposalsByGroupPolicy(g.ctx, r)
}

func newRealGroup(t *testing.T, ctrl *gomock.Controller) realGroup {
	key := storetypes.NewKVStoreKey(group.StoreKey)
	db := dbm.NewMemDB()
	ms := store.NewCommitMultiStore(db, log.NewNopLogger(), metrics.NewNoOpMetrics())
	ms.MountStoreWithDB(key, storetypes.StoreTypeIAVL, db)
	require.NoError(t, ms.LoadLatestVersion())
	ctx := sdk.NewContext(ms, cmtproto.Header{}, false, log.NewNopLogger())

	ak := grouptestutil.NewMockAccountKeeper(ctrl)
	ak.EXPECT().AddressCodec().Return(addresscodec.NewBech32Codec("gonka")).AnyTimes()
	ak.EXPECT().GetAccount(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	ak.EXPECT().NewAccount(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, a sdk.AccountI) sdk.AccountI { return a }).AnyTimes()
	ak.EXPECT().SetAccount(gomock.Any(), gomock.Any()).AnyTimes()

	enc := moduletestutil.MakeTestEncodingConfig(groupmodule.AppModuleBasic{})
	k := groupkeeper.NewKeeper(key, enc.Codec, baseapp.NewMsgServiceRouter(), ak, group.DefaultConfig())
	return realGroup{k: k, ctx: ctx}
}

// A host that left its epoch group via MsgLeaveGroup must still become INACTIVE
// on a failed confirmation PoC (removal from x/group must not fail the transition).
func TestLeaveGroupDoesNotBlockStatusTransition(t *testing.T) {
	ctrl := gomock.NewController(t)
	bank := keepertest.NewMockBookkeepingBankKeeper(ctrl)
	bankView := keepertest.NewMockBankKeeper(ctrl)
	acc := keepertest.NewMockAccountKeeper(ctrl)
	vs := keepertest.NewMockValidatorSet(ctrl)
	staking := keepertest.NewMockStakingKeeper(ctrl)
	coll := keepertest.NewMockCollateralKeeper(ctrl)
	sv := keepertest.NewMockStreamVestingKeeper(ctrl)
	authz := keepertest.NewMockAuthzKeeper(ctrl)
	upg := keepertest.NewMockUpgradeKeeper(ctrl)
	rg := newRealGroup(t, ctrl)
	k, ctx := keepertest.InferenceKeeperWithMock(t, bank, acc, vs, rg, staking, coll, sv, bankView, authz, upg)
	k.PrecomputeSPRTValues(ctx)

	coll.EXPECT().Slash(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(sdk.Coin{}, nil).AnyTimes()

	epochIndex := uint64(1)
	k.SetEpoch(ctx, &types.Epoch{Index: epochIndex, PocStartBlockHeight: 100})
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, epochIndex))
	eg, err := k.CreateEpochGroup(ctx, epochIndex, epochIndex)
	require.NoError(t, err)
	require.NoError(t, eg.CreateGroup(ctx))

	params := types.DefaultParams()
	params.ConfirmationPocParams.AlphaThreshold = types.DecimalFromFloat(0.7)
	require.NoError(t, k.SetParams(ctx, params))

	honest := testutil.Bech32Addr(1)
	cheater := testutil.Bech32Addr(2)
	_, err = rg.k.UpdateGroupMembers(rg.ctx, &group.MsgUpdateGroupMembers{
		Admin:   k.GetAuthority(),
		GroupId: eg.GroupData.EpochGroupId,
		MemberUpdates: []group.MemberRequest{
			{Address: honest, Weight: "100"},
			{Address: cheater, Weight: "100"},
			{Address: testutil.Bech32Addr(3), Weight: "100"},
		},
	})
	require.NoError(t, err)

	for _, a := range []string{honest, cheater} {
		require.NoError(t, k.SetParticipant(ctx, types.Participant{
			Index: a, Address: a, Status: types.ParticipantStatus_ACTIVE,
			CurrentEpochStats: types.NewCurrentEpochStats(),
		}))
	}

	// The cheater sends MsgLeaveGroup for the current epoch group (signer = itself).
	_, err = rg.k.LeaveGroup(rg.ctx, &group.MsgLeaveGroup{Address: cheater, GroupId: eg.GroupData.EpochGroupId})
	require.NoError(t, err)

	_, live, err := k.GetRootGroupDataWithLiveMembers(ctx)
	require.NoError(t, err)
	require.True(t, live[honest])
	require.False(t, live[cheater], "cheater no longer in the live set -> MsgCreatePoCChallenge target refused")

	// Both fail confirmation PoC (ratio 0.3 < alpha 0.7), as evaluateConfirmation records it.
	failCPoC := func(a string) error {
		p, found := k.GetParticipant(ctx, a)
		require.True(t, found)
		p.CurrentEpochStats.ConfirmationPoCRatio = types.DecimalFromFloat(0.3)
		return k.SetParticipant(ctx, p)
	}

	require.NoError(t, failCPoC(honest))
	h, _ := k.GetParticipant(ctx, honest)
	require.Equal(t, types.ParticipantStatus_INACTIVE, h.Status)
	require.True(t, eg.IsChanged(ctx))
	require.NoError(t, eg.MarkUnchanged(ctx))

	require.NoError(t, failCPoC(cheater))
	c, _ := k.GetParticipant(ctx, cheater)
	require.Equal(t, types.ParticipantStatus_INACTIVE, c.Status, "host that left its epoch group is still deactivated")
	require.True(t, eg.IsChanged(ctx), "validator set is recomputed for the host that left")
	ex, err := k.ExcludedParticipantsMap.Has(ctx, collections.Join(epochIndex, sdk.MustAccAddressFromBech32(cheater)))
	require.NoError(t, err)
	require.True(t, ex)
}
