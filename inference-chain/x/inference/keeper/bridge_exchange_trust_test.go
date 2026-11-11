package keeper_test

import (
	"testing"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/group"
	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

const (
	trustTestEpoch  = uint64(7)
	trustTestBridge = "0x972a7a92d92796a98801a8818bcf91f1648f2f68"
)

type trustMember struct {
	addr        sdk.AccAddress
	weight      int64
	capWeight   int64
	notAdmitted bool
}

func trustTestAddr(seed string) sdk.AccAddress {
	return sdk.AccAddress([]byte(seed))
}

func setupBridgeTrust(t *testing.T, members []trustMember, capApplied bool) (keeper.Keeper, types.MsgServer, sdk.Context, *keepertest.InferenceMocks) {
	t.Helper()
	k, ms, ctx, mocks := setupKeeperWithMocks(t)

	require.NoError(t, k.SetEffectiveEpochIndex(ctx, trustTestEpoch))
	var totalWeight int64
	participants := make([]*types.ActiveParticipant, 0, len(members))
	groupMembers := make([]*group.GroupMember, 0, len(members))
	for _, m := range members {
		totalWeight += m.weight
		participant := &types.ActiveParticipant{
			Index:     m.addr.String(),
			Weight:    m.weight,
			CapWeight: m.capWeight,
		}
		if !m.notAdmitted {
			participant.Seed = &types.RandomSeed{}
		}
		participants = append(participants, participant)
		if m.notAdmitted {
			continue
		}
		groupMembers = append(groupMembers, &group.GroupMember{
			GroupId: 1,
			Member:  &group.Member{Address: m.addr.String(), Weight: math.NewInt(m.weight).String()},
		})
	}
	for _, m := range members {
		mocks.AccountKeeper.EXPECT().HasAccount(gomock.Any(), m.addr).Return(true).AnyTimes()
	}
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:   trustTestEpoch,
		EpochGroupId: 1,
		TotalWeight:  totalWeight,
	})
	mocks.GroupKeeper.EXPECT().GroupMembers(gomock.Any(), gomock.Any()).Return(
		&group.QueryGroupMembersResponse{Members: groupMembers}, nil,
	).AnyTimes()
	aps := types.ActiveParticipants{
		EpochId:          trustTestEpoch,
		Participants:     participants,
		CapWeightApplied: capApplied,
	}
	require.NoError(t, k.SetActiveParticipants(ctx, aps))
	require.NoError(t, k.SetEpochTrustWeights(ctx, aps))
	k.SetBridgeContractAddress(ctx, types.BridgeContractAddress{ChainId: "ethereum", Address: trustTestBridge})
	return k, ms, ctx, mocks
}

func fakeBurn(voter sdk.AccAddress, receipt string) *types.MsgBridgeExchange {
	return &types.MsgBridgeExchange{
		Validator:       voter.String(),
		OriginChain:     "ethereum",
		ContractAddress: trustTestBridge,
		OwnerAddress:    testutil.Requester,
		Amount:          "100",
		BlockNumber:     "1000",
		ReceiptIndex:    receipt,
		ReceiptsRoot:    "0xroot",
	}
}

func bridgeRecord(t *testing.T, k keeper.Keeper, ctx sdk.Context, msg *types.MsgBridgeExchange) *types.BridgeTransaction {
	t.Helper()
	tx, found := k.GetBridgeTransactionByContent(ctx, &types.BridgeTransaction{
		ChainId:         msg.OriginChain,
		ContractAddress: msg.ContractAddress,
		OwnerAddress:    msg.OwnerAddress,
		Amount:          msg.Amount,
		BlockNumber:     msg.BlockNumber,
		ReceiptIndex:    msg.ReceiptIndex,
		ReceiptsRoot:    msg.ReceiptsRoot,
	})
	require.True(t, found)
	return tx
}

