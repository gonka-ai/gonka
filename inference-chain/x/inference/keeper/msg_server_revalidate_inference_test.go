package keeper_test

import (
	"context"
	"testing"
	"time"

	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

const revalidationTestBlockHeight int64 = 100
const revalidationTestBlockHash = "blockhash_for_revalidation_tests"

// mockRevalidationEventsProvider returns fixed events per height for ProcessPendingRevalidationEvents.
type mockRevalidationEventsProvider struct {
	eventsByHeight map[int64][]keeper.RevalidationEventInfo
}

func (m *mockRevalidationEventsProvider) GetInferenceValidationRevalidationEvents(_ context.Context, height int64) ([]keeper.RevalidationEventInfo, error) {
	return m.eventsByHeight[height], nil
}

// setupInferenceInVoting creates a finished inference, then one below-threshold validation to set status VOTING.
// Does not use policy/x/group; used for revalidation voting tests.
func setupInferenceInVoting(t *testing.T) (*MockInferenceHelper, keeper.Keeper, *types.Inference) {
	inferenceHelper, k, ctx := NewMockInferenceHelper(t)
	createParticipants(t, inferenceHelper.MessageServer, ctx)
	model := &types.Model{Id: MODEL_ID, ValidationThreshold: &types.Decimal{Value: 85, Exponent: -2}}
	k.SetModel(ctx, model)
	StubModelSubgroup(t, ctx, k, inferenceHelper.Mocks, model)
	addMembersToGroupData(k, ctx)

	expected, err := inferenceHelper.StartInference("promptPayload", model.Id, time.Now().UnixNano(), calculations.DefaultMaxTokens)
	require.NoError(t, err)
	_, err = inferenceHelper.FinishInference()
	require.NoError(t, err)

	_, err = inferenceHelper.MessageServer.Validation(ctx, &types.MsgValidation{
		InferenceId:  expected.InferenceId,
		Creator:      testutil.Validator,
		ValueDecimal: types.DecimalFromFloat(0.0),
	})
	require.NoError(t, err)
	inf, found := k.GetInference(ctx, expected.InferenceId)
	require.True(t, found)
	require.Equal(t, types.InferenceStatus_VOTING, inf.Status)
	return inferenceHelper, k, &inf
}

// TestNormalizedParticipantsForBlock asserts that SetNormalizedParticipantsForCommittedBlock builds
// the normalized tree from epoch group data and GetNormalizedWeightedParticipants returns it.
func TestNormalizedParticipantsForBlock(t *testing.T) {
	inferenceHelper, k, ctx := NewMockInferenceHelper(t)
	createParticipants(t, inferenceHelper.MessageServer, ctx)
	model := &types.Model{Id: MODEL_ID, ValidationThreshold: &types.Decimal{Value: 85, Exponent: -2}}
	k.SetModel(ctx, model)
	StubModelSubgroup(t, ctx, k, inferenceHelper.Mocks, model)
	addMembersToGroupData(k, ctx)

	blockHash := []byte(revalidationTestBlockHash)
	kPtr := inferenceHelper.keeper
	kPtr.SetNormalizedParticipantsForCommittedBlock(ctx, revalidationTestBlockHeight, blockHash)

	tree, ok := k.GetNormalizedWeightedParticipants(blockHash, MODEL_ID)
	require.True(t, ok)
	require.NotNil(t, tree)
	require.GreaterOrEqual(t, tree.Len(), 2, "expect at least Validator and Requester from addMembersToGroupData")
	var prev float64
	count := 0
	tree.Scan(func(cumWeight float64, addr string) bool {
		require.GreaterOrEqual(t, cumWeight, prev)
		require.NotEmpty(t, addr)
		prev = cumWeight
		count++
		return true
	})
	require.GreaterOrEqual(t, count, 2)
	require.LessOrEqual(t, prev, 1.0+1e-9)
}

// TestProcessPendingRevalidationEvents_PopulatesEligibleAndStartsSession asserts that after
// SetNormalizedParticipantsForCommittedBlock and ProcessPendingRevalidationEvents with a mock provider,
// the invalidator and sampled participants are eligible to vote and the ephemeral session is started.
func TestProcessPendingRevalidationEvents_PopulatesEligibleAndStartsSession(t *testing.T) {
	inferenceHelper, k, ctx := NewMockInferenceHelper(t)
	createParticipants(t, inferenceHelper.MessageServer, ctx)
	model := &types.Model{Id: MODEL_ID, ValidationThreshold: &types.Decimal{Value: 85, Exponent: -2}}
	k.SetModel(ctx, model)
	StubModelSubgroup(t, ctx, k, inferenceHelper.Mocks, model)
	addMembersToGroupData(k, ctx)

	expected, err := inferenceHelper.StartInference("promptPayload", model.Id, time.Now().UnixNano(), calculations.DefaultMaxTokens)
	require.NoError(t, err)
	_, err = inferenceHelper.FinishInference()
	require.NoError(t, err)
	_, err = inferenceHelper.MessageServer.Validation(ctx, &types.MsgValidation{
		InferenceId:  expected.InferenceId,
		Creator:      testutil.Validator,
		ValueDecimal: types.DecimalFromFloat(0.0),
	})
	require.NoError(t, err)

	blockHash := []byte(revalidationTestBlockHash)
	kPtr := inferenceHelper.keeper
	kPtr.SetNormalizedParticipantsForCommittedBlock(ctx, revalidationTestBlockHeight, blockHash)
	kPtr.SetBlockRevalidationEventsProvider(&mockRevalidationEventsProvider{
		eventsByHeight: map[int64][]keeper.RevalidationEventInfo{
			revalidationTestBlockHeight: {{InferenceId: expected.InferenceId, Validator: testutil.Validator}},
		},
	})
	kPtr.ProcessPendingRevalidationEvents(ctx, revalidationTestBlockHeight, blockHash)

	require.True(t, k.IsParticipantEligibleToVoteOnRevalidation(revalidationTestBlockHeight, expected.InferenceId, testutil.Validator))
	weight, ok := k.GetRevalidationVoteWeight(revalidationTestBlockHeight, expected.InferenceId, testutil.Validator)
	require.True(t, ok)
	require.Greater(t, weight, int64(0))
	// ≤2 participants: no hard cap; addMembersToGroupData has Validator and Requester with weight 100 each.
	require.Equal(t, int64(100), weight, "with 2 participants there is no cap; weight stays 100")
	require.True(t, k.IsRevalidationVoteInKeeper(ctx, expected.InferenceId))
}

// TestRevalidationVote_HardCapByParticipantCount_ThresholdUsesCappedWeights asserts that (1) with 3
// participants the 49% cap applies (here 100 each so no one is capped), and (2) the revalidation
// threshold is computed from capped weights (half of total capped).
func TestRevalidationVote_HardCapByParticipantCount_ThresholdUsesCappedWeights(t *testing.T) {
	inferenceHelper, k, ctx := NewMockInferenceHelper(t)
	createParticipants(t, inferenceHelper.MessageServer, ctx)
	model := &types.Model{Id: MODEL_ID, ValidationThreshold: &types.Decimal{Value: 85, Exponent: -2}}
	k.SetModel(ctx, model)
	StubModelSubgroup(t, ctx, k, inferenceHelper.Mocks, model)
	addMembersToGroupData(k, ctx)
	groupData, _ := k.GetEpochGroupData(ctx, 0, MODEL_ID)
	groupData.ValidationWeights = append(groupData.ValidationWeights, &types.ValidationWeight{
		MemberAddress: testutil.Creator, Weight: 100, Reputation: 100, ConfirmationWeight: 100,
	})
	groupData.TotalWeight += 100
	k.SetEpochGroupData(ctx, groupData)
	// 3 participants -> 49% cap. Total 300, 49% = 147; each weight 100 < 147 so uncapped. Total capped = 300; half = 150.

	expected, err := inferenceHelper.StartInference("promptPayload", model.Id, time.Now().UnixNano(), calculations.DefaultMaxTokens)
	require.NoError(t, err)
	_, err = inferenceHelper.FinishInference()
	require.NoError(t, err)
	ctx = inferenceHelper.context
	ctx = ctx.WithBlockHeight(revalidationTestBlockHeight)
	_, err = inferenceHelper.MessageServer.Validation(ctx, &types.MsgValidation{
		InferenceId:  expected.InferenceId,
		Creator:      testutil.Validator,
		ValueDecimal: types.DecimalFromFloat(0.0),
	})
	require.NoError(t, err)

	blockHash := []byte(revalidationTestBlockHash)
	kPtr := inferenceHelper.keeper
	kPtr.SetNormalizedParticipantsForCommittedBlock(ctx, revalidationTestBlockHeight, blockHash)
	kPtr.SetBlockRevalidationEventsProvider(&mockRevalidationEventsProvider{
		eventsByHeight: map[int64][]keeper.RevalidationEventInfo{
			revalidationTestBlockHeight: {{InferenceId: expected.InferenceId, Validator: testutil.Validator}},
		},
	})
	kPtr.ProcessPendingRevalidationEvents(ctx, revalidationTestBlockHeight, blockHash)

	// (1) 3 participants: 49% cap = 147; each has 100 so uncapped.
	for _, addr := range []string{testutil.Validator, testutil.Requester, testutil.Creator} {
		w, ok := k.GetRevalidationVoteWeight(revalidationTestBlockHeight, expected.InferenceId, addr)
		require.True(t, ok, "eligible %s", addr)
		require.Equal(t, int64(100), w, "49%% cap is 147; weight 100 is uncapped")
	}

	// (2) Threshold half of 300 = 150. One pass (100) vs invalidator (100) -> no side >= 150; two pass (200) -> revalidate wins.
	_, err = inferenceHelper.MessageServer.Validation(ctx, &types.MsgValidation{
		InferenceId:  expected.InferenceId,
		Creator:      testutil.Requester,
		ValueDecimal: types.DecimalFromFloat(0.99),
		Revalidation: true,
	})
	require.NoError(t, err)
	inf, _ := k.GetInference(ctx, expected.InferenceId)
	require.Equal(t, types.InferenceStatus_VOTING, inf.Status, "one pass (100) vs invalidator (100); threshold 150 not reached")

	_, err = inferenceHelper.MessageServer.Validation(ctx, &types.MsgValidation{
		InferenceId:  expected.InferenceId,
		Creator:      testutil.Creator,
		ValueDecimal: types.DecimalFromFloat(0.99),
		Revalidation: true,
	})
	require.NoError(t, err)
	inf, _ = k.GetInference(ctx, expected.InferenceId)
	require.Equal(t, types.InferenceStatus_VALIDATED, inf.Status, "two pass (100+100=200) >= 150; revalidate wins")
}

// TestRevalidationVote_HardCap_TwoParticipants_NoCap asserts that with ≤2 participants no hard cap is applied.
func TestRevalidationVote_HardCap_TwoParticipants_NoCap(t *testing.T) {
	inferenceHelper, k, ctx := NewMockInferenceHelper(t)
	createParticipants(t, inferenceHelper.MessageServer, ctx)
	model := &types.Model{Id: MODEL_ID, ValidationThreshold: &types.Decimal{Value: 85, Exponent: -2}}
	k.SetModel(ctx, model)
	StubModelSubgroup(t, ctx, k, inferenceHelper.Mocks, model)
	addMembersToGroupData(k, ctx) // Validator 100, Requester 100 -> 2 participants

	expected, err := inferenceHelper.StartInference("promptPayload", model.Id, time.Now().UnixNano(), calculations.DefaultMaxTokens)
	require.NoError(t, err)
	_, err = inferenceHelper.FinishInference()
	require.NoError(t, err)
	ctx = inferenceHelper.context
	ctx = ctx.WithBlockHeight(revalidationTestBlockHeight)
	_, err = inferenceHelper.MessageServer.Validation(ctx, &types.MsgValidation{
		InferenceId: expected.InferenceId, Creator: testutil.Validator, ValueDecimal: types.DecimalFromFloat(0.0),
	})
	require.NoError(t, err)

	blockHash := []byte(revalidationTestBlockHash)
	kPtr := inferenceHelper.keeper
	kPtr.SetNormalizedParticipantsForCommittedBlock(ctx, revalidationTestBlockHeight, blockHash)
	kPtr.SetBlockRevalidationEventsProvider(&mockRevalidationEventsProvider{
		eventsByHeight: map[int64][]keeper.RevalidationEventInfo{
			revalidationTestBlockHeight: {{InferenceId: expected.InferenceId, Validator: testutil.Validator}},
		},
	})
	kPtr.ProcessPendingRevalidationEvents(ctx, revalidationTestBlockHeight, blockHash)

	// No cap: each keeps raw weight 100.
	for _, addr := range []string{testutil.Validator, testutil.Requester} {
		w, ok := k.GetRevalidationVoteWeight(revalidationTestBlockHeight, expected.InferenceId, addr)
		require.True(t, ok, addr)
		require.Equal(t, int64(100), w, "≤2 participants: no cap")
	}
}

// TestRevalidationVote_HardCap_ThreeParticipants_49PercentCap asserts that with 3 participants the 49% cap is applied.
func TestRevalidationVote_HardCap_ThreeParticipants_49PercentCap(t *testing.T) {
	inferenceHelper, k, ctx := NewMockInferenceHelper(t)
	createParticipants(t, inferenceHelper.MessageServer, ctx)
	// Pin effective epoch to 0 so subgroup (0, MODEL_ID) is used by addMembersToGroupDataWithWeights and SetNormalizedParticipantsForCommittedBlock.
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 0))
	model := &types.Model{Id: MODEL_ID, ValidationThreshold: &types.Decimal{Value: 85, Exponent: -2}}
	k.SetModel(ctx, model)
	StubModelSubgroup(t, ctx, k, inferenceHelper.Mocks, model)
	// Cap is applied to ConfirmationWeight. Validator ConfirmationWeight 200, Requester 100, Creator 100 -> total 400; 49% = 196.
	addMembersToGroupDataWithWeights(k, ctx, 200, 100, 200, 100)
	groupData, found := k.GetEpochGroupData(ctx, 0, MODEL_ID)
	require.True(t, found, "subgroup must exist after StubModelSubgroup")
	groupData.EpochIndex = 0
	groupData.ModelId = MODEL_ID
	groupData.ValidationWeights = append(groupData.ValidationWeights, &types.ValidationWeight{
		MemberAddress: testutil.Creator, Weight: 100, Reputation: 100, ConfirmationWeight: 100,
	})
	groupData.TotalWeight += 100
	k.SetEpochGroupData(ctx, groupData)

	// Use a dedicated context for revalidation steps so epoch group data is isolated from the message
	// execution context (as it is in separate tx with separate EpochGroupData tx draft). GetEpochGroupData in ProcessPendingRevalidationEvents then
	// sees the same store/cache state as SetNormalizedParticipantsForCommittedBlock.
	revalidationCtx := ctx.WithBlockHeight(revalidationTestBlockHeight)

	expected, err := inferenceHelper.StartInference("promptPayload", model.Id, time.Now().UnixNano(), calculations.DefaultMaxTokens)
	require.NoError(t, err)
	_, err = inferenceHelper.FinishInference()
	require.NoError(t, err)
	ctx = inferenceHelper.context
	ctx = ctx.WithBlockHeight(revalidationTestBlockHeight)
	_, err = inferenceHelper.MessageServer.Validation(ctx, &types.MsgValidation{
		InferenceId: expected.InferenceId, Creator: testutil.Validator, ValueDecimal: types.DecimalFromFloat(0.0),
	})
	require.NoError(t, err)

	blockHash := []byte(revalidationTestBlockHash)
	kPtr := inferenceHelper.keeper
	// Invalidate epoch group cache so both SetNormalizedParticipantsForCommittedBlock and
	// ProcessPendingRevalidationEvents read subgroup from store (our 3-member setup).
	kPtr.InvalidateEpochGroupCache()
	kPtr.SetNormalizedParticipantsForCommittedBlock(revalidationCtx, revalidationTestBlockHeight, blockHash)

	// Assert normalized tree is filled from our 3-member subgroup before voting.
	tree, treeOk := k.GetNormalizedWeightedParticipants(blockHash, MODEL_ID)
	require.True(t, treeOk, "normalized tree for MODEL_ID must exist after SetNormalizedParticipantsForCommittedBlock")
	require.NotNil(t, tree)
	require.Equal(t, 3, tree.Len(), "tree must have 3 entries (Validator, Requester, Creator)")
	sampled := k.SampleNormalizedParticipantsForInference(blockHash, MODEL_ID, expected.InferenceId)
	require.Len(t, sampled, 3, "sample must return all 3 participants when n <= sample size")

	kPtr.SetBlockRevalidationEventsProvider(&mockRevalidationEventsProvider{
		eventsByHeight: map[int64][]keeper.RevalidationEventInfo{
			revalidationTestBlockHeight: {{InferenceId: expected.InferenceId, Validator: testutil.Validator}},
		},
	})
	kPtr.ProcessPendingRevalidationEvents(revalidationCtx, revalidationTestBlockHeight, blockHash)

	// 3 participants -> 49% cap = 196. Validator (ConfirmationWeight 200) capped to 196; Requester and Creator stay 100.
	validatorW, ok := k.GetRevalidationVoteWeight(revalidationTestBlockHeight, expected.InferenceId, testutil.Validator)
	require.True(t, ok)
	require.Equal(t, int64(196), validatorW, "49%% cap of 400 = 196")
	for _, addr := range []string{testutil.Requester, testutil.Creator} {
		w, ok := k.GetRevalidationVoteWeight(revalidationTestBlockHeight, expected.InferenceId, addr)
		require.True(t, ok, addr)
		require.Equal(t, int64(100), w, "weight below cap unchanged")
	}
}

