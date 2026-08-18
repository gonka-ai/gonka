package app

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/stretchr/testify/require"

	testkeeper "github.com/productscience/inference/testutil/keeper"
)

func TestPocPeriodValidationDecorator_NonPocMessage(t *testing.T) {
	decorator := PocPeriodValidationDecorator{
		inferenceKeeper: nil,
	}

	ctx := sdk.Context{}

	t.Log("Non-PoC messages pass through without validation")
	require.NotNil(t, decorator)
	require.NotNil(t, ctx)
}

func TestPocPeriodValidationDecorator_SimulationMode(t *testing.T) {
	decorator := PocPeriodValidationDecorator{
		inferenceKeeper: nil,
	}

	ctx := sdk.Context{}

	t.Log("Simulation mode bypasses PoC period validation")
	require.NotNil(t, decorator)
	require.NotNil(t, ctx)
}

// nestedExec wraps msg in `levels` MsgExec layers. The grantee is irrelevant to
// the depth bound: this decorator runs before signature verification and does
// not check grants, so any address serves.
func nestedExec(levels int, msg sdk.Msg) sdk.Msg {
	grantee := sdk.AccAddress([]byte("poc-nesting-grantee-"))
	for i := 0; i < levels; i++ {
		wrapped := authztypes.NewMsgExec(grantee, []sdk.Msg{msg})
		msg = &wrapped
	}
	return msg
}

func pocNestingDecorator(t *testing.T) (PocPeriodValidationDecorator, sdk.Context) {
	t.Helper()
	k, ctx := testkeeper.InferenceKeeper(t)
	return PocPeriodValidationDecorator{inferenceKeeper: &k}, ctx
}

// TestPocPeriodValidation_NestedMsgExec_RejectsPastDepthLimit is the #1576
// review follow-up: checkMessage used to recurse through MsgExec with no bound.
// It runs CheckTx-only, where ante work is not gas-metered, so an unbounded
// walk is a spam surface. Past the limit the tree cannot be inspected, so it
// must be rejected rather than passed through.
func TestPocPeriodValidation_NestedMsgExec_RejectsPastDepthLimit(t *testing.T) {
	ppd, ctx := pocNestingDecorator(t)

	// One level deeper than the bound, wrapping a message the decorator would
	// otherwise ignore — proving the rejection comes from depth alone.
	msg := nestedExec(maxMsgExecNestingDepth+1, &banktypes.MsgSend{})

	err := ppd.checkMessage(ctx, msg, 0)

	require.Error(t, err, "MsgExec nested past the inspection limit must be rejected")
	require.ErrorIs(t, err, sdkerrors.ErrInvalidRequest)
}

// TestPocPeriodValidation_NestedMsgExec_AdmitsWithinDepthLimit guards against
// over-rejection: nesting up to the bound must still be walked, or the bound
// becomes a liveness bug for legitimate warm-key traffic (production uses one
// level).
func TestPocPeriodValidation_NestedMsgExec_AdmitsWithinDepthLimit(t *testing.T) {
	ppd, ctx := pocNestingDecorator(t)

	msg := nestedExec(maxMsgExecNestingDepth, &banktypes.MsgSend{})

	require.NoError(t, ppd.checkMessage(ctx, msg, 0),
		"nesting up to the limit must still be inspected and admitted")
}

// TestPocPeriodValidation_DepthCountsFromCaller pins the contract that
// AnteHandle starts the walk at depth 0, so the bound counts MsgExec levels
// rather than being offset by the caller.
func TestPocPeriodValidation_DepthCountsFromCaller(t *testing.T) {
	ppd, ctx := pocNestingDecorator(t)

	single := nestedExec(1, &banktypes.MsgSend{})

	require.NoError(t, ppd.checkMessage(ctx, single, 0),
		"one wrapper at depth 0 is well within the limit")
	require.Error(t, ppd.checkMessage(ctx, single, maxMsgExecNestingDepth),
		"the same wrapper entered at the limit must be rejected")
}