func expectEscrowRelease(mocks *keepertest.InferenceMocks, times int) {
	mocks.AccountKeeper.EXPECT().GetModuleAddress(types.BridgeEscrowAccName).Return(trustTestAddr("bridge_escrow_______")).AnyTimes()
	mocks.BankViewKeeper.EXPECT().SpendableCoin(gomock.Any(), gomock.Any(), types.BaseCoin).Return(sdk.NewInt64Coin(types.BaseCoin, 1000)).AnyTimes()
	mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(gomock.Any(), types.BridgeEscrowAccName, gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).Times(times)
}

func TestBridgeExchange_ZeroCapRawMajorityCannotRelease(t *testing.T) {
	a := trustTestAddr("zero_cap_member_a___")
	b := trustTestAddr("zero_cap_member_b___")
	honest := trustTestAddr("trusted_incumbent___")
	k, ms, ctx, mocks := setupBridgeTrust(t, []trustMember{
		{addr: a, weight: 26, capWeight: 0},
		{addr: b, weight: 26, capWeight: 0},
		{addr: honest, weight: 48, capWeight: 48},
	}, true)
	expectEscrowRelease(mocks, 0)

	msg := fakeBurn(a, "1")
	_, err := ms.BridgeExchange(ctx, msg)
	require.NoError(t, err)
	msg.Validator = b.String()
	_, err = ms.BridgeExchange(ctx, msg)
	require.NoError(t, err)

	tx := bridgeRecord(t, k, ctx, msg)
	require.Equal(t, types.BridgeTransactionStatus_BRIDGE_PENDING, tx.Status)
	require.Equal(t, int64(0), tx.TotalValidationPower)
	require.Len(t, tx.Validators, 2)
}

func TestBridgeExchange_CapMajorityWithRawMinorityReleases(t *testing.T) {
	newcomer := trustTestAddr("zero_cap_newcomer___")
	first := trustTestAddr("trusted_incumbent_1_")
	second := trustTestAddr("trusted_incumbent_2_")
	k, ms, ctx, mocks := setupBridgeTrust(t, []trustMember{
		{addr: newcomer, weight: 60, capWeight: 0},
		{addr: first, weight: 20, capWeight: 20},
		{addr: second, weight: 20, capWeight: 15},
	}, true)
	expectEscrowRelease(mocks, 1)

	msg := fakeBurn(first, "2")
	_, err := ms.BridgeExchange(ctx, msg)
	require.NoError(t, err)
	msg.Validator = second.String()
	_, err = ms.BridgeExchange(ctx, msg)
	require.NoError(t, err)

	tx := bridgeRecord(t, k, ctx, msg)
	require.Equal(t, types.BridgeTransactionStatus_BRIDGE_COMPLETED, tx.Status)
	require.Equal(t, int64(35), tx.TotalValidationPower)
}

func TestBridgeExchange_PreCapEpochUsesWeight(t *testing.T) {
	first := trustTestAddr("legacy_participant_1")
	second := trustTestAddr("legacy_participant_2")
	k, ms, ctx, mocks := setupBridgeTrust(t, []trustMember{
		{addr: first, weight: 30},
		{addr: second, weight: 40},
	}, false)
	expectEscrowRelease(mocks, 1)

	msg := fakeBurn(first, "3")
	_, err := ms.BridgeExchange(ctx, msg)
	require.NoError(t, err)
	msg.Validator = second.String()
	_, err = ms.BridgeExchange(ctx, msg)
	require.NoError(t, err)

	tx := bridgeRecord(t, k, ctx, msg)
	require.Equal(t, types.BridgeTransactionStatus_BRIDGE_COMPLETED, tx.Status)
	require.Equal(t, int64(70), tx.TotalValidationPower)
}