// TestRevalidationVote_HardCap_FiveParticipants_24PercentCap asserts that with 5+ participants the 24% cap is applied.
func TestRevalidationVote_HardCap_FiveParticipants_24PercentCap(t *testing.T) {
	inferenceHelper, k, ctx := NewMockInferenceHelper(t)
	createParticipants(t, inferenceHelper.MessageServer, ctx)
	MustAddParticipant(t, inferenceHelper.MessageServer, ctx, *NewMockAccount(testutil.Executor2))
	// Pin effective epoch to 0 so subgroup (0, MODEL_ID) is used consistently.
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 0))
	model := &types.Model{Id: MODEL_ID, ValidationThreshold: &types.Decimal{Value: 85, Exponent: -2}}
	k.SetModel(ctx, model)
	StubModelSubgroup(t, ctx, k, inferenceHelper.Mocks, model)
	// Cap is applied to ConfirmationWeight. Validator 200, others 100 each -> total 600; 24% = 144.
	addMembersToGroupDataWithWeights(k, ctx, 200, 100, 200, 100)
	groupData, found := k.GetEpochGroupData(ctx, 0, MODEL_ID)
	require.True(t, found, "subgroup must exist after StubModelSubgroup")
	groupData.EpochIndex = 0
	groupData.ModelId = MODEL_ID
	groupData.ValidationWeights = append(groupData.ValidationWeights,
		&types.ValidationWeight{MemberAddress: testutil.Creator, Weight: 100, Reputation: 100, ConfirmationWeight: 100},
		&types.ValidationWeight{MemberAddress: testutil.Executor, Weight: 100, Reputation: 100, ConfirmationWeight: 100},
		&types.ValidationWeight{MemberAddress: testutil.Executor2, Weight: 100, Reputation: 100, ConfirmationWeight: 100},
	)
	groupData.TotalWeight += 300
	k.SetEpochGroupData(ctx, groupData)

	// Use a dedicated context for revalidation steps so epoch group data is isolated from the message
	// execution context (no tx draft). GetEpochGroupData in ProcessPendingRevalidationEvents then
	// sees the same store/cache state as SetNormalizedParticipantsForCommittedBlock.
	revalidationCtx := ctx.WithBlockHeight(revalidationTestBlockHeight)

	expected, err := inferenceHelper.StartInference("promptPayload", model.Id, time.Now().UnixNano(), calculations.DefaultMaxTokens)
	require.NoError(t, err)
	_, err = inferenceHelper.FinishInference()
	require.NoError(t, err)
	ctx = inferenceHelper.context
	ctx = ctx.WithBlockHeight(revalidationTestBlockHeight)
	_, err = inferenceHelper.MessageServer.Validation(ctx, &types.MsgValidation{
		InferenceId: expected.InferenceId, Creator: testutil.Validator, ValueDecimal: types.DecimalFromFloat(0.0),
	})
	require.NoError(t, err)

	blockHash := []byte(revalidationTestBlockHash)
	kPtr := inferenceHelper.keeper
	// Invalidate epoch group cache so SetNormalizedParticipantsForCommittedBlock reads subgroup from store (our 5-member setup).
	kPtr.InvalidateEpochGroupCache()
	kPtr.SetNormalizedParticipantsForCommittedBlock(revalidationCtx, revalidationTestBlockHeight, blockHash)

	// Assert normalized tree is filled from our 5-member subgroup before voting.
	tree, treeOk := k.GetNormalizedWeightedParticipants(blockHash, MODEL_ID)
	require.True(t, treeOk, "normalized tree for MODEL_ID must exist after SetNormalizedParticipantsForCommittedBlock")
	require.NotNil(t, tree)
	require.Equal(t, 5, tree.Len(), "tree must have 5 entries")
	sampled := k.SampleNormalizedParticipantsForInference(blockHash, MODEL_ID, expected.InferenceId)
	require.Len(t, sampled, 5, "sample must return all 5 participants when n <= sample size")

	kPtr.SetBlockRevalidationEventsProvider(&mockRevalidationEventsProvider{
		eventsByHeight: map[int64][]keeper.RevalidationEventInfo{
			revalidationTestBlockHeight: {{InferenceId: expected.InferenceId, Validator: testutil.Validator}},
		},
	})
	kPtr.ProcessPendingRevalidationEvents(revalidationCtx, revalidationTestBlockHeight, blockHash)

	// 5 participants -> 24% cap = 144. Validator (ConfirmationWeight 200) capped to 144; others stay 100.
	validatorW, ok := k.GetRevalidationVoteWeight(revalidationTestBlockHeight, expected.InferenceId, testutil.Validator)
	require.True(t, ok)
	require.Equal(t, int64(144), validatorW, "24%% cap of 600 = 144")
	for _, addr := range []string{testutil.Requester, testutil.Creator, testutil.Executor, testutil.Executor2} {
		w, ok := k.GetRevalidationVoteWeight(revalidationTestBlockHeight, expected.InferenceId, addr)
		require.True(t, ok, addr)
		require.Equal(t, int64(100), w, "weight below cap unchanged")
	}
}

