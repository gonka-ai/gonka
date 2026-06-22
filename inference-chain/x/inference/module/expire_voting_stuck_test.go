package inference_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/testutil/sample"
	inference "github.com/productscience/inference/x/inference/module"
	"github.com/productscience/inference/x/inference/types"
)

// TestExpireInferences_VotingInferenceRefundedOnTimeout exercises the real
// expireInferences filter (module.go) and asserts that a VOTING inference
// whose timeout fires is refunded and marked EXPIRED.
//
// Bug (pre-fix): the filter handled Status == STARTED only. When a failing
// MsgValidation moves an inference to VOTING and the resulting x/group
// proposals miss quorum, the timeout cleanup silently skips it — the timeout
// entry is removed but the inference stays VOTING forever and the client's
// escrow is stranded in the inference module account.
//
// Without the fix this test FAILS twice over: the BankKeeper refund call is
// never made (unmet gomock expectation) and the inference stays VOTING. With
// the fix the VOTING branch calls expireInferenceAndIssueRefund → refund +
// EXPIRED.
//
// expireInferences is unexported, so the test drives it through the
// ExpireInferencesForTest wrapper (export_test.go). currentEpoch is passed as
// nil: the VOTING path never consults the epoch, and
// GetLatestPoCOrCPoCRangeWithData short-circuits to an inactive PoC range when
// currentEpoch == nil — so the expiry context builds without any epoch/PoC
// state and the real STARTED/VOTING filter runs.
func TestExpireInferences_VotingInferenceRefundedOnTimeout(t *testing.T) {
	k, sdkCtx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	am := inference.NewAppModule(nil, k, nil, nil, nil, nil)

	// Refund path: IssueRefund -> PayParticipantFromEscrow ->
	// SendCoinsFromModuleToAccount (+ best-effort LogSubAccountTransaction).
	// Times(1) is the fail-without-fix guard: pre-fix the VOTING inference is
	// skipped and this expectation goes unmet.
	mocks.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil).Times(1)
	mocks.BankKeeper.EXPECT().LogSubAccountTransaction(
		gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
	).AnyTimes()

	const inferenceID = "stuck-in-voting"
	const blockHeight = int64(100)
	const escrowAmount = int64(500)
	clientAddr := sample.AccAddress()
	executorAddr := sample.AccAddress()

	require.NoError(t, k.SetInference(sdkCtx, types.Inference{
		InferenceId:  inferenceID,
		Index:        inferenceID,
		Status:       types.InferenceStatus_VOTING,
		EpochId:      1,
		EscrowAmount: escrowAmount,
		ExecutedBy:   executorAddr,
		RequestedBy:  clientAddr,
		AssignedTo:   executorAddr,
	}))
	require.NoError(t, k.SetInferenceTimeout(sdkCtx, types.InferenceTimeout{
		InferenceId:      inferenceID,
		ExpirationHeight: uint64(blockHeight),
	}))

	params, err := k.GetParams(sdkCtx)
	require.NoError(t, err)

	timeouts := k.GetAllInferenceTimeoutForHeight(sdkCtx, uint64(blockHeight))
	require.Len(t, timeouts, 1, "expected the one queued timeout")

	// The operation under test — the real production filter.
	require.NoError(t, am.ExpireInferencesForTest(sdkCtx, timeouts, blockHeight, nil, &params))

	after, found := k.GetInference(sdkCtx, inferenceID)
	require.True(t, found)
	require.Equalf(t, types.InferenceStatus_EXPIRED, after.Status,
		"expireInferences must mark a timed-out VOTING inference EXPIRED and refund "+
			"the client escrow; pre-fix it was silently dropped (finding #3 / #1265)")
	require.Equal(t, int64(0), after.ActualCost)
}