func TestBridgeExchange_UnindexedEpochFallsBackToActiveParticipants(t *testing.T) {
	a := trustTestAddr("zero_cap_member_a___")
	b := trustTestAddr("zero_cap_member_b___")
	honest := trustTestAddr("trusted_incumbent___")
	k, ms, ctx, mocks := setupBridgeTrust(t, []trustMember{
		{addr: a, weight: 26, capWeight: 0},
		{addr: b, weight: 26, capWeight: 0},
		{addr: honest, weight: 48, capWeight: 48},
	}, true)
	require.NoError(t, k.ClearEpochTrustWeights(ctx, trustTestEpoch))
	expectEscrowRelease(mocks, 0)

	trust, err := k.GetEpochTrust(ctx, trustTestEpoch)
	require.NoError(t, err)
	require.Equal(t, int64(48), trust.Total)

	msg := fakeBurn(a, "4")
	_, err = ms.BridgeExchange(ctx, msg)
	require.NoError(t, err)
	msg.Validator = b.String()
	_, err = ms.BridgeExchange(ctx, msg)
	require.NoError(t, err)

	tx := bridgeRecord(t, k, ctx, msg)
	require.Equal(t, types.BridgeTransactionStatus_BRIDGE_PENDING, tx.Status)
}

func TestBridgeExchange_MissingTrustWeightsRejectsVote(t *testing.T) {
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
	voter := trustTestAddr("orphan_epoch_voter__")

	require.NoError(t, k.SetEffectiveEpochIndex(ctx, trustTestEpoch))
	require.NoError(t, k.ActiveParticipantsSet.Set(ctx, collections.Join(trustTestEpoch, voter)))
	k.SetEpochGroupData(ctx, types.EpochGroupData{EpochIndex: trustTestEpoch, EpochGroupId: 1, TotalWeight: 10})
	mocks.GroupKeeper.EXPECT().GroupMembers(gomock.Any(), gomock.Any()).Return(
		&group.QueryGroupMembersResponse{Members: []*group.GroupMember{{
			GroupId: 1,
			Member:  &group.Member{Address: voter.String(), Weight: "10"},
		}}}, nil,
	).AnyTimes()

	_, err := k.ValidateBridgeExchange(ctx, fakeBurn(voter, "5"))
	require.ErrorIs(t, err, types.ErrActiveParticipantNotFound)
}

func TestSetEpochTrustWeights_IndexesAdmittedMembers(t *testing.T) {
	a := trustTestAddr("indexed_participant1")
	b := trustTestAddr("indexed_participant2")
	k, _, ctx, _ := setupBridgeTrust(t, []trustMember{
		{addr: a, weight: 50, capWeight: 0},
		{addr: b, weight: 50, capWeight: 20},
	}, true)

	total, err := k.EpochTrustWeightTotals.Get(ctx, trustTestEpoch)
	require.NoError(t, err)
	require.Equal(t, int64(20), total)
	trust, err := k.GetEpochTrust(ctx, trustTestEpoch)
	require.NoError(t, err)
	weightA, err := k.EpochTrustWeight(ctx, trust, a)
	require.NoError(t, err)
	require.Equal(t, int64(0), weightA)
	weightB, err := k.EpochTrustWeight(ctx, trust, b)
	require.NoError(t, err)
	require.Equal(t, int64(20), weightB)

	require.NoError(t, k.ClearEpochTrustWeights(ctx, trustTestEpoch))
	has, err := k.EpochTrustWeightTotals.Has(ctx, trustTestEpoch)
	require.NoError(t, err)
	require.False(t, has)
	has, err = k.EpochTrustWeights.Has(ctx, collections.Join(trustTestEpoch, b))
	require.NoError(t, err)
	require.False(t, has)
}

func storeLegacyRecord(t *testing.T, k keeper.Keeper, ctx sdk.Context, receipt string, epochIndex uint64, status types.BridgeTransactionStatus, rawPower int64, voters ...sdk.AccAddress) {
	t.Helper()
	record := &types.BridgeTransaction{
		ChainId:              "ethereum",
		ContractAddress:      trustTestBridge,
		OwnerAddress:         testutil.Requester,
		Amount:               "100",
		BlockNumber:          "1000",
		ReceiptIndex:         receipt,
		ReceiptsRoot:         "0xroot",
		EpochIndex:           epochIndex,
		Status:               status,
		TotalValidationPower: rawPower,
	}
	k.SetBridgeTransaction(ctx, record)
	for _, voter := range voters {
		require.NoError(t, k.AddBridgeTransactionValidator(ctx, record, voter.String()))
	}
}

