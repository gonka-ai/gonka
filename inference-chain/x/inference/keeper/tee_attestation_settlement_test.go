package keeper_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func snapshotFor(stage int64, total int64, powers map[string]int64) types.PoCValidationSnapshot {
	entries := make([]*types.VotingPowerEntry, 0, len(powers))
	for addr, p := range powers {
		entries = append(entries, &types.VotingPowerEntry{Address: addr, VotingPower: p})
	}
	return types.PoCValidationSnapshot{
		PocStageStartHeight: stage,
		SnapshotHeight:      stage + 10,
		TotalNetworkWeight:  total,
		ModelVotingPowers: []*types.ModelVotingPowers{{
			ModelId:      "m1",
			VotingPowers: entries,
		}},
	}
}

func recordVote(t *testing.T, k keeper.Keeper, ctx sdk.Context, stage int64, subjectAddr string, nodeID string, validatorAddr string, verdict types.TeeVerdict) {
	t.Helper()
	require.NoError(t, k.SetTeeValidation(ctx, types.TeeValidation{
		PocStageStartBlockHeight: stage,
		SubjectAddress:           subjectAddr,
		NodeId:                   nodeID,
		ValidatorAddress:         validatorAddr,
		Verdict:                  verdict,
	}))
}

func enableValidationOnly(t *testing.T, k keeper.Keeper, ctx sdk.Context, thresholdBps uint32) {
	t.Helper()
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.TeeAttestationParams = types.DefaultTeeAttestationParams()
	if thresholdBps != 0 {
		params.TeeAttestationParams.ValidationThresholdBps = thresholdBps
	}
	require.NoError(t, k.SetParams(ctx, params))
}

