package app

import (
	"testing"

	"cosmossdk.io/log"
	"cosmossdk.io/math"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/stretchr/testify/require"
	protov2 "google.golang.org/protobuf/proto"

	inferencetypes "github.com/productscience/inference/x/inference/types"

	blstypes "github.com/productscience/inference/x/bls/types"
)

// newTestContext creates a minimal sdk.Context suitable for unit tests.
func newTestContext() sdk.Context {
	return sdk.NewContext(nil, cmtproto.Header{}, false, log.NewNopLogger())
}

// --- Test FeeTx implementation ---

type testFeeTx struct {
	msgs []sdk.Msg
	fee  sdk.Coins
	gas  uint64
}

func (t testFeeTx) GetMsgs() []sdk.Msg                    { return t.msgs }
func (t testFeeTx) GetMsgsV2() ([]protov2.Message, error) { return nil, nil }
func (t testFeeTx) GetFee() sdk.Coins                     { return t.fee }
func (t testFeeTx) GetGas() uint64                        { return t.gas }
func (t testFeeTx) FeePayer() []byte                       { return nil }
func (t testFeeTx) FeeGranter() []byte                     { return nil }

// --- NetworkDutyFeeBypassDecorator tests ---

func TestNetworkDutyBypass_AllExemptMessages(t *testing.T) {
	exemptMsgs := map[string]sdk.Msg{
		"MsgSubmitPocBatch":                    &inferencetypes.MsgSubmitPocBatch{},
		"MsgSubmitSeed":                        &inferencetypes.MsgSubmitSeed{},
		"MsgValidation":                        &inferencetypes.MsgValidation{},
		"MsgStartInference":                    &inferencetypes.MsgStartInference{},
		"MsgFinishInference":                   &inferencetypes.MsgFinishInference{},
		"MsgInvalidateInference":               &inferencetypes.MsgInvalidateInference{},
		"MsgRevalidateInference":               &inferencetypes.MsgRevalidateInference{},
		"MsgMLNodeWeightDistribution":          &inferencetypes.MsgMLNodeWeightDistribution{},
		"MsgSubmitPocValidationsV2":            &inferencetypes.MsgSubmitPocValidationsV2{},
		"MsgSubmitHardwareDiff":                &inferencetypes.MsgSubmitHardwareDiff{},
		"MsgClaimRewards":                      &inferencetypes.MsgClaimRewards{},
		"MsgSettleDevshardEscrow":              &inferencetypes.MsgSettleDevshardEscrow{},
		"MsgSubmitDealerPart":                  &blstypes.MsgSubmitDealerPart{},
		"MsgSubmitVerificationVector":          &blstypes.MsgSubmitVerificationVector{},
		"MsgSubmitGroupKeyValidationSignature": &blstypes.MsgSubmitGroupKeyValidationSignature{},
		"MsgSubmitPartialSignature":            &blstypes.MsgSubmitPartialSignature{},
		"MsgRespondDealerComplaints":           &blstypes.MsgRespondDealerComplaints{},
	}

	for name, msg := range exemptMsgs {
		t.Run(name, func(t *testing.T) {
			decorator := NetworkDutyFeeBypassDecorator{
				InferenceKeeper: nil,
				GasCap:          10_000_000,
				Priority:        500_000,
			}
			tx := testFeeTx{msgs: []sdk.Msg{msg}, gas: 100_000}
			ctx := newTestContext().WithMinGasPrices(sdk.DecCoins{sdk.NewDecCoin("ngonka", math.NewInt(10))})

			nextCalled := false
			_, err := decorator.AnteHandle(ctx, tx, false, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
				nextCalled = true
				require.True(t, IsNetworkDutyBypassed(ctx), "bypass flag should be set")
				require.Empty(t, ctx.MinGasPrices(), "min gas prices should be cleared")
				return ctx, nil
			})
			require.NoError(t, err)
			require.True(t, nextCalled, "next handler should be called")
		})
	}
}

func TestNetworkDutyBypass_NonExemptMessages(t *testing.T) {
	decorator := NetworkDutyFeeBypassDecorator{
		InferenceKeeper: nil,
		GasCap:          10_000_000,
		Priority:        500_000,
	}

	nonExemptMsgs := []sdk.Msg{
		&banktypes.MsgSend{},
		&stakingtypes.MsgDelegate{},
		// MsgPoCV2StoreCommit is intentionally non-exempt: it carries the
		// count-proportional sybil-defense fee defined in FeeParams. See
		// chargePoCV2StoreCommitGas in msg_server_poc_v2_commit.go.
		&inferencetypes.MsgPoCV2StoreCommit{},
		&inferencetypes.MsgSubmitNewParticipant{},
	}

	for _, msg := range nonExemptMsgs {
		tx := testFeeTx{msgs: []sdk.Msg{msg}, gas: 100_000}
		ctx := newTestContext().WithMinGasPrices(sdk.DecCoins{sdk.NewDecCoin("ngonka", math.NewInt(10))})

		nextCalled := false
		_, err := decorator.AnteHandle(ctx, tx, false, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
			nextCalled = true
			// Verify bypass flag was NOT set
			require.False(t, IsNetworkDutyBypassed(ctx), "bypass flag should NOT be set for %T", msg)
			// Verify min gas prices were NOT cleared
			require.NotEmpty(t, ctx.MinGasPrices(), "min gas prices should NOT be cleared for %T", msg)
			return ctx, nil
		})
		require.NoError(t, err, "non-exempt message %T should still pass through", msg)
		require.True(t, nextCalled, "next handler should be called for %T", msg)
	}
}

