package keeper_test

import (
	"testing"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/testutil/sample"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// This suite focuses on msg_server_invalidate_inference.go behavior around refunding the requester
// and subtracting the ActualCost from the executor.

func setupInvalidateHarness(t testing.TB) (keeper.Keeper, types.MsgServer, sdk.Context, *keepertest.InferenceMocks) {
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	ms := keeper.NewMsgServerImpl(k)
	return k, ms, ctx, &mocks
}

func setEffectiveEpoch(ctx sdk.Context, k keeper.Keeper, epochIndex uint64, mocks *keepertest.InferenceMocks) error {
	k.SetEpoch(ctx, &types.Epoch{Index: epochIndex})
	k.SetEffectiveEpochIndex(ctx, epochIndex)
	mocks.ExpectCreateGroupWithPolicyCall(ctx, epochIndex)
	eg, err := k.CreateEpochGroup(ctx, epochIndex, epochIndex)
	if err != nil {
		return err
	}
	err = eg.CreateGroup(ctx)
	if err != nil {
		return err
	}
	return nil
}

func TestInvalidateInference_RefundsRequesterAndChargesExecutor_NoSlash(t *testing.T) {
	k, ms, ctx, mocks := setupInvalidateHarness(t)

	// Configure params so that invalidation does NOT trigger status INVALID (no slash).
	params := types.DefaultParams()
	// Keep FalsePositiveRate at default 0.05 and start with 0 consecutive; after +1, probability is 0.05^1 = 0.05 > 1e-6, so remains ACTIVE, so no slashing.
	k.SetParams(ctx, params)

	err := setEffectiveEpoch(ctx, k, 1, mocks)
	require.NoError(t, err)

	// Create executor and payer accounts

	executorAddr := sample.AccAddress()
	payerAddr := sample.AccAddress()

	// Register executor with some coin balance and just below threshold
	executor := types.Participant{
		Index:                        executorAddr,
		Address:                      executorAddr,
		Status:                       types.ParticipantStatus_ACTIVE,
		ConsecutiveInvalidInferences: 0,
		CurrentEpochStats:            &types.CurrentEpochStats{},
		CoinBalance:                  1_000, // arbitrary internal balance field used by keeper
	}
	k.SetParticipant(ctx, executor)

	// Register payer (requester)
	payer := types.Participant{Index: payerAddr, Address: payerAddr, CurrentEpochStats: &types.CurrentEpochStats{}}
	k.SetParticipant(ctx, payer)

	// Inference with non-zero cost
	inferenceID := "refund-no-slash"
	actualCost := int64(123)
	k.SetInference(ctx, types.Inference{
		Index:           inferenceID,
		InferenceId:     inferenceID,
		ExecutedBy:      executorAddr,
		RequestedBy:     payerAddr,
		Status:          types.InferenceStatus_FINISHED,
		ActualCost:      actualCost,
		ProposalDetails: &types.ProposalDetails{PolicyAddress: payerAddr},
		EpochId:         1,
	})

	// Expect subaccount transaction log for executor debt subtraction
	mocks.BankKeeper.EXPECT().LogSubAccountTransaction(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any())
	// Expect refund to be issued to the payer via module->account transfer inside IssueRefund
	mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).Times(1)

	// We do NOT expect any slashing in this test
	mocks.CollateralKeeper.EXPECT().Slash(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Times(0)

	_, err = ms.InvalidateInference(ctx, &types.MsgInvalidateInference{Creator: payerAddr, InferenceId: inferenceID})
	require.NoError(t, err)

	// Verify executor was charged
	updatedExecutor, ok := k.GetParticipant(ctx, executorAddr)
	require.True(t, ok)
	require.Equal(t, int64(1_000-actualCost), updatedExecutor.CoinBalance)

	// Verify inference status updated
	updatedInf, found := k.GetInference(ctx, inferenceID)
	require.True(t, found)
	require.Equal(t, types.InferenceStatus_INVALIDATED, updatedInf.Status)
}

func TestInvalidateInference_RefundsRequesterAndChargesExecutor_WithSlash(t *testing.T) {
	k, ms, ctx, mocks := setupInvalidateHarness(t)

	// Configure params so that slashing will occur upon INVALID status; we will force INVALID by setting
	// executor.ConsecutiveInvalidInferences high enough that after +1, ProbabilityOfConsecutiveFailures < 1e-6.
	// With FPR=0.05, need N such that 0.05^N < 1e-6 => N > log(1e-6)/log(0.05) ~ 3.01/1.3 ~ 4; actually 0.05^5=3.125e-7 <1e-6.
	params := types.DefaultParams()
	params.CollateralParams.SlashFractionInvalid = types.DecimalFromFloat(0.25)
	k.SetParams(ctx, params)

	err := setEffectiveEpoch(ctx, k, 1, mocks)
	require.NoError(t, err)

	executorAddr := sample.AccAddress()
	payerAddr := sample.AccAddress()

	// Set executor with 4 consecutive invalids so increment => 5, causing INVALID by probability rule
	executor := types.Participant{
		Index:                        executorAddr,
		Address:                      executorAddr,
		Status:                       types.ParticipantStatus_ACTIVE,
		ConsecutiveInvalidInferences: 4,
		CurrentEpochStats:            &types.CurrentEpochStats{},
		CoinBalance:                  5_000,
	}
	k.SetParticipant(ctx, executor)

	// Register payer
	payer := types.Participant{Index: payerAddr, Address: payerAddr, CurrentEpochStats: &types.CurrentEpochStats{}}
	k.SetParticipant(ctx, payer)

	// Non-zero cost
	inferenceID := "refund-with-slash"
	actualCost := int64(250)
	k.SetInference(ctx, types.Inference{
		Index:           inferenceID,
		InferenceId:     inferenceID,
		ExecutedBy:      executorAddr,
		RequestedBy:     payerAddr,
		Status:          types.InferenceStatus_FINISHED,
		ActualCost:      actualCost,
		ProposalDetails: &types.ProposalDetails{PolicyAddress: payerAddr},
		EpochId:         1,
	})

	// Expect refund transfer to payer for ActualCost
	mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).Times(1)
	// Expect subaccount transaction log
	mocks.BankKeeper.EXPECT().LogSubAccountTransaction(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any())
	mocks.GroupKeeper.EXPECT().UpdateGroupMembers(gomock.Any(), gomock.Any())
	mocks.GroupKeeper.EXPECT().UpdateGroupMetadata(gomock.Any(), gomock.Any())

	// Expect a slash due to status transition to INVALID
	execAcc, _ := sdk.AccAddressFromBech32(executorAddr)
	slashFraction, _ := params.CollateralParams.SlashFractionInvalid.ToLegacyDec()
	mocks.CollateralKeeper.EXPECT().Slash(gomock.Any(), execAcc, slashFraction, types.SlashReasonInvalidation).Return(sdk.NewCoin(types.BaseCoin, math.NewInt(0)), nil).Times(1)

	_, err = ms.InvalidateInference(ctx, &types.MsgInvalidateInference{Creator: payerAddr, InferenceId: inferenceID})
	require.NoError(t, err)

	// Verify executor charged
	updatedExecutor, ok := k.GetParticipant(ctx, executorAddr)
	require.True(t, ok)
	require.Equal(t, int64(5_000-actualCost), updatedExecutor.CoinBalance)
	// And status should now be INVALID due to threshold 1
	require.Equal(t, types.ParticipantStatus_INVALID, updatedExecutor.Status)

	updatedInf, found := k.GetInference(ctx, inferenceID)
	require.True(t, found)
	require.Equal(t, types.InferenceStatus_INVALIDATED, updatedInf.Status)
}

