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

func newTestSubject(t *testing.T, k keeper.Keeper, ctx sdk.Context, participant, nodeID string, atHeight int64) sdk.AccAddress {
	t.Helper()
	addr, err := sdk.AccAddressFromBech32(participant)
	require.NoError(t, err)
	att := types.TeeAttestation{
		ParticipantAddress: participant,
		NodeId:             nodeID,
		PublicKey:          makeTestPublicKey(0x42),
		Provider:           "intel-tdx-lite",
		EnvelopeVersion:    "v1",
		Measurement:        "deadbeef",
		AttestedAtHeight:   atHeight,
		ExpiresAtHeight:    atHeight + 1000,
		VerificationStatus: types.TeeVerificationStatus_TEE_VERIFICATION_PENDING,
	}
	require.NoError(t, k.SetTeeAttestation(ctx, att))
	return addr
}

func enableTeeValidationParams(t *testing.T, k keeper.Keeper, ctx sdk.Context) {
	t.Helper()
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams = &types.PocParams{PocV2Enabled: true}
	params.EpochParams = &types.EpochParams{
		PocStageDuration:      50,
		PocExchangeDuration:   20,
		PocValidationDelay:    5,
		PocValidationDuration: 100,
	}
	params.TeeAttestationParams = types.DefaultTeeAttestationParams()
	require.NoError(t, k.SetParams(ctx, params))

	k.SetEffectiveEpochIndex(ctx, 0)
	k.SetEpoch(ctx, &types.Epoch{Index: 1, PocStartBlockHeight: 100})
}

func TestTeeValidation_SetAndTally(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(100)

	subject := newTestSubject(t, k, sdkCtx, testutil.Executor, "node-1", 100)
	validator, err := sdk.AccAddressFromBech32(testutil.Validator)
	require.NoError(t, err)

	require.NoError(t, k.SetTeeValidation(sdkCtx, types.TeeValidation{
		PocStageStartBlockHeight: 100,
		SubjectAddress:           testutil.Executor,
		NodeId:                   "node-1",
		ValidatorAddress:         testutil.Validator,
		Verdict:                  types.TeeVerdict_TEE_VERDICT_OK,
	}))

	got, found, err := k.GetTeeValidation(sdkCtx, 100, subject, "node-1", validator)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, types.TeeVerdict_TEE_VERDICT_OK, got.Verdict)

	tally, err := k.TallyTeeValidationsForStage(sdkCtx, 100)
	require.NoError(t, err)
	require.Len(t, tally, 1)
	require.Equal(t, "node-1", tally[0].NodeID)
	require.Len(t, tally[0].Votes, 1)
	require.Equal(t, testutil.Validator, tally[0].Votes[0].ValidatorAddress)

	removed, err := k.PruneTeeValidationsForStage(sdkCtx, 100)
	require.NoError(t, err)
	require.Equal(t, 1, removed)

	tally, err = k.TallyTeeValidationsForStage(sdkCtx, 100)
	require.NoError(t, err)
	require.Empty(t, tally)
	_, found, err = k.GetTeeValidation(sdkCtx, 100, subject, "node-1", validator)
	require.NoError(t, err)
	require.False(t, found)
}

func TestSubmitTeeValidations_StoresAndDedups(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(160)
	enableTeeValidationParams(t, k, sdkCtx)
	requireParticipant(t, k, sdkCtx, testutil.Validator)
	subject := newTestSubject(t, k, sdkCtx, testutil.Executor, "node-1", 100)
	validator, err := sdk.AccAddressFromBech32(testutil.Validator)
	require.NoError(t, err)

	srv := keeper.NewMsgServerImpl(k)
	msg := &types.MsgSubmitTeeValidations{
		Creator:                  testutil.Validator,
		PocStageStartBlockHeight: 100,
		Validations: []*types.TeeValidationEntry{{
			SubjectAddress:  testutil.Executor,
			NodeId:          "node-1",
			Verdict:         types.TeeVerdict_TEE_VERDICT_OK,
			MeasurementSeen: "0xDEADBEEF",
		}},
	}

	_, err = srv.SubmitTeeValidations(sdkCtx, msg)
	require.NoError(t, err)

	has, err := k.HasTeeValidation(sdkCtx, 100, subject, "node-1", validator)
	require.NoError(t, err)
	require.True(t, has)

	_, err = srv.SubmitTeeValidations(sdkCtx, msg)
	require.NoError(t, err)
	tally, err := k.TallyTeeValidationsForStage(sdkCtx, 100)
	require.NoError(t, err)
	require.Len(t, tally, 1)
	require.Equal(t, subject, tally[0].Subject)
	require.Len(t, tally[0].Votes, 1)
}