func TestNetworkDutyBypass_MixedMessages_NoBypass(t *testing.T) {
	decorator := NetworkDutyFeeBypassDecorator{
		InferenceKeeper: nil,
		GasCap:          10_000_000,
		Priority:        500_000,
	}

	// Mix of exempt and non-exempt: bypass should NOT apply
	tx := testFeeTx{
		msgs: []sdk.Msg{
			&inferencetypes.MsgValidation{},
			&banktypes.MsgSend{}, // non-exempt
		},
		gas: 100_000,
	}
	ctx := newTestContext().WithMinGasPrices(sdk.DecCoins{sdk.NewDecCoin("ngonka", math.NewInt(10))})

	_, err := decorator.AnteHandle(ctx, tx, false, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		require.False(t, IsNetworkDutyBypassed(ctx), "mixed tx should NOT be bypassed")
		return ctx, nil
	})
	require.NoError(t, err)
}

func TestNetworkDutyBypass_GasCapEnforced(t *testing.T) {
	decorator := NetworkDutyFeeBypassDecorator{
		InferenceKeeper: nil,
		GasCap:          10_000_000,
		Priority:        500_000,
	}

	// Gas exceeds cap: should reject
	tx := testFeeTx{
		msgs: []sdk.Msg{&inferencetypes.MsgValidation{}},
		gas:  20_000_000, // exceeds 10M cap
	}
	ctx := newTestContext()

	_, err := decorator.AnteHandle(ctx, tx, false, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		t.Fatal("next should not be called when gas exceeds cap")
		return ctx, nil
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeds cap")
}

// --- isExemptMessageType tests ---

func TestIsExemptMessageType(t *testing.T) {
	// Exempt
	require.True(t, isExemptMessageType(&inferencetypes.MsgSubmitPocBatch{}))
	require.True(t, isExemptMessageType(&inferencetypes.MsgSubmitSeed{}))
	require.True(t, isExemptMessageType(&inferencetypes.MsgSubmitPocValidationsV2{}))
	require.True(t, isExemptMessageType(&inferencetypes.MsgValidation{}))
	require.True(t, isExemptMessageType(&inferencetypes.MsgStartInference{}))
	require.True(t, isExemptMessageType(&inferencetypes.MsgFinishInference{}))
	require.True(t, isExemptMessageType(&inferencetypes.MsgInvalidateInference{}))
	require.True(t, isExemptMessageType(&inferencetypes.MsgRevalidateInference{}))
	require.True(t, isExemptMessageType(&inferencetypes.MsgMLNodeWeightDistribution{}))
	require.True(t, isExemptMessageType(&blstypes.MsgSubmitDealerPart{}))
	require.True(t, isExemptMessageType(&blstypes.MsgSubmitVerificationVector{}))
	require.True(t, isExemptMessageType(&blstypes.MsgSubmitGroupKeyValidationSignature{}))
	require.True(t, isExemptMessageType(&blstypes.MsgSubmitPartialSignature{}))
	require.True(t, isExemptMessageType(&blstypes.MsgRespondDealerComplaints{}))
	require.True(t, isExemptMessageType(&inferencetypes.MsgSubmitHardwareDiff{}))
	require.True(t, isExemptMessageType(&inferencetypes.MsgClaimRewards{}))
	require.True(t, isExemptMessageType(&inferencetypes.MsgSettleDevshardEscrow{}))

	// Not exempt
	require.False(t, isExemptMessageType(&blstypes.MsgRequestThresholdSignature{})) // open to anyone, no rate limit
	require.False(t, isExemptMessageType(&inferencetypes.MsgPoCV2StoreCommit{}))    // intentional sybil-defense fee via chargePoCV2StoreCommitGas
	require.False(t, isExemptMessageType(&inferencetypes.MsgCreateDevshardEscrow{})) // user-driven, paid
	require.False(t, isExemptMessageType(&inferencetypes.MsgSubmitNewParticipant{}))
	require.False(t, isExemptMessageType(&banktypes.MsgSend{}))
	require.False(t, isExemptMessageType(&stakingtypes.MsgDelegate{}))
}

// --- MsgExec recursive unwrapping tests ---

func TestNetworkDutyBypass_MsgExec_FailsClosedWithNilKeeper(t *testing.T) {
	// With nil keeper, MsgExec should fail closed (not bypassed)
	// even if the inner message is exempt.
	decorator := NetworkDutyFeeBypassDecorator{
		InferenceKeeper: nil,
		GasCap:          10_000_000,
		Priority:        500_000,
	}

	execMsg := &authztypes.MsgExec{
		Grantee: "cosmos1test",
		// Inner messages would need UnpackAny which requires a codec,
		// so with nil keeper we fail closed before even checking inners.
	}

	tx := testFeeTx{msgs: []sdk.Msg{execMsg}, gas: 100_000}
	ctx := newTestContext().WithMinGasPrices(sdk.DecCoins{sdk.NewDecCoin("ngonka", math.NewInt(10))})

	_, err := decorator.AnteHandle(ctx, tx, false, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		// MsgExec with nil keeper should NOT be bypassed
		require.False(t, IsNetworkDutyBypassed(ctx), "MsgExec should fail closed with nil keeper")
		require.NotEmpty(t, ctx.MinGasPrices(), "min gas prices should NOT be cleared for MsgExec with nil keeper")
		return ctx, nil
	})
	require.NoError(t, err)
}