func TestMigrateBridgeTrustPower_ConvertsRawVotes(t *testing.T) {
	a := trustTestAddr("zero_cap_member_a___")
	b := trustTestAddr("zero_cap_member_b___")
	honest := trustTestAddr("trusted_incumbent___")
	k, ms, ctx, mocks := setupBridgeTrust(t, []trustMember{
		{addr: a, weight: 45, capWeight: 0},
		{addr: b, weight: 10, capWeight: 0},
		{addr: honest, weight: 45, capWeight: 40},
	}, true)
	expectEscrowRelease(mocks, 0)
	storeLegacyRecord(t, k, ctx, "6", trustTestEpoch, types.BridgeTransactionStatus_BRIDGE_PENDING, 45, a)

	result, err := k.MigrateBridgeTrustPower(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, result.Recomputed)
	require.Equal(t, 0, result.Completed)

	msg := fakeBurn(a, "6")
	tx := bridgeRecord(t, k, ctx, msg)
	require.Equal(t, int64(0), tx.TotalValidationPower)
	require.Equal(t, []string{a.String()}, tx.Validators)

	_, err = ms.BridgeExchange(ctx, fakeBurn(b, "6"))
	require.NoError(t, err)
	tx = bridgeRecord(t, k, ctx, msg)
	require.Equal(t, types.BridgeTransactionStatus_BRIDGE_PENDING, tx.Status)
	require.Equal(t, int64(0), tx.TotalValidationPower)
}

func TestMigrateBridgeTrustPower_CompletesTrustedMajority(t *testing.T) {
	first := trustTestAddr("trusted_incumbent_1_")
	second := trustTestAddr("trusted_incumbent_2_")
	third := trustTestAddr("trusted_incumbent_3_")
	newcomer := trustTestAddr("zero_cap_newcomer___")
	k, ms, ctx, mocks := setupBridgeTrust(t, []trustMember{
		{addr: first, weight: 10, capWeight: 10},
		{addr: second, weight: 10, capWeight: 10},
		{addr: third, weight: 10, capWeight: 10},
		{addr: newcomer, weight: 70, capWeight: 0},
	}, true)
	expectEscrowRelease(mocks, 1)
	storeLegacyRecord(t, k, ctx, "8", trustTestEpoch, types.BridgeTransactionStatus_BRIDGE_PENDING, 30, first, second, third)

	result, err := k.MigrateBridgeTrustPower(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, result.Completed)

	tx := bridgeRecord(t, k, ctx, fakeBurn(first, "8"))
	require.Equal(t, types.BridgeTransactionStatus_BRIDGE_COMPLETED, tx.Status)
	require.Equal(t, int64(30), tx.TotalValidationPower)

	_, err = ms.BridgeExchange(ctx, fakeBurn(newcomer, "8"))
	require.NoError(t, err)
	require.Equal(t, int64(30), bridgeRecord(t, k, ctx, fakeBurn(first, "8")).TotalValidationPower)
}

func TestMigrateBridgeTrustPower_FailedCompletionStaysPending(t *testing.T) {
	first := trustTestAddr("trusted_incumbent_1_")
	k, _, ctx, mocks := setupBridgeTrust(t, []trustMember{
		{addr: first, weight: 10, capWeight: 10},
	}, true)
	mocks.AccountKeeper.EXPECT().GetModuleAddress(types.BridgeEscrowAccName).Return(trustTestAddr("bridge_escrow_______")).AnyTimes()
	mocks.BankViewKeeper.EXPECT().SpendableCoin(gomock.Any(), gomock.Any(), types.BaseCoin).Return(sdk.NewInt64Coin(types.BaseCoin, 1)).AnyTimes()
	storeLegacyRecord(t, k, ctx, "9", trustTestEpoch, types.BridgeTransactionStatus_BRIDGE_PENDING, 10, first)

	result, err := k.MigrateBridgeTrustPower(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, result.Completed)
	tx := bridgeRecord(t, k, ctx, fakeBurn(first, "9"))
	require.Equal(t, types.BridgeTransactionStatus_BRIDGE_PENDING, tx.Status)
	require.Equal(t, int64(10), tx.TotalValidationPower)
}

