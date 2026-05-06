package keeper_test

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// claimRewardsAtomicitySetup prepares state and mocks needed to reach payoutClaim.
// Uses default params where WorkVestingPeriod=0 (direct payment, not vested).
func claimRewardsAtomicitySetup(t *testing.T) (keeper.Keeper, types.MsgServer, sdk.Context, *keepertest.InferenceMocks, *types.MsgClaimRewards) {
	t.Helper()
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	ms := keeper.NewMsgServerImpl(k)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	// Create mock account and use its key for signing
	mockAccount := NewMockAccount(testutil.Creator)
	seed := uint64(1)
	seedBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(seedBytes, seed)
	signature, err := mockAccount.key.Sign(seedBytes)
	require.NoError(t, err)
	signatureHex := hex.EncodeToString(signature)

	// Setup epochs: claim is for epochIndex, current is epochIndex+1
	epochIndex := uint64(100)
	epoch := types.Epoch{Index: epochIndex, PocStartBlockHeight: 1000}
	k.SetEpoch(ctx, &epoch)

	currentEpochIndex := uint64(101)
	currentEpoch := types.Epoch{Index: currentEpochIndex, PocStartBlockHeight: 2000}
	k.SetEpoch(ctx, &currentEpoch)
	_ = k.SetEffectiveEpochIndex(ctx, currentEpoch.Index)

	// Epoch group data for both epochs
	k.SetEpochGroupData(sdk.UnwrapSDKContext(ctx), types.EpochGroupData{
		EpochIndex:          currentEpochIndex,
		EpochGroupId:        101,
		PocStartBlockHeight: currentEpochIndex,
		ValidationWeights: []*types.ValidationWeight{
			{MemberAddress: testutil.Creator, Weight: 10},
		},
	})
	k.SetEpochGroupData(sdk.UnwrapSDKContext(ctx), types.EpochGroupData{
		EpochIndex:          epochIndex,
		EpochGroupId:        100,
		PocStartBlockHeight: epochIndex,
		ValidationWeights: []*types.ValidationWeight{
			{MemberAddress: testutil.Creator, Weight: 10},
		},
	})

	// Register participant as ACTIVE and add to active participants sets
	creatorAddr, _ := sdk.AccAddressFromBech32(testutil.Creator)
	k.Participants.Set(ctx, creatorAddr, types.Participant{Index: testutil.Creator, Address: testutil.Creator, Status: types.ParticipantStatus_ACTIVE})
	k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId:      epochIndex,
		Participants: []*types.ActiveParticipant{{Index: testutil.Creator}},
	})
	k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId:      currentEpochIndex,
		Participants: []*types.ActiveParticipant{{Index: testutil.Creator}},
	})

	// Settle amount
	_ = k.SetSettleAmount(sdk.UnwrapSDKContext(ctx), types.SettleAmount{
		Participant:   testutil.Creator,
		EpochIndex:    epochIndex,
		WorkCoins:     1000,
		RewardCoins:   500,
		SeedSignature: signatureHex,
	})

	// Performance summary (Claimed=false)
	k.SetEpochPerformanceSummary(sdk.UnwrapSDKContext(ctx), types.EpochPerformanceSummary{
		EpochIndex:    epochIndex,
		ParticipantId: testutil.Creator,
		Claimed:       false,
	})

	// Validations (empty list so missed-stat-test passes trivially)
	k.SeedEpochGroupValidationEntries(sdk.UnwrapSDKContext(ctx), types.EpochGroupValidations{
		Participant:         testutil.Creator,
		EpochIndex:          epochIndex,
		ValidatedInferences: []string{},
	})

	// Mock account keeper: return mock account with matching pubkey
	addr, err := sdk.AccAddressFromBech32(testutil.Creator)
	require.NoError(t, err)
	mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), addr).Return(mockAccount).AnyTimes()

	// No grantees -- only the primary account key used for sig verification
	mocks.AuthzKeeper.EXPECT().GranterGrants(gomock.Any(), gomock.Any()).Return(&authztypes.QueryGranterGrantsResponse{Grants: []*authztypes.GrantAuthorization{}}, nil).AnyTimes()

	// Allow LogSubAccountTransaction calls
	mocks.BankKeeper.EXPECT().LogSubAccountTransaction(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()

	msg := &types.MsgClaimRewards{
		Creator:    testutil.Creator,
		EpochIndex: epochIndex,
		Seed:       1,
	}

	return k, ms, ctx.WithBlockHeight(claimDebounceBlocks + 1), &mocks, msg
}