// TestRevalidationVote_EligibleCanVote_IneligibleCannot asserts that only participants in the
// revalidation vote cache can submit a revalidation vote; others get ErrNotDesignatedValidator.
func TestRevalidationVote_EligibleCanVote_IneligibleCannot(t *testing.T) {
	inferenceHelper, k, inf := setupInferenceInVoting(t)
	ctx := inferenceHelper.context
	inferenceId := inf.InferenceId
	blockHeight := ctx.BlockHeight()
	k.AddRevalidationEligibleParticipantForTest(blockHeight, inferenceId, testutil.Validator, testutil.Requester, 100)
	// Start ephemeral session: totalCapped 300 so invalidator (100) alone does not reach half (150)
	kPtr := inferenceHelper.keeper
	kPtr.StartRevalidationVote(ctx, inferenceId, testutil.Validator, 100, 300, blockHeight)

	// Requester is eligible -> vote accepted
	_, err := inferenceHelper.MessageServer.Validation(ctx, &types.MsgValidation{
		InferenceId:  inferenceId,
		Creator:      testutil.Requester,
		ValueDecimal: types.DecimalFromFloat(0.99),
		Revalidation: true,
	})
	require.NoError(t, err)

	// Creator (transfer agent) is not in the eligible set -> must fail
	_, err = inferenceHelper.MessageServer.Validation(ctx, &types.MsgValidation{
		InferenceId:  inferenceId,
		Creator:      testutil.Creator,
		ValueDecimal: types.DecimalFromFloat(0.99),
		Revalidation: true,
	})
	require.Error(t, err)
	require.ErrorIs(t, err, types.ErrNotDesignatedValidator)
}