func TestMigrateBridgeTrustPower_OldEpochRecomputedNotCompleted(t *testing.T) {
	first := trustTestAddr("trusted_incumbent_1_")
	k, _, ctx, mocks := setupBridgeTrust(t, []trustMember{
		{addr: first, weight: 10, capWeight: 10},
	}, true)
	expectEscrowRelease(mocks, 0)
	oldEpoch := trustTestEpoch - 3
	k.SetEpochGroupData(ctx, types.EpochGroupData{EpochIndex: oldEpoch, EpochGroupId: 1, TotalWeight: 10})
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId:      oldEpoch,
		Participants: []*types.ActiveParticipant{{Index: first.String(), Weight: 10, Seed: &types.RandomSeed{}}},
	}))
	storeLegacyRecord(t, k, ctx, "10", oldEpoch, types.BridgeTransactionStatus_BRIDGE_PENDING, 3, first)

	result, err := k.MigrateBridgeTrustPower(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, result.Completed)
	tx := bridgeRecord(t, k, ctx, fakeBurn(first, "10"))
	require.Equal(t, types.BridgeTransactionStatus_BRIDGE_PENDING, tx.Status)
	require.Equal(t, int64(10), tx.TotalValidationPower)
}

func TestMigrateBridgeTrustPower_RecomputesCompletedRecords(t *testing.T) {
	a := trustTestAddr("trusted_voter_a_____")
	b := trustTestAddr("trusted_voter_b_____")
	late := trustTestAddr("trusted_voter_late__")
	k, ms, ctx, mocks := setupBridgeTrust(t, []trustMember{
		{addr: a, weight: 35, capWeight: 10},
		{addr: b, weight: 35, capWeight: 10},
		{addr: late, weight: 30, capWeight: 5},
	}, true)
	expectEscrowRelease(mocks, 0)
	storeLegacyRecord(t, k, ctx, "11", trustTestEpoch, types.BridgeTransactionStatus_BRIDGE_COMPLETED, 70, a, b)

	_, err := k.MigrateBridgeTrustPower(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(20), bridgeRecord(t, k, ctx, fakeBurn(a, "11")).TotalValidationPower)

	_, err = ms.BridgeExchange(ctx, fakeBurn(late, "11"))
	require.NoError(t, err)
	tx := bridgeRecord(t, k, ctx, fakeBurn(a, "11"))
	require.Equal(t, types.BridgeTransactionStatus_BRIDGE_COMPLETED, tx.Status)
	require.Equal(t, int64(25), tx.TotalValidationPower)
}

func TestBridgeExchange_NonAdmittedParticipantsExcludedFromDenominator(t *testing.T) {
	first := trustTestAddr("admitted_member_1___")
	second := trustTestAddr("admitted_member_2___")
	k, ms, ctx, mocks := setupBridgeTrust(t, []trustMember{
		{addr: first, weight: 20, capWeight: 20},
		{addr: second, weight: 20, capWeight: 20},
		{addr: trustTestAddr("seedless_member_1___"), weight: 20, capWeight: 20, notAdmitted: true},
		{addr: trustTestAddr("seedless_member_2___"), weight: 20, capWeight: 20, notAdmitted: true},
		{addr: trustTestAddr("seedless_member_3___"), weight: 20, capWeight: 20, notAdmitted: true},
	}, true)
	expectEscrowRelease(mocks, 1)

	for _, indexed := range []bool{true, false} {
		if !indexed {
			require.NoError(t, k.ClearEpochTrustWeights(ctx, trustTestEpoch))
		}
		trust, err := k.GetEpochTrust(ctx, trustTestEpoch)
		require.NoError(t, err)
		require.Equal(t, int64(40), trust.Total)
	}

	msg := fakeBurn(first, "12")
	_, err := ms.BridgeExchange(ctx, msg)
	require.NoError(t, err)
	msg.Validator = second.String()
	_, err = ms.BridgeExchange(ctx, msg)
	require.NoError(t, err)
	require.Equal(t, types.BridgeTransactionStatus_BRIDGE_COMPLETED, bridgeRecord(t, k, ctx, msg).Status)
}