func TestSubmitTeeValidations_OutsideWindow(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(110)
	enableTeeValidationParams(t, k, sdkCtx)
	requireParticipant(t, k, sdkCtx, testutil.Validator)
	newTestSubject(t, k, sdkCtx, testutil.Executor, "node-1", 100)

	srv := keeper.NewMsgServerImpl(k)
	_, err := srv.SubmitTeeValidations(sdkCtx, &types.MsgSubmitTeeValidations{
		Creator:                  testutil.Validator,
		PocStageStartBlockHeight: 100,
		Validations: []*types.TeeValidationEntry{{
			SubjectAddress: testutil.Executor,
			NodeId:         "node-1",
			Verdict:        types.TeeVerdict_TEE_VERDICT_OK,
		}},
	})
	require.ErrorIs(t, err, types.ErrTeeValidationOutsideWindow)
}

func TestSubmitTeeValidations_RespectsMaxEntriesParam(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(160)
	enableTeeValidationParams(t, k, sdkCtx)
	requireParticipant(t, k, sdkCtx, testutil.Validator)
	newTestSubject(t, k, sdkCtx, testutil.Executor, "node-1", 100)
	newTestSubject(t, k, sdkCtx, testutil.Executor, "node-2", 100)

	params, err := k.GetParams(sdkCtx)
	require.NoError(t, err)
	params.TeeAttestationParams.MaxValidationEntriesPerMessage = 1
	require.NoError(t, k.SetParams(sdkCtx, params))

	srv := keeper.NewMsgServerImpl(k)
	_, err = srv.SubmitTeeValidations(sdkCtx, &types.MsgSubmitTeeValidations{
		Creator:                  testutil.Validator,
		PocStageStartBlockHeight: 100,
		Validations: []*types.TeeValidationEntry{
			{
				SubjectAddress: testutil.Executor,
				NodeId:         "node-1",
				Verdict:        types.TeeVerdict_TEE_VERDICT_OK,
			},
			{
				SubjectAddress: testutil.Executor,
				NodeId:         "node-2",
				Verdict:        types.TeeVerdict_TEE_VERDICT_OK,
			},
		},
	})
	require.ErrorIs(t, err, types.ErrIllegalState)
}

func TestSubmitTeeValidations_SkipsUnknownSubject(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(160)
	enableTeeValidationParams(t, k, sdkCtx)
	requireParticipant(t, k, sdkCtx, testutil.Validator)

	srv := keeper.NewMsgServerImpl(k)
	_, err := srv.SubmitTeeValidations(sdkCtx, &types.MsgSubmitTeeValidations{
		Creator:                  testutil.Validator,
		PocStageStartBlockHeight: 100,
		Validations: []*types.TeeValidationEntry{{
			SubjectAddress: testutil.Executor,
			NodeId:         "missing-node",
			Verdict:        types.TeeVerdict_TEE_VERDICT_OK,
		}},
	})
	require.NoError(t, err)

	tally, err := k.TallyTeeValidationsForStage(sdkCtx, 100)
	require.NoError(t, err)
	require.Empty(t, tally)
}

func TestSubmitTeeValidations_SkipsSelfVote(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(160)
	enableTeeValidationParams(t, k, sdkCtx)
	requireParticipant(t, k, sdkCtx, testutil.Validator)
	newTestSubject(t, k, sdkCtx, testutil.Validator, "node-self", 100)
	newTestSubject(t, k, sdkCtx, testutil.Executor, "node-peer", 100)

	srv := keeper.NewMsgServerImpl(k)
	_, err := srv.SubmitTeeValidations(sdkCtx, &types.MsgSubmitTeeValidations{
		Creator:                  testutil.Validator,
		PocStageStartBlockHeight: 100,
		Validations: []*types.TeeValidationEntry{
			{
				SubjectAddress: testutil.Validator,
				NodeId:         "node-self",
				Verdict:        types.TeeVerdict_TEE_VERDICT_OK,
			},
			{
				SubjectAddress:  testutil.Executor,
				NodeId:          "node-peer",
				Verdict:         types.TeeVerdict_TEE_VERDICT_OK,
				MeasurementSeen: "0xDEADBEEF",
			},
		},
	})
	require.NoError(t, err)

	tally, err := k.TallyTeeValidationsForStage(sdkCtx, 100)
	require.NoError(t, err)
	require.Len(t, tally, 1)
	require.Equal(t, testutil.Executor, tally[0].Subject.String())
	require.Equal(t, "node-peer", tally[0].NodeID)
}