// TestClaimRewards_EscrowPaymentFails_SettleRecordPreserved proves that when
// escrow payment fails, CacheContext prevents finishSettle from consuming
// the settle record. The participant can retry later.
func TestClaimRewards_EscrowPaymentFails_SettleRecordPreserved(t *testing.T) {
	k, ms, ctx, mocks, msg := claimRewardsAtomicitySetup(t)

	addr, _ := sdk.AccAddressFromBech32(testutil.Creator)
	workCoins := sdk.NewCoins(sdk.NewInt64Coin(types.BaseCoin, 1000))

	// Mock: escrow payment (direct, vesting=0) fails
	mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(
		gomock.Any(),
		types.ModuleName,
		addr,
		workCoins,
		gomock.Any(),
	).Return(fmt.Errorf("insufficient funds in module account"))

	resp, err := ms.ClaimRewards(ctx, msg)

	// ClaimRewards returns (response, nil) per the handler convention
	require.NoError(t, err, "ClaimRewards handler returns nil error by convention")
	require.NotNil(t, resp)
	require.Contains(t, resp.Result, "can be retried")

	// KEY ASSERTION: settle record must still exist for retry
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	sa, found := k.GetSettleAmount(sdkCtx, testutil.Creator)
	require.True(t, found, "settle record must be preserved when payment fails")
	require.Equal(t, uint64(1000), sa.WorkCoins, "work coins unchanged")
	require.Equal(t, uint64(500), sa.RewardCoins, "reward coins unchanged")

	// Performance summary must NOT be marked as claimed
	perf, found := k.GetEpochPerformanceSummary(sdkCtx, msg.EpochIndex, testutil.Creator)
	require.True(t, found)
	require.False(t, perf.Claimed, "claim must not be marked as completed on payment failure")
}

// TestClaimRewards_RewardPaymentFails_SettleRecordPreserved proves that when
// escrow payment succeeds but reward payment fails, CacheContext rolls back
// everything. No partial state persists.
func TestClaimRewards_RewardPaymentFails_SettleRecordPreserved(t *testing.T) {
	k, ms, ctx, mocks, msg := claimRewardsAtomicitySetup(t)

	addr, _ := sdk.AccAddressFromBech32(testutil.Creator)
	workCoins := sdk.NewCoins(sdk.NewInt64Coin(types.BaseCoin, 1000))
	rewardCoins := sdk.NewCoins(sdk.NewInt64Coin(types.BaseCoin, 500))

	// Mock: work payment succeeds
	mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(
		gomock.Any(),
		types.ModuleName,
		addr,
		workCoins,
		gomock.Any(),
	).Return(nil)

	// Mock: reward payment fails
	mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(
		gomock.Any(),
		types.ModuleName,
		addr,
		rewardCoins,
		gomock.Any(),
	).Return(fmt.Errorf("insufficient funds for rewards"))

	resp, err := ms.ClaimRewards(ctx, msg)

	require.NoError(t, err, "ClaimRewards handler returns nil error by convention")
	require.NotNil(t, resp)
	require.Contains(t, resp.Result, "can be retried")

	// KEY ASSERTION: settle record must still exist -- entire payout rolled back
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	sa, found := k.GetSettleAmount(sdkCtx, testutil.Creator)
	require.True(t, found, "settle record must be preserved when reward payment fails")
	require.Equal(t, uint64(1000), sa.WorkCoins, "work coins unchanged")
	require.Equal(t, uint64(500), sa.RewardCoins, "reward coins unchanged")

	// Performance summary must NOT be marked as claimed
	perf, found := k.GetEpochPerformanceSummary(sdkCtx, msg.EpochIndex, testutil.Creator)
	require.True(t, found)
	require.False(t, perf.Claimed, "claim must not be marked as completed on payment failure")
}

// TestClaimRewards_HappyPath_SettleRecordConsumed proves the normal success
// path: both payments succeed, settle record is consumed, claim is marked complete.
func TestClaimRewards_HappyPath_SettleRecordConsumed(t *testing.T) {
	k, ms, ctx, mocks, msg := claimRewardsAtomicitySetup(t)

	addr, _ := sdk.AccAddressFromBech32(testutil.Creator)
	workCoins := sdk.NewCoins(sdk.NewInt64Coin(types.BaseCoin, 1000))
	rewardCoins := sdk.NewCoins(sdk.NewInt64Coin(types.BaseCoin, 500))

	// Mock: both payments succeed
	mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(
		gomock.Any(),
		types.ModuleName,
		addr,
		workCoins,
		gomock.Any(),
	).Return(nil)

	mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(
		gomock.Any(),
		types.ModuleName,
		addr,
		rewardCoins,
		gomock.Any(),
	).Return(nil)

	resp, err := ms.ClaimRewards(ctx, msg)

	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, "Rewards claimed successfully", resp.Result)
	require.Equal(t, uint64(1500), resp.Amount)

	// Settle record should be consumed
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	_, found := k.GetSettleAmount(sdkCtx, testutil.Creator)
	require.False(t, found, "settle record must be consumed after successful claim")

	// Performance summary must be marked as claimed
	perf, found := k.GetEpochPerformanceSummary(sdkCtx, msg.EpochIndex, testutil.Creator)
	require.True(t, found)
	require.True(t, perf.Claimed, "claim must be marked as completed on success")
}