// TestRevalidationVote_ThresholdReached_RevalidateWins uses the full flow (normalized participants +
// ProcessPendingRevalidationEvents); with 3 participants the 49% cap applies (here weights stay 100).
// Two eligible participants vote pass and the inference transitions to VALIDATED.
func TestRevalidationVote_ThresholdReached_RevalidateWins(t *testing.T) {
	inferenceHelper, k, ctx := NewMockInferenceHelper(t)
	createParticipants(t, inferenceHelper.MessageServer, ctx)
	model := &types.Model{Id: MODEL_ID, ValidationThreshold: &types.Decimal{Value: 85, Exponent: -2}}
	k.SetModel(ctx, model)
	StubModelSubgroup(t, ctx, k, inferenceHelper.Mocks, model)
	addMembersToGroupData(k, ctx)
	// Add Creator so we have 3 participants: 2 non-invalidator eligible voters can push passTotal > noPassTotal.
	groupData, _ := k.GetEpochGroupData(ctx, 0, MODEL_ID)
	groupData.ValidationWeights = append(groupData.ValidationWeights, &types.ValidationWeight{
		MemberAddress: testutil.Creator, Weight: 100, Reputation: 100, ConfirmationWeight: 100,
	})
	groupData.TotalWeight += 100
	k.SetEpochGroupData(ctx, groupData)

	expected, err := inferenceHelper.StartInference("promptPayload", model.Id, time.Now().UnixNano(), calculations.DefaultMaxTokens)
	require.NoError(t, err)
	_, err = inferenceHelper.FinishInference()
	require.NoError(t, err)
	ctx = inferenceHelper.context
	ctx = ctx.WithBlockHeight(revalidationTestBlockHeight)
	_, err = inferenceHelper.MessageServer.Validation(ctx, &types.MsgValidation{
		InferenceId:  expected.InferenceId,
		Creator:      testutil.Validator,
		ValueDecimal: types.DecimalFromFloat(0.0),
	})
	require.NoError(t, err)

	blockHash := []byte(revalidationTestBlockHash)
	kPtr := inferenceHelper.keeper
	kPtr.SetNormalizedParticipantsForCommittedBlock(ctx, revalidationTestBlockHeight, blockHash)
	kPtr.SetBlockRevalidationEventsProvider(&mockRevalidationEventsProvider{
		eventsByHeight: map[int64][]keeper.RevalidationEventInfo{
			revalidationTestBlockHeight: {{InferenceId: expected.InferenceId, Validator: testutil.Validator}},
		},
	})
	kPtr.ProcessPendingRevalidationEvents(ctx, revalidationTestBlockHeight, blockHash)

	tree, ok := k.GetNormalizedWeightedParticipants(blockHash, MODEL_ID)
	require.True(t, ok)
	require.NotNil(t, tree)
	// Collect eligible participants (invalidator + sampled). Sampled from SampleNormalizedParticipantsForInference.
	sampled := k.SampleNormalizedParticipantsForInference(blockHash, MODEL_ID, expected.InferenceId)
	eligible := make(map[string]struct{})
	eligible[testutil.Validator] = struct{}{}
	for _, a := range sampled {
		eligible[a] = struct{}{}
	}
	// Vote pass with every eligible participant except the invalidator (Validator) until threshold.
	// Invalidator already voted no. We need passTotal >= half of capped total.
	for addr := range eligible {
		if addr == testutil.Validator {
			continue
		}
		w, okW := k.GetRevalidationVoteWeight(revalidationTestBlockHeight, expected.InferenceId, addr)
		if !okW || w <= 0 {
			continue
		}
		_, err = inferenceHelper.MessageServer.Validation(ctx, &types.MsgValidation{
			InferenceId:  expected.InferenceId,
			Creator:      addr,
			ValueDecimal: types.DecimalFromFloat(0.99),
			Revalidation: true,
		})
		require.NoError(t, err)
		inf, found := k.GetInference(ctx, expected.InferenceId)
		require.True(t, found)
		if inf.Status == types.InferenceStatus_VALIDATED {
			return
		}
	}
	inf, found := k.GetInference(ctx, expected.InferenceId)
	require.True(t, found)
	require.Equal(t, types.InferenceStatus_VALIDATED, inf.Status, "revalidation should have reached threshold and set VALIDATED")
}