func TestSubmitTeeValidations_SkipsNonPendingWrongStageAndExpired(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(160)
	enableTeeValidationParams(t, k, sdkCtx)
	requireParticipant(t, k, sdkCtx, testutil.Validator)

	subject := newTestSubject(t, k, sdkCtx, testutil.Executor, "node-pending", 100)
	newTestSubject(t, k, sdkCtx, testutil.Executor, "node-verified", 100)
	newTestSubject(t, k, sdkCtx, testutil.Executor, "node-stale", 90)
	newTestSubject(t, k, sdkCtx, testutil.Executor, "node-expired", 100)

	att, found, err := k.GetTeeAttestation(sdkCtx, subject, "node-verified")
	require.NoError(t, err)
	require.True(t, found)
	att.VerificationStatus = types.TeeVerificationStatus_TEE_VERIFICATION_VERIFIED
	require.NoError(t, k.SetTeeAttestation(sdkCtx, att))

	expiredAtt, found, err := k.GetTeeAttestation(sdkCtx, subject, "node-expired")
	require.NoError(t, err)
	require.True(t, found)
	expiredAtt.ExpiresAtHeight = 150
	require.NoError(t, k.SetTeeAttestation(sdkCtx, expiredAtt))

	srv := keeper.NewMsgServerImpl(k)
	_, err = srv.SubmitTeeValidations(sdkCtx, &types.MsgSubmitTeeValidations{
		Creator:                  testutil.Validator,
		PocStageStartBlockHeight: 100,
		Validations: []*types.TeeValidationEntry{
			{SubjectAddress: testutil.Executor, NodeId: "node-pending", Verdict: types.TeeVerdict_TEE_VERDICT_OK, MeasurementSeen: "0xDEADBEEF"},
			{SubjectAddress: testutil.Executor, NodeId: "node-verified", Verdict: types.TeeVerdict_TEE_VERDICT_OK},
			{SubjectAddress: testutil.Executor, NodeId: "node-stale", Verdict: types.TeeVerdict_TEE_VERDICT_OK},
			{SubjectAddress: testutil.Executor, NodeId: "node-expired", Verdict: types.TeeVerdict_TEE_VERDICT_OK},
		},
	})
	require.NoError(t, err)

	validator, err := sdk.AccAddressFromBech32(testutil.Validator)
	require.NoError(t, err)

	has, err := k.HasTeeValidation(sdkCtx, 100, subject, "node-pending", validator)
	require.NoError(t, err)
	require.True(t, has)

	has, err = k.HasTeeValidation(sdkCtx, 100, subject, "node-verified", validator)
	require.NoError(t, err)
	require.False(t, has)

	has, err = k.HasTeeValidation(sdkCtx, 100, subject, "node-stale", validator)
	require.NoError(t, err)
	require.False(t, has)

	has, err = k.HasTeeValidation(sdkCtx, 100, subject, "node-expired", validator)
	require.NoError(t, err)
	require.False(t, has)
}

func TestSubmitTeeValidations_SkipsOkVoteMissingMeasurementSeen(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(160)
	enableTeeValidationParams(t, k, sdkCtx)
	requireParticipant(t, k, sdkCtx, testutil.Validator)

	subject, err := sdk.AccAddressFromBech32(testutil.Executor)
	require.NoError(t, err)
	att := types.TeeAttestation{
		ParticipantAddress: testutil.Executor,
		NodeId:             "node-1",
		PublicKey:          makeTestPublicKey(0x42),
		Provider:           "intel-tdx-lite",
		EnvelopeVersion:    "v1",
		Measurement:        "deadbeef",
		AttestedAtHeight:   100,
		ExpiresAtHeight:    100 + 1000,
		VerificationStatus: types.TeeVerificationStatus_TEE_VERIFICATION_PENDING,
	}
	require.NoError(t, k.SetTeeAttestation(sdkCtx, att))

	srv := keeper.NewMsgServerImpl(k)
	_, err = srv.SubmitTeeValidations(sdkCtx, &types.MsgSubmitTeeValidations{
		Creator:                  testutil.Validator,
		PocStageStartBlockHeight: 100,
		Validations: []*types.TeeValidationEntry{{
			SubjectAddress: testutil.Executor,
			NodeId:         "node-1",
			Verdict:        types.TeeVerdict_TEE_VERDICT_OK,
		}},
	})
	require.NoError(t, err)

	validator, err := sdk.AccAddressFromBech32(testutil.Validator)
	require.NoError(t, err)
	_, found, err := k.GetTeeValidation(sdkCtx, 100, subject, "node-1", validator)
	require.NoError(t, err)
	require.False(t, found, "invalid OK vote must be skipped, not rewritten")
}

