package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/testutil/sample"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// TestRefundInvalidation_LeavesAccountingDrift constructively reproduces
// the inference module's accounting drift triggered by
// refundInvalidatedInference (`msg_server_invalidate_inference.go:64-79`).
//
// Scenario:
//
//  1. Client escrows ActualCost=500 ngonka via MsgStartInference; module
//     account ends up holding the 500.
//  2. Inference completes; executor's CoinBalance is credited the full
//     500. A validator's vote then redistributes 100 of the 500 to the
//     validator via shareWorkWithValidators
//     (`msg_server_validation.go:483, :495`). Post-completion state:
//     executor.CoinBalance=+400, validator.CoinBalance=+100,
//     positiveSum=500, moduleBalance=500 — BankBacksPositiveBalance
//     invariant holds.
//  3. Inference is later invalidated. `refundInvalidatedInference` does:
//        k.IssueRefund(ctx, ActualCost, RequestedBy, ...)   // bank: -500
//        executor.CoinBalance -= ActualCost                  // ledger: -500 from executor only
//     Validator's +100 claim is NOT touched.
//
// Post-invalidation state: executor.CoinBalance=-100, validator=+100,
// positiveSum=100, moduleBalance=0. moduleBalance < positiveSum →
// invariant fires by exactly the validator's share (100 ngonka), which
// is the gap between the symmetric credit path (split among multiple
// parties) and the asymmetric debit path (charged to executor only).
//
// This test demonstrates a single legitimate handler call breaking the
// BankBacksPositiveBalanceInvariant. The drift is not a sim artefact —
// it is deterministic from the handler's existing logic.
func TestRefundInvalidation_LeavesAccountingDrift(t *testing.T) {
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)

	// Bank mocks for the refund path:
	// IssueRefund → PayParticipantFromEscrow → SendCoinsFromModuleToAccount
	// + best-effort LogSubAccountTransaction.
	mocks.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(
			gomock.Any(), types.ModuleName, gomock.Any(), gomock.Any(), gomock.Any(),
		).Return(nil).Times(1)
	mocks.BankKeeper.EXPECT().LogSubAccountTransaction(
		gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
	).AnyTimes()

	// Pre-state: executor +400, validator +100 (post-completion +
	// post-validation-share). positiveSum = 500.
	executorAddr := sample.AccAddress()
	validatorAddr := sample.AccAddress()
	requesterAddr := sample.AccAddress()
	setParticipant(t, k, ctx, executorAddr, 400)
	setParticipant(t, k, ctx, validatorAddr, 100)

	const actualCost = int64(500)

	// Mirror the two state mutations from refundInvalidatedInference at
	// `msg_server_invalidate_inference.go:67` (bank refund) and `:74`
	// (executor ledger debit). The handler is package-private; we call
	// the same exported primitives in the same order.
	require.NoError(t,
		k.IssueRefund(ctx, actualCost, requesterAddr, "invalidated_inference:test-id"),
	)
	setParticipant(t, k, ctx, executorAddr, 400-actualCost)

	// Module account: started at 500, paid out 500 in refund, now holds 0.
	expectModuleBalance(mocks, 0)

	msg, broken := keeper.BankBacksPositiveBalanceInvariant(k)(ctx)
	require.Truef(t, broken,
		"BUG REPRODUCED: refundInvalidatedInference debits full ActualCost from "+
			"executor only — the validator's +100 claim from shareWorkWithValidators "+
			"remains as positive CoinBalance, but the backing coins have already left "+
			"the module account as refund. Invariant must fire. msg=%q", msg)
	require.Contains(t, msg, "owed 100",
		"module account expected to be unable to cover the validator's +100 claim")
}