// TestSampleNormalizedParticipants_Deterministic asserts that sampling with the same (blockHash, modelId, inferenceId) returns the same list.
func TestSampleNormalizedParticipants_Deterministic(t *testing.T) {
	inferenceHelper, k, ctx := NewMockInferenceHelper(t)
	createParticipants(t, inferenceHelper.MessageServer, ctx)
	model := &types.Model{Id: MODEL_ID, ValidationThreshold: &types.Decimal{Value: 85, Exponent: -2}}
	k.SetModel(ctx, model)
	StubModelSubgroup(t, ctx, k, inferenceHelper.Mocks, model)
	addMembersToGroupData(k, ctx)

	blockHash := []byte(revalidationTestBlockHash)
	kPtr := inferenceHelper.keeper
	kPtr.SetNormalizedParticipantsForCommittedBlock(ctx, revalidationTestBlockHeight, blockHash)

	inferenceId := "fixed-inference-id"
	a := k.SampleNormalizedParticipantsForInference(blockHash, MODEL_ID, inferenceId)
	b := k.SampleNormalizedParticipantsForInference(blockHash, MODEL_ID, inferenceId)
	require.Equal(t, a, b)
}

// TestNormalizedParticipants_CumulativeWeightsSumToOne asserts the normalized tree keys are cumulative and the last key is <= 1.
func TestNormalizedParticipants_CumulativeWeightsSumToOne(t *testing.T) {
	inferenceHelper, k, ctx := NewMockInferenceHelper(t)
	createParticipants(t, inferenceHelper.MessageServer, ctx)
	model := &types.Model{Id: MODEL_ID, ValidationThreshold: &types.Decimal{Value: 85, Exponent: -2}}
	k.SetModel(ctx, model)
	StubModelSubgroup(t, ctx, k, inferenceHelper.Mocks, model)
	addMembersToGroupData(k, ctx)

	blockHash := []byte(revalidationTestBlockHash)
	kPtr := inferenceHelper.keeper
	kPtr.SetNormalizedParticipantsForCommittedBlock(ctx, revalidationTestBlockHeight, blockHash)

	tree, ok := k.GetNormalizedWeightedParticipants(blockHash, MODEL_ID)
	require.True(t, ok)
	require.NotNil(t, tree)
	var keys []float64
	tree.Scan(func(cum float64, _ string) bool {
		keys = append(keys, cum)
		return true
	})
	require.NotEmpty(t, keys)
	require.GreaterOrEqual(t, keys[len(keys)-1], 0.99)
	require.LessOrEqual(t, keys[len(keys)-1], 1.0+1e-9)
	for i := 1; i < len(keys); i++ {
		require.Greater(t, keys[i], keys[i-1])
	}
}