func TestInvalidateInference_NextEpoch_NoRefundNoCharge_NoSlash(t *testing.T) {
	k, ms, ctx, mocks := setupInvalidateHarness(t)

	// Params where slashing could occur if status flips, but in next-epoch invalidation
	// we expect NO financial moves (no refund, no executor charge) and NO slash.
	params := types.DefaultParams()
	params.CollateralParams.SlashFractionInvalid = types.DecimalFromFloat(0.3)
	k.SetParams(ctx, params)

	err := setEffectiveEpoch(ctx, k, 1, mocks)
	require.NoError(t, err)

	executorAddr := sample.AccAddress()
	payerAddr := sample.AccAddress()

	initialBalance := int64(2_000)
	executor := types.Participant{
		Index:                        executorAddr,
		Address:                      executorAddr,
		Status:                       types.ParticipantStatus_ACTIVE,
		ConsecutiveInvalidInferences: 0,
		CurrentEpochStats:            &types.CurrentEpochStats{},
		CoinBalance:                  initialBalance,
	}
	k.SetParticipant(ctx, executor)

	payer := types.Participant{Index: payerAddr, Address: payerAddr, CurrentEpochStats: &types.CurrentEpochStats{}}
	k.SetParticipant(ctx, payer)

	inferenceID := "invalidate-next-epoch"
	actualCost := int64(777)
	k.SetInference(ctx, types.Inference{
		Index:           inferenceID,
		InferenceId:     inferenceID,
		ExecutedBy:      executorAddr,
		RequestedBy:     payerAddr,
		Status:          types.InferenceStatus_FINISHED,
		ActualCost:      actualCost,
		ProposalDetails: &types.ProposalDetails{PolicyAddress: payerAddr},
		EpochId:         1,
	})

	err = setEffectiveEpoch(ctx, k, 2, mocks)
	require.NoError(t, err)

	// In the correct behavior, since the invalidation happens after the epoch of execution,
	// there should be NO refund and NO charge to executor, and NO slashing.
	mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Times(0)
	mocks.BankKeeper.EXPECT().LogSubAccountTransaction(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Times(0)
	mocks.CollateralKeeper.EXPECT().Slash(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Times(0)

	_, err = ms.InvalidateInference(ctx, &types.MsgInvalidateInference{Creator: payerAddr, InferenceId: inferenceID})
	require.NoError(t, err)

	// Expect executor coin balance unchanged
	updatedExecutor, ok := k.GetParticipant(ctx, executorAddr)
	require.True(t, ok)
	require.Equal(t, initialBalance, updatedExecutor.CoinBalance)

	// Inference may be marked invalidated, but no financials changed
	updatedInf, found := k.GetInference(ctx, inferenceID)
	require.True(t, found)
	require.Equal(t, types.InferenceStatus_INVALIDATED, updatedInf.Status)
}

func TestInvalidateInference_FailsWithWrongPolicyAddress(t *testing.T) {
	k, ms, ctx, mocks := setupInvalidateHarness(t)

	// Setup epoch and group
	require.NoError(t, setEffectiveEpoch(ctx, k, 1, mocks))

	executorAddr := sample.AccAddress()
	payerAddr := sample.AccAddress()
	wrongCreator := sample.AccAddress()

	k.SetParticipant(ctx, types.Participant{Index: executorAddr, Address: executorAddr, CurrentEpochStats: &types.CurrentEpochStats{}})
	k.SetParticipant(ctx, types.Participant{Index: payerAddr, Address: payerAddr, CurrentEpochStats: &types.CurrentEpochStats{}})

	inferenceID := "wrong-policy"
	k.SetInference(ctx, types.Inference{
		Index:           inferenceID,
		InferenceId:     inferenceID,
		ExecutedBy:      executorAddr,
		RequestedBy:     payerAddr,
		Status:          types.InferenceStatus_FINISHED,
		ActualCost:      10,
		ProposalDetails: &types.ProposalDetails{PolicyAddress: payerAddr},
		EpochId:         1,
	})

	// Creator is not equal to policy address -> expect error
	_, err := ms.InvalidateInference(ctx, &types.MsgInvalidateInference{Creator: wrongCreator, InferenceId: inferenceID, Invalidator: payerAddr})
	require.Error(t, err)
}

func TestInvalidateInference_RemovesActiveInvalidations(t *testing.T) {
	k, ms, ctx, mocks := setupInvalidateHarness(t)

	// Set epoch 1
	require.NoError(t, setEffectiveEpoch(ctx, k, 1, mocks))

	executorAddr := sample.AccAddress()
	payerAddr := sample.AccAddress()
	invalidator := sample.AccAddress()
	k.SetParticipant(ctx, types.Participant{Index: executorAddr, Address: executorAddr, CurrentEpochStats: &types.CurrentEpochStats{}})
	k.SetParticipant(ctx, types.Participant{Index: payerAddr, Address: payerAddr, CurrentEpochStats: &types.CurrentEpochStats{}})

	inferenceID := "remove-active-invalidations"
	k.SetInference(ctx, types.Inference{
		Index:           inferenceID,
		InferenceId:     inferenceID,
		ExecutedBy:      executorAddr,
		RequestedBy:     payerAddr,
		Status:          types.InferenceStatus_FINISHED,
		ActualCost:      100,
		ProposalDetails: &types.ProposalDetails{PolicyAddress: payerAddr},
		EpochId:         1,
	})

	// Add ActiveInvalidations entry for invalidator
	addr := sdk.MustAccAddressFromBech32(invalidator)
	require.NoError(t, k.ActiveInvalidations.Set(ctx, collections.Join(addr, inferenceID)))
	has, err := k.ActiveInvalidations.Has(ctx, collections.Join(addr, inferenceID))
	require.NoError(t, err)
	require.True(t, has)

	// Move to next epoch to avoid any refund/charge side effects in this focused test
	require.NoError(t, setEffectiveEpoch(ctx, k, 2, mocks))
	mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Times(0)
	mocks.BankKeeper.EXPECT().LogSubAccountTransaction(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Times(0)
	mocks.CollateralKeeper.EXPECT().Slash(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Times(0)

	_, err = ms.InvalidateInference(ctx, &types.MsgInvalidateInference{Creator: payerAddr, InferenceId: inferenceID, Invalidator: invalidator})
	require.NoError(t, err)

	// ActiveInvalidations should be removed
	has, err = k.ActiveInvalidations.Has(ctx, collections.Join(addr, inferenceID))
	require.NoError(t, err)
	require.False(t, has)
}

func TestInvalidateInference_AlreadyInvalidated_RemovesActiveInvalidations(t *testing.T) {
	k, ms, ctx, mocks := setupInvalidateHarness(t)
	require.NoError(t, setEffectiveEpoch(ctx, k, 1, mocks))

	executorAddr := sample.AccAddress()
	payerAddr := sample.AccAddress()
	invalidator := sample.AccAddress()
	k.SetParticipant(ctx, types.Participant{Index: executorAddr, Address: executorAddr, CurrentEpochStats: &types.CurrentEpochStats{}})
	k.SetParticipant(ctx, types.Participant{Index: payerAddr, Address: payerAddr, CurrentEpochStats: &types.CurrentEpochStats{}})

	inferenceID := "already-invalidated-removes"
	k.SetInference(ctx, types.Inference{
		Index:           inferenceID,
		InferenceId:     inferenceID,
		ExecutedBy:      executorAddr,
		RequestedBy:     payerAddr,
		Status:          types.InferenceStatus_INVALIDATED, // already invalidated
		ActualCost:      50,
		ProposalDetails: &types.ProposalDetails{PolicyAddress: payerAddr},
		EpochId:         1,
	})

	// Add ActiveInvalidations entry which should be removed even on idempotent path
	addr := sdk.MustAccAddressFromBech32(invalidator)
	require.NoError(t, k.ActiveInvalidations.Set(ctx, collections.Join(addr, inferenceID)))
	has, err := k.ActiveInvalidations.Has(ctx, collections.Join(addr, inferenceID))
	require.NoError(t, err)
	require.True(t, has)

	// Move to next epoch to avoid any refund/charge expectations
	require.NoError(t, setEffectiveEpoch(ctx, k, 2, mocks))
	mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Times(0)
	mocks.BankKeeper.EXPECT().LogSubAccountTransaction(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Times(0)
	mocks.CollateralKeeper.EXPECT().Slash(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Times(0)

	// Call invalidate; should succeed and remove ActiveInvalidations
	_, err = ms.InvalidateInference(ctx, &types.MsgInvalidateInference{Creator: payerAddr, InferenceId: inferenceID, Invalidator: invalidator})
	require.NoError(t, err)

	has, err = k.ActiveInvalidations.Has(ctx, collections.Join(addr, inferenceID))
	require.NoError(t, err)
	require.False(t, has)
}