func TestSettleTeeAttestations_VerifiedAboveThreshold(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(200)
	enableValidationOnly(t, k, sdkCtx, types.DefaultTeeValidationThresholdBps)

	stage := int64(100)
	newTestSubject(t, k, sdkCtx, testutil.Executor, "node-1", stage)

	require.NoError(t, k.SetPoCValidationSnapshot(sdkCtx, snapshotFor(stage, 300, map[string]int64{
		testutil.Validator:  200,
		testutil.Validator2: 100,
	})))

	recordVote(t, k, sdkCtx, stage, testutil.Executor, "node-1", testutil.Validator, types.TeeVerdict_TEE_VERDICT_OK)
	recordVote(t, k, sdkCtx, stage, testutil.Executor, "node-1", testutil.Validator2, types.TeeVerdict_TEE_VERDICT_OK)

	require.NoError(t, k.SettleTeeAttestations(sdkCtx, stage))

	subj, err := sdk.AccAddressFromBech32(testutil.Executor)
	require.NoError(t, err)
	att, found, err := k.GetTeeAttestation(sdkCtx, subj, "node-1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, types.TeeVerificationStatus_TEE_VERIFICATION_VERIFIED, att.VerificationStatus)
	require.Equal(t, int64(200), sdkCtx.BlockHeight())
	require.Equal(t, int64(200), att.VerifiedAtHeight)
	require.Equal(t, uint32(10000), att.OkVotingPowerBps)
	require.Equal(t, uint32(0), att.RejectVotingPowerBps)
	require.Equal(t, uint32(0), att.PendingEpochs)

	tally, err := k.TallyTeeValidationsForStage(sdkCtx, stage)
	require.NoError(t, err)
	require.Empty(t, tally)
}

func TestSettleTeeAttestations_RejectedAboveThreshold(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(200)
	enableValidationOnly(t, k, sdkCtx, types.DefaultTeeValidationThresholdBps)

	stage := int64(100)
	newTestSubject(t, k, sdkCtx, testutil.Executor, "node-1", stage)
	require.NoError(t, k.SetPoCValidationSnapshot(sdkCtx, snapshotFor(stage, 300, map[string]int64{
		testutil.Validator:  200,
		testutil.Validator2: 100,
	})))

	recordVote(t, k, sdkCtx, stage, testutil.Executor, "node-1", testutil.Validator, types.TeeVerdict_TEE_VERDICT_REJECT)
	recordVote(t, k, sdkCtx, stage, testutil.Executor, "node-1", testutil.Validator2, types.TeeVerdict_TEE_VERDICT_REJECT)

	require.NoError(t, k.SettleTeeAttestations(sdkCtx, stage))

	subj, _ := sdk.AccAddressFromBech32(testutil.Executor)
	att, _, _ := k.GetTeeAttestation(sdkCtx, subj, "node-1")
	require.Equal(t, types.TeeVerificationStatus_TEE_VERIFICATION_REJECTED, att.VerificationStatus)
	require.Equal(t, uint32(10000), att.RejectVotingPowerBps)
}

func TestSettleTeeAttestations_RejectsBelowThreshold(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(200)
	enableValidationOnly(t, k, sdkCtx, types.DefaultTeeValidationThresholdBps)

	stage := int64(100)
	newTestSubject(t, k, sdkCtx, testutil.Executor, "node-1", stage)
	require.NoError(t, k.SetPoCValidationSnapshot(sdkCtx, snapshotFor(stage, 300, map[string]int64{
		testutil.Validator:  150,
		testutil.Validator2: 150,
	})))
	recordVote(t, k, sdkCtx, stage, testutil.Executor, "node-1", testutil.Validator, types.TeeVerdict_TEE_VERDICT_OK)

	require.NoError(t, k.SettleTeeAttestations(sdkCtx, stage))

	subj, _ := sdk.AccAddressFromBech32(testutil.Executor)
	att, _, _ := k.GetTeeAttestation(sdkCtx, subj, "node-1")
	require.Equal(t, types.TeeVerificationStatus_TEE_VERIFICATION_REJECTED, att.VerificationStatus)
	require.Equal(t, uint32(0), att.PendingEpochs)
}

func TestSettleTeeAttestations_NoRolloverImmediateReject(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(200)
	enableValidationOnly(t, k, sdkCtx, types.DefaultTeeValidationThresholdBps)

	stage := int64(100)
	newTestSubject(t, k, sdkCtx, testutil.Executor, "node-1", stage)
	require.NoError(t, k.SetPoCValidationSnapshot(sdkCtx, snapshotFor(stage, 300, map[string]int64{
		testutil.Validator:  150,
		testutil.Validator2: 150,
	})))

	subj, _ := sdk.AccAddressFromBech32(testutil.Executor)

	recordVote(t, k, sdkCtx, stage, testutil.Executor, "node-1", testutil.Validator, types.TeeVerdict_TEE_VERDICT_OK)
	require.NoError(t, k.SettleTeeAttestations(sdkCtx, stage))
	att, _, _ := k.GetTeeAttestation(sdkCtx, subj, "node-1")
	require.Equal(t, types.TeeVerificationStatus_TEE_VERIFICATION_REJECTED, att.VerificationStatus)
	require.Equal(t, uint32(0), att.PendingEpochs)
}

func TestSettleTeeAttestations_DisabledNoop(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(200)
	params, err := k.GetParams(sdkCtx)
	require.NoError(t, err)
	params.TeeAttestationParams = nil
	require.NoError(t, k.SetParams(sdkCtx, params))

	stage := int64(100)
	newTestSubject(t, k, sdkCtx, testutil.Executor, "node-1", stage)
	recordVote(t, k, sdkCtx, stage, testutil.Executor, "node-1", testutil.Validator, types.TeeVerdict_TEE_VERDICT_OK)

	require.NoError(t, k.SettleTeeAttestations(sdkCtx, stage))

	subj, _ := sdk.AccAddressFromBech32(testutil.Executor)
	att, _, _ := k.GetTeeAttestation(sdkCtx, subj, "node-1")
	require.Equal(t, types.TeeVerificationStatus_TEE_VERIFICATION_PENDING, att.VerificationStatus)
}

func TestSettleTeeAttestations_DropsQuoteBytesOnVerified(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(200)
	enableValidationOnly(t, k, sdkCtx, types.DefaultTeeValidationThresholdBps)

	stage := int64(100)
	subj, _ := sdk.AccAddressFromBech32(testutil.Executor)
	att := types.TeeAttestation{
		ParticipantAddress: testutil.Executor,
		NodeId:             "node-1",
		PublicKey:          makeTestPublicKey(0x42),
		Provider:           "intel-tdx-lite",
		EnvelopeVersion:    "v1",
		AttestedAtHeight:   stage,
		ExpiresAtHeight:    stage + 1000,
		Quote:              []byte("attested-quote-bytes"),
	}
	require.NoError(t, k.SetTeeAttestation(sdkCtx, att))

	require.NoError(t, k.SetPoCValidationSnapshot(sdkCtx, snapshotFor(stage, 300, map[string]int64{
		testutil.Validator: 300,
	})))
	recordVote(t, k, sdkCtx, stage, testutil.Executor, "node-1", testutil.Validator, types.TeeVerdict_TEE_VERDICT_OK)

	require.NoError(t, k.SettleTeeAttestations(sdkCtx, stage))
	got, _, _ := k.GetTeeAttestation(sdkCtx, subj, "node-1")
	require.Equal(t, types.TeeVerificationStatus_TEE_VERIFICATION_VERIFIED, got.VerificationStatus)
	require.Nil(t, got.Quote)
}

func TestSettleTeeAttestations_DoesNotDowngradeVerifiedAttestation(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(200)
	enableValidationOnly(t, k, sdkCtx, types.DefaultTeeValidationThresholdBps)

	stage := int64(100)
	subj, _ := sdk.AccAddressFromBech32(testutil.Executor)
	att := types.TeeAttestation{
		ParticipantAddress: testutil.Executor,
		NodeId:             "node-1",
		PublicKey:          makeTestPublicKey(0x42),
		Provider:           "intel-tdx-lite",
		EnvelopeVersion:    "v1",
		AttestedAtHeight:   stage,
		ExpiresAtHeight:    stage + 1000,
		VerificationStatus: types.TeeVerificationStatus_TEE_VERIFICATION_VERIFIED,
		VerifiedAtHeight:   150,
	}
	require.NoError(t, k.SetTeeAttestation(sdkCtx, att))

	require.NoError(t, k.SetPoCValidationSnapshot(sdkCtx, snapshotFor(stage, 300, map[string]int64{
		testutil.Validator: 300,
	})))
	recordVote(t, k, sdkCtx, stage, testutil.Executor, "node-1", testutil.Validator, types.TeeVerdict_TEE_VERDICT_REJECT)

	require.NoError(t, k.SettleTeeAttestations(sdkCtx, stage))
	got, _, _ := k.GetTeeAttestation(sdkCtx, subj, "node-1")
	require.Equal(t, types.TeeVerificationStatus_TEE_VERIFICATION_VERIFIED, got.VerificationStatus)
	require.Equal(t, int64(150), got.VerifiedAtHeight)
	require.Equal(t, uint32(0), got.PendingEpochs)
}

func TestSettleTeeAttestations_IgnoresVotesFromUnknownValidators(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(200)
	enableValidationOnly(t, k, sdkCtx, types.DefaultTeeValidationThresholdBps)

	stage := int64(100)
	newTestSubject(t, k, sdkCtx, testutil.Executor, "node-1", stage)
	require.NoError(t, k.SetPoCValidationSnapshot(sdkCtx, snapshotFor(stage, 300, map[string]int64{
		testutil.Validator: 100,
	})))
	recordVote(t, k, sdkCtx, stage, testutil.Executor, "node-1", testutil.Validator2, types.TeeVerdict_TEE_VERDICT_OK)

	require.NoError(t, k.SettleTeeAttestations(sdkCtx, stage))
	subj, _ := sdk.AccAddressFromBech32(testutil.Executor)
	att, _, _ := k.GetTeeAttestation(sdkCtx, subj, "node-1")
	require.Equal(t, types.TeeVerificationStatus_TEE_VERIFICATION_REJECTED, att.VerificationStatus)
	require.Equal(t, uint32(0), att.PendingEpochs)
}

func TestSettleTeeAttestations_NoSnapshotRejectsPending(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(200)
	enableValidationOnly(t, k, sdkCtx, types.DefaultTeeValidationThresholdBps)

	stage := int64(100)
	newTestSubject(t, k, sdkCtx, testutil.Executor, "node-1", stage)

	require.NoError(t, k.SettleTeeAttestations(sdkCtx, stage))

	subj, _ := sdk.AccAddressFromBech32(testutil.Executor)
	att, _, _ := k.GetTeeAttestation(sdkCtx, subj, "node-1")
	require.Equal(t, types.TeeVerificationStatus_TEE_VERIFICATION_REJECTED, att.VerificationStatus)
	require.Equal(t, uint32(0), att.PendingEpochs)
}

func TestSettleTeeAttestations_SnapshotRejectsPendingWithoutVotes(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(200)
	enableValidationOnly(t, k, sdkCtx, types.DefaultTeeValidationThresholdBps)

	stage := int64(100)
	newTestSubject(t, k, sdkCtx, testutil.Executor, "node-1", stage)
	require.NoError(t, k.SetPoCValidationSnapshot(sdkCtx, snapshotFor(stage, 300, map[string]int64{
		testutil.Validator: 300,
	})))

	subj, _ := sdk.AccAddressFromBech32(testutil.Executor)

	require.NoError(t, k.SettleTeeAttestations(sdkCtx, stage))
	att, _, _ := k.GetTeeAttestation(sdkCtx, subj, "node-1")
	require.Equal(t, types.TeeVerificationStatus_TEE_VERIFICATION_REJECTED, att.VerificationStatus)
	require.Equal(t, uint32(0), att.PendingEpochs)

	require.NoError(t, k.SettleTeeAttestations(sdkCtx, stage))
	att, _, _ = k.GetTeeAttestation(sdkCtx, subj, "node-1")
	require.Equal(t, types.TeeVerificationStatus_TEE_VERIFICATION_REJECTED, att.VerificationStatus)
	require.Equal(t, uint32(0), att.PendingEpochs)
}