// TestRevalidationCapLimit_ByParticipantCount tests the cap limit formula: ≤2 no cap, 3–4 @ 49%%, 5+ @ 24%%.
func TestRevalidationCapLimit_ByParticipantCount(t *testing.T) {
	total := int64(400)
	// ≤2: no cap -> limit = total (so no one is capped)
	require.Equal(t, total, keeper.RevalidationCapLimitForParticipantCount(1, total))
	require.Equal(t, total, keeper.RevalidationCapLimitForParticipantCount(2, total))
	// 3–4: 49%%
	require.Equal(t, int64(196), keeper.RevalidationCapLimitForParticipantCount(3, total))
	require.Equal(t, int64(196), keeper.RevalidationCapLimitForParticipantCount(4, total))
	// 5+: 24%%
	require.Equal(t, int64(96), keeper.RevalidationCapLimitForParticipantCount(5, total))
	require.Equal(t, int64(96), keeper.RevalidationCapLimitForParticipantCount(10, total))
	// 600 total, 5 participants -> 24%% = 144
	require.Equal(t, int64(144), keeper.RevalidationCapLimitForParticipantCount(5, 600))
}

// TestApplyRevalidationCap tests that weights above cap are reduced, others unchanged.
func TestApplyRevalidationCap(t *testing.T) {
	selected := map[string]int64{"a": 200, "b": 100, "c": 100}
	capLimit := int64(196)
	capped := keeper.ApplyRevalidationCap(selected, capLimit)
	require.Equal(t, int64(196), capped["a"])
	require.Equal(t, int64(100), capped["b"])
	require.Equal(t, int64(100), capped["c"])
	var total int64
	for _, w := range capped {
		total += w
	}
	require.Equal(t, int64(396), total)
}