func TestIsNetworkDuty_MsgExec_FailsClosed(t *testing.T) {
	// Direct test of isNetworkDuty with MsgExec
	execMsg := &authztypes.MsgExec{Grantee: "cosmos1test"}

	// nil keeper: fail closed
	require.False(t, isNetworkDuty(execMsg, nil),
		"MsgExec should fail closed with nil keeper")
}

func TestIsNetworkDuty_NonExecNonExempt(t *testing.T) {
	// Non-MsgExec, non-exempt message
	require.False(t, isNetworkDuty(&banktypes.MsgSend{}, nil))
	require.False(t, isNetworkDuty(&inferencetypes.MsgPoCV2StoreCommit{}, nil))
}

func TestIsNetworkDuty_ExemptDirectMessage(t *testing.T) {
	// Direct exempt message (not wrapped in MsgExec)
	require.True(t, isNetworkDuty(&inferencetypes.MsgValidation{}, nil))
	require.True(t, isNetworkDuty(&blstypes.MsgSubmitDealerPart{}, nil))
}

// --- GonkaFeeChecker tests ---

func TestGonkaFeeChecker_SufficientFee(t *testing.T) {
	// nil keeper = 0 min gas price = any fee accepted
	checker := GonkaFeeChecker(nil)

	tx := testFeeTx{
		msgs: []sdk.Msg{&banktypes.MsgSend{}},
		fee:  sdk.NewCoins(sdk.NewCoin("ngonka", math.NewInt(0))),
		gas:  100_000,
	}
	ctx := newTestContext()

	feeCoins, priority, err := checker(ctx, tx)
	require.NoError(t, err)
	require.NotNil(t, feeCoins)
	require.Equal(t, int64(0), priority)
}

func TestGonkaFeeChecker_BypassFlag(t *testing.T) {
	checker := GonkaFeeChecker(nil)

	// Zero fee tx with bypass flag: should pass
	tx := testFeeTx{
		msgs: []sdk.Msg{&banktypes.MsgSend{}},
		fee:  sdk.Coins{},
		gas:  100_000,
	}
	ctx := newTestContext().WithValue(networkDutyFeeBypassKey{}, true)

	feeCoins, _, err := checker(ctx, tx)
	require.NoError(t, err)
	require.Empty(t, feeCoins)
}

func TestGonkaFeeChecker_BypassPreservesPriority(t *testing.T) {
	checker := GonkaFeeChecker(nil)

	tx := testFeeTx{
		msgs: []sdk.Msg{&banktypes.MsgSend{}},
		fee:  sdk.Coins{},
		gas:  100_000,
	}
	// Simulate what the bypass decorator does: set flag and priority.
	ctx := newTestContext().
		WithValue(networkDutyFeeBypassKey{}, true).
		WithPriority(500_000)

	_, priority, err := checker(ctx, tx)
	require.NoError(t, err)
	require.Equal(t, int64(500_000), priority)
}

func TestGonkaFeeChecker_Priority(t *testing.T) {
	checker := GonkaFeeChecker(nil)

	// Higher fee = higher priority
	tx := testFeeTx{
		msgs: []sdk.Msg{&banktypes.MsgSend{}},
		fee:  sdk.NewCoins(sdk.NewCoin("ngonka", math.NewInt(1_000_000))),
		gas:  100_000,
	}
	ctx := newTestContext()

	_, priority, err := checker(ctx, tx)
	require.NoError(t, err)
	require.Equal(t, int64(10), priority) // 1_000_000 / 100_000 = 10
}

// --- FeeParams tests ---

func TestDefaultFeeParams(t *testing.T) {
	fp := inferencetypes.DefaultFeeParams()
	require.Equal(t, uint64(10), fp.MinGasPriceNgonka)
	require.Equal(t, uint64(500_000), fp.BaseValidationGas)
	require.Equal(t, uint64(100), fp.GasPerPocCount)
}

func TestFeeParamsMarshalRoundtrip(t *testing.T) {
	fp := &inferencetypes.FeeParams{
		MinGasPriceNgonka: 42,
		BaseValidationGas: 123_456,
		GasPerPocCount:    789,
	}

	bz, err := fp.Marshal()
	require.NoError(t, err)

	fp2 := &inferencetypes.FeeParams{}
	require.NoError(t, fp2.Unmarshal(bz))
	require.Equal(t, fp, fp2)
}