func TestAddEpochTrustWeight_AccumulatesOnce(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
	a := trustTestAddr("admitted_member_1___")

	require.NoError(t, k.ResetEpochTrustWeights(ctx, trustTestEpoch))
	trust, err := k.GetEpochTrust(ctx, trustTestEpoch)
	require.NoError(t, err)
	require.Equal(t, int64(0), trust.Total)

	require.NoError(t, k.AddEpochTrustWeight(ctx, trustTestEpoch, a.String(), 15))
	require.NoError(t, k.AddEpochTrustWeight(ctx, trustTestEpoch, a.String(), 15))
	require.NoError(t, k.AddEpochTrustWeight(ctx, trustTestEpoch, trustTestAddr("zero_cap_member_____").String(), 0))
	total, err := k.EpochTrustWeightTotals.Get(ctx, trustTestEpoch)
	require.NoError(t, err)
	require.Equal(t, int64(15), total)
}

func TestBackfillEpochTrustWeights(t *testing.T) {
	a := trustTestAddr("backfill_participant")
	k, _, ctx, _ := setupBridgeTrust(t, []trustMember{
		{addr: a, weight: 50, capWeight: 30},
	}, true)
	require.NoError(t, k.ClearEpochTrustWeights(ctx, trustTestEpoch))

	n, err := k.BackfillEpochTrustWeights(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	total, err := k.EpochTrustWeightTotals.Get(ctx, trustTestEpoch)
	require.NoError(t, err)
	require.Equal(t, int64(30), total)
}

func TestEpochTrust_ExcludedMemberCountsAndFailedAdmissionDoesNot(t *testing.T) {
	member := trustTestAddr("admitted_member_1___")
	removed := trustTestAddr("removed_member______")
	failed := trustTestAddr("failed_admission____")
	k, _, ctx, _ := setupBridgeTrust(t, []trustMember{
		{addr: member, weight: 30, capWeight: 30},
		{addr: removed, weight: 20, capWeight: 20, notAdmitted: true},
		{addr: failed, weight: 50, capWeight: 50, notAdmitted: true},
	}, true)
	require.NoError(t, k.ExcludedParticipantsMap.Set(ctx, collections.Join(trustTestEpoch, removed), types.ExcludedParticipant{
		Address:    removed.String(),
		EpochIndex: trustTestEpoch,
	}))

	require.NoError(t, k.ClearEpochTrustWeights(ctx, trustTestEpoch))
	fallback, err := k.GetEpochTrust(ctx, trustTestEpoch)
	require.NoError(t, err)
	require.Equal(t, int64(50), fallback.Total)

	n, err := k.BackfillEpochTrustWeights(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	indexed, err := k.GetEpochTrust(ctx, trustTestEpoch)
	require.NoError(t, err)
	require.Equal(t, int64(50), indexed.Total)
	for addr, expected := range map[string]int64{member.String(): 30, removed.String(): 20, failed.String(): 0} {
		weight, err := k.EpochTrustWeight(ctx, indexed, sdk.MustAccAddressFromBech32(addr))
		require.NoError(t, err)
		require.Equal(t, expected, weight)
	}
}

func TestBridgeExchange_RetryCompletesAfterFailedMigration(t *testing.T) {
	first := trustTestAddr("trusted_incumbent_1_")
	k, ms, ctx, mocks := setupBridgeTrust(t, []trustMember{
		{addr: first, weight: 10, capWeight: 10},
	}, true)
	escrow := sdk.NewInt64Coin(types.BaseCoin, 1)
	mocks.AccountKeeper.EXPECT().GetModuleAddress(types.BridgeEscrowAccName).Return(trustTestAddr("bridge_escrow_______")).AnyTimes()
	mocks.BankViewKeeper.EXPECT().SpendableCoin(gomock.Any(), gomock.Any(), types.BaseCoin).DoAndReturn(
		func(_ any, _ any, _ string) sdk.Coin { return escrow },
	).AnyTimes()
	mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(gomock.Any(), types.BridgeEscrowAccName, gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).Times(1)
	storeLegacyRecord(t, k, ctx, "13", trustTestEpoch, types.BridgeTransactionStatus_BRIDGE_PENDING, 10, first)

	result, err := k.MigrateBridgeTrustPower(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, result.Completed)

	failedTxCtx, _ := ctx.CacheContext()
	_, err = ms.BridgeExchange(failedTxCtx, fakeBurn(first, "13"))
	require.Error(t, err)
	require.Equal(t, types.BridgeTransactionStatus_BRIDGE_PENDING, bridgeRecord(t, k, ctx, fakeBurn(first, "13")).Status)

	escrow = sdk.NewInt64Coin(types.BaseCoin, 1000)
	_, err = ms.BridgeExchange(ctx, fakeBurn(first, "13"))
	require.NoError(t, err)
	tx := bridgeRecord(t, k, ctx, fakeBurn(first, "13"))
	require.Equal(t, types.BridgeTransactionStatus_BRIDGE_COMPLETED, tx.Status)
	require.Equal(t, int64(10), tx.TotalValidationPower)
	require.Len(t, tx.Validators, 1)

	_, err = ms.BridgeExchange(ctx, fakeBurn(first, "13"))
	require.ErrorIs(t, err, types.ErrBridgeAlreadyValidated)
}

func TestBridgeExchange_DuplicateVoteBelowMajorityRejected(t *testing.T) {
	first := trustTestAddr("trusted_incumbent_1_")
	second := trustTestAddr("trusted_incumbent_2_")
	k, ms, ctx, mocks := setupBridgeTrust(t, []trustMember{
		{addr: first, weight: 10, capWeight: 10},
		{addr: second, weight: 10, capWeight: 10},
	}, true)
	expectEscrowRelease(mocks, 0)

	_, err := ms.BridgeExchange(ctx, fakeBurn(first, "14"))
	require.NoError(t, err)
	_, err = ms.BridgeExchange(ctx, fakeBurn(first, "14"))
	require.ErrorIs(t, err, types.ErrBridgeAlreadyValidated)
	require.Equal(t, int64(10), bridgeRecord(t, k, ctx, fakeBurn(first, "14")).TotalValidationPower)
}

func TestBridgeExchange_FirstVoteWithMajorityCompletes(t *testing.T) {
	first := trustTestAddr("trusted_incumbent_1_")
	newcomer := trustTestAddr("zero_cap_newcomer___")
	k, ms, ctx, mocks := setupBridgeTrust(t, []trustMember{
		{addr: first, weight: 10, capWeight: 10},
		{addr: newcomer, weight: 90, capWeight: 0},
	}, true)
	expectEscrowRelease(mocks, 1)

	_, err := ms.BridgeExchange(ctx, fakeBurn(first, "15"))
	require.NoError(t, err)
	tx := bridgeRecord(t, k, ctx, fakeBurn(first, "15"))
	require.Equal(t, types.BridgeTransactionStatus_BRIDGE_COMPLETED, tx.Status)
	require.Equal(t, int64(10), tx.TotalValidationPower)
}

func TestBridgeExchange_FirstVoteZeroCapNeverCompletes(t *testing.T) {
	newcomer := trustTestAddr("zero_cap_newcomer___")
	k, ms, ctx, mocks := setupBridgeTrust(t, []trustMember{
		{addr: newcomer, weight: 90, capWeight: 0},
	}, true)
	expectEscrowRelease(mocks, 0)

	_, err := ms.BridgeExchange(ctx, fakeBurn(newcomer, "16"))
	require.NoError(t, err)
	require.Equal(t, types.BridgeTransactionStatus_BRIDGE_PENDING, bridgeRecord(t, k, ctx, fakeBurn(newcomer, "16")).Status)
}