func TestSubmitTeeValidations_SkipsOkVoteOnMeasurementMismatch(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(160)
	enableTeeValidationParams(t, k, sdkCtx)
	requireParticipant(t, k, sdkCtx, testutil.Validator)

	subject, err := sdk.AccAddressFromBech32(testutil.Executor)
	require.NoError(t, err)
	att := types.TeeAttestation{
		ParticipantAddress: testutil.Executor,
		NodeId:             "node-1",
		PublicKey:          makeTestPublicKey(0x42),
		Provider:           "intel-tdx-lite",
		EnvelopeVersion:    "v1",
		Measurement:        "deadbeef",
		AttestedAtHeight:   100,
		ExpiresAtHeight:    100 + 1000,
		VerificationStatus: types.TeeVerificationStatus_TEE_VERIFICATION_PENDING,
	}
	require.NoError(t, k.SetTeeAttestation(sdkCtx, att))

	srv := keeper.NewMsgServerImpl(k)
	_, err = srv.SubmitTeeValidations(sdkCtx, &types.MsgSubmitTeeValidations{
		Creator:                  testutil.Validator,
		PocStageStartBlockHeight: 100,
		Validations: []*types.TeeValidationEntry{{
			SubjectAddress:  testutil.Executor,
			NodeId:          "node-1",
			Verdict:         types.TeeVerdict_TEE_VERDICT_OK,
			MeasurementSeen: "0xCAFEBABE",
		}},
	})
	require.NoError(t, err)

	validator, err := sdk.AccAddressFromBech32(testutil.Validator)
	require.NoError(t, err)
	_, found, err := k.GetTeeValidation(sdkCtx, 100, subject, "node-1", validator)
	require.NoError(t, err)
	require.False(t, found, "OK vote with mismatching measurement_seen must be skipped")
}

func TestSubmitTeeValidations_AcceptsOkWithMatchingMeasurement(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(160)
	enableTeeValidationParams(t, k, sdkCtx)
	requireParticipant(t, k, sdkCtx, testutil.Validator)

	subject, err := sdk.AccAddressFromBech32(testutil.Executor)
	require.NoError(t, err)
	att := types.TeeAttestation{
		ParticipantAddress: testutil.Executor,
		NodeId:             "node-1",
		PublicKey:          makeTestPublicKey(0x42),
		Provider:           "intel-tdx-lite",
		EnvelopeVersion:    "v1",
		Measurement:        "deadbeef",
		AttestedAtHeight:   100,
		ExpiresAtHeight:    100 + 1000,
		VerificationStatus: types.TeeVerificationStatus_TEE_VERIFICATION_PENDING,
	}
	require.NoError(t, k.SetTeeAttestation(sdkCtx, att))

	srv := keeper.NewMsgServerImpl(k)
	_, err = srv.SubmitTeeValidations(sdkCtx, &types.MsgSubmitTeeValidations{
		Creator:                  testutil.Validator,
		PocStageStartBlockHeight: 100,
		Validations: []*types.TeeValidationEntry{{
			SubjectAddress:  testutil.Executor,
			NodeId:          "node-1",
			Verdict:         types.TeeVerdict_TEE_VERDICT_OK,
			MeasurementSeen: "0xDEADBEEF",
		}},
	})
	require.NoError(t, err)

	validator, err := sdk.AccAddressFromBech32(testutil.Validator)
	require.NoError(t, err)
	got, found, err := k.GetTeeValidation(sdkCtx, 100, subject, "node-1", validator)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, types.TeeVerdict_TEE_VERDICT_OK, got.Verdict)
	require.Equal(t, "deadbeef", got.MeasurementSeen, "canonical form persisted")
}
