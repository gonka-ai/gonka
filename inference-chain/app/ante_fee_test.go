package app

import (
	"testing"

	"cosmossdk.io/log"
	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/stretchr/testify/require"
	protov2 "google.golang.org/protobuf/proto"

	inferencetypes "github.com/productscience/inference/x/inference/types"

	keepertest "github.com/productscience/inference/testutil/keeper"

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
func (t testFeeTx) FeePayer() []byte                      { return nil }
func (t testFeeTx) FeeGranter() []byte                    { return nil }

// --- NetworkDutyFeeBypassDecorator tests ---

func TestNetworkDutyBypass_AllExemptMessages(t *testing.T) {
	exemptMsgs := map[string]sdk.Msg{
		"MsgSubmitPocBatch":                    &inferencetypes.MsgSubmitPocBatch{},
		"MsgSubmitSeed":                        &inferencetypes.MsgSubmitSeed{},
		"MsgMLNodeWeightDistribution":          &inferencetypes.MsgMLNodeWeightDistribution{},
		"MsgSubmitPocValidationsV2":            &inferencetypes.MsgSubmitPocValidationsV2{},
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
		&inferencetypes.MsgSubmitHardwareDiff{},
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
			&inferencetypes.MsgClaimRewards{},
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
		msgs: []sdk.Msg{&inferencetypes.MsgClaimRewards{}},
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

// --- inferencetypes.IsNetworkDuty tests ---

func TestIsExemptMessageType(t *testing.T) {
	// Exempt
	require.True(t, inferencetypes.IsNetworkDuty(&inferencetypes.MsgSubmitPocBatch{}))
	require.True(t, inferencetypes.IsNetworkDuty(&inferencetypes.MsgSubmitSeed{}))
	require.True(t, inferencetypes.IsNetworkDuty(&inferencetypes.MsgSubmitPocValidationsV2{}))
	require.True(t, inferencetypes.IsNetworkDuty(&inferencetypes.MsgMLNodeWeightDistribution{}))
	require.True(t, inferencetypes.IsNetworkDuty(&blstypes.MsgSubmitDealerPart{}))
	require.True(t, inferencetypes.IsNetworkDuty(&blstypes.MsgSubmitVerificationVector{}))
	require.True(t, inferencetypes.IsNetworkDuty(&blstypes.MsgSubmitGroupKeyValidationSignature{}))
	require.True(t, inferencetypes.IsNetworkDuty(&blstypes.MsgSubmitPartialSignature{}))
	require.True(t, inferencetypes.IsNetworkDuty(&blstypes.MsgRespondDealerComplaints{}))
	require.True(t, inferencetypes.IsNetworkDuty(&inferencetypes.MsgClaimRewards{}))
	require.True(t, inferencetypes.IsNetworkDuty(&inferencetypes.MsgSettleDevshardEscrow{}))

	// Not exempt
	require.False(t, inferencetypes.IsNetworkDuty(&inferencetypes.MsgSubmitHardwareDiff{}))
	require.False(t, inferencetypes.IsNetworkDuty(&blstypes.MsgRequestThresholdSignature{}))  // open to anyone, no rate limit
	require.False(t, inferencetypes.IsNetworkDuty(&inferencetypes.MsgPoCV2StoreCommit{}))     // intentional sybil-defense fee via chargePoCV2StoreCommitGas
	require.False(t, inferencetypes.IsNetworkDuty(&inferencetypes.MsgCreateDevshardEscrow{})) // user-driven, paid
	require.False(t, inferencetypes.IsNetworkDuty(&inferencetypes.MsgSubmitNewParticipant{}))
	require.False(t, inferencetypes.IsNetworkDuty(&banktypes.MsgSend{}))
	require.False(t, inferencetypes.IsNetworkDuty(&stakingtypes.MsgDelegate{}))
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

func TestIsNetworkDuty_EmptyMsgExec_NotDuty(t *testing.T) {
	k, _ := keepertest.InferenceKeeper(t)
	ir := k.Codec().(codec.ProtoCodecMarshaler).InterfaceRegistry()
	authztypes.RegisterInterfaces(ir)

	empty := &authztypes.MsgExec{Grantee: "cosmos1test"}
	require.False(t, isNetworkDuty(empty, &k),
		"empty MsgExec must not be treated as an all-exempt network duty")

	decorator := NetworkDutyFeeBypassDecorator{
		InferenceKeeper: &k,
		GasCap:          10_000_000,
		Priority:        10_000_000,
	}
	tx := testFeeTx{msgs: []sdk.Msg{empty}, gas: 100_000}
	ctx := newTestContext().WithMinGasPrices(sdk.DecCoins{sdk.NewDecCoin("ngonka", math.NewInt(10))})
	_, err := decorator.AnteHandle(ctx, tx, false, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		require.False(t, IsNetworkDutyBypassed(ctx), "empty MsgExec must not get the duty bypass")
		require.NotEmpty(t, ctx.MinGasPrices())
		return ctx, nil
	})
	require.NoError(t, err)
}

func TestUnwrapFeeMsgs_EmptyMsgExecRejected(t *testing.T) {
	k, _ := keepertest.InferenceKeeper(t)
	ir := k.Codec().(codec.ProtoCodecMarshaler).InterfaceRegistry()
	authztypes.RegisterInterfaces(ir)
	inferencetypes.RegisterInterfaces(ir)

	_, err := unwrapFeeMsgs([]sdk.Msg{&authztypes.MsgExec{Grantee: "cosmos1test"}}, &k)
	require.Error(t, err)
	require.Contains(t, err.Error(), "empty MsgExec")

	grantee := sdk.AccAddress("granteeaddr________")
	innerEmpty := authztypes.NewMsgExec(grantee, []sdk.Msg{})
	outer := authztypes.NewMsgExec(grantee, []sdk.Msg{&innerEmpty})
	_, err = unwrapFeeMsgs([]sdk.Msg{&outer}, &k)
	require.Error(t, err, "nested empty MsgExec must be rejected at every depth")
}

func TestIsNetworkDuty_MsgExec_OneLevelDuty(t *testing.T) {
	k, _ := keepertest.InferenceKeeper(t)
	ir := k.Codec().(codec.ProtoCodecMarshaler).InterfaceRegistry()
	authztypes.RegisterInterfaces(ir)
	inferencetypes.RegisterInterfaces(ir)

	grantee := sdk.AccAddress("granteeaddr________")
	exec := authztypes.NewMsgExec(grantee, []sdk.Msg{&inferencetypes.MsgClaimRewards{}})
	require.True(t, isNetworkDuty(&exec, &k), "one-level DAPI MsgExec of a duty must still classify as duty")

	mixed := authztypes.NewMsgExec(grantee, []sdk.Msg{
		&inferencetypes.MsgClaimRewards{},
		&inferencetypes.MsgPoCV2StoreCommit{},
	})
	require.False(t, isNetworkDuty(&mixed, &k), "mixed duty+paying MsgExec must not bypass")
}

func TestIsNetworkDuty_NonExecNonExempt(t *testing.T) {
	// Non-MsgExec, non-exempt message
	require.False(t, isNetworkDuty(&banktypes.MsgSend{}, nil))
	require.False(t, isNetworkDuty(&inferencetypes.MsgPoCV2StoreCommit{}, nil))
}

func TestIsNetworkDuty_ExemptDirectMessage(t *testing.T) {
	// Direct exempt message (not wrapped in MsgExec)
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
	require.Equal(t, uint64(0), fp.MinGasPriceNgonka)
	require.Equal(t, uint64(500_000), fp.BaseValidationGas)
	require.Equal(t, uint64(100), fp.GasPerPocCount)
	require.Empty(t, fp.EnabledFeeGroups)
	require.NoError(t, fp.Validate())
	require.NotNil(t, fp.GroupByName(inferencetypes.FeeGroupEpoch))
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

func TestGonkaFeeChecker_GroupPolarity(t *testing.T) {
	exempt := inferencetypes.IsNetworkDuty
	fp := inferencetypes.DefaultFeeParams()
	require.Equal(t, uint64(0), fp.EnabledPayingPrice([]sdk.Msg{&inferencetypes.MsgPoCV2StoreCommit{}}, exempt))
	require.Equal(t, uint64(0), fp.EnabledPayingPrice([]sdk.Msg{&inferencetypes.MsgSubmitHardwareDiff{}}, exempt))

	epoch := fp.GroupByName(inferencetypes.FeeGroupEpoch)
	require.NotNil(t, epoch)
	epoch.MinGasPrice = 10
	fp.EnabledFeeGroups = []string{inferencetypes.FeeGroupEpoch}

	require.Equal(t, uint64(10), fp.EnabledPayingPrice([]sdk.Msg{&inferencetypes.MsgPoCV2StoreCommit{}}, exempt))
	require.Equal(t, uint64(10), fp.EnabledPayingPrice([]sdk.Msg{&inferencetypes.MsgSubmitHardwareDiff{}}, exempt))
	require.Equal(t, uint64(0), fp.EnabledPayingPrice([]sdk.Msg{&inferencetypes.MsgSubmitSeed{}}, exempt), "seed stays ante-exempt")
	require.Equal(t, uint64(0), fp.EnabledPayingPrice([]sdk.Msg{&banktypes.MsgSend{}}, exempt), "cosmos off")
	require.Equal(t, uint64(10), fp.EnabledPayingPrice([]sdk.Msg{
		&inferencetypes.MsgSubmitSeed{},
		&inferencetypes.MsgPoCV2StoreCommit{},
	}, exempt), "mixed fail-closed")

	fp.EnabledFeeGroups = []string{inferencetypes.FeeGroupCosmos}
	require.Equal(t, uint64(0), fp.EnabledPayingPrice([]sdk.Msg{&inferencetypes.MsgPoCV2StoreCommit{}}, exempt))
	cosmos := &inferencetypes.FeeGroup{Name: inferencetypes.FeeGroupCosmos, MinGasPrice: 7}
	fp.Groups = append(fp.Groups, cosmos)
	require.Equal(t, uint64(7), fp.EnabledPayingPrice([]sdk.Msg{&banktypes.MsgSend{}}, exempt))

	fp.EnabledFeeGroups = []string{inferencetypes.FeeGroupBLS}
	require.Equal(t, uint64(0), fp.EnabledPayingPrice([]sdk.Msg{&blstypes.MsgSubmitDealerPart{}}, exempt), "bls duties still exempt")
}

func TestGonkaFeeChecker_MsgExecWithoutCodecRejected(t *testing.T) {
	checker := GonkaFeeChecker(nil)
	tx := testFeeTx{
		msgs: []sdk.Msg{&authztypes.MsgExec{Grantee: "cosmos1test"}},
		fee:  sdk.Coins{},
		gas:  100_000,
	}
	_, _, err := checker(newTestContext(), tx)
	require.Error(t, err, "unclassifiable MsgExec must not be treated as fee-less")
}

func TestGonkaFeeChecker_NestedMsgExecPaysInner(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	ir := k.Codec().(codec.ProtoCodecMarshaler).InterfaceRegistry()
	authztypes.RegisterInterfaces(ir)
	inferencetypes.RegisterInterfaces(ir)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	fp := inferencetypes.DefaultFeeParams()
	epoch := fp.GroupByName(inferencetypes.FeeGroupEpoch)
	require.NotNil(t, epoch)
	epoch.MinGasPrice = 10
	fp.EnabledFeeGroups = []string{inferencetypes.FeeGroupEpoch}
	params.FeeParams = fp
	require.NoError(t, k.SetParams(ctx, params))

	grantee := sdk.AccAddress("granteeaddr________")
	inner := authztypes.NewMsgExec(grantee, []sdk.Msg{&inferencetypes.MsgPoCV2StoreCommit{}})
	outer := authztypes.NewMsgExec(grantee, []sdk.Msg{&inner})

	checker := GonkaFeeChecker(&k)
	tx := testFeeTx{
		msgs: []sdk.Msg{&outer},
		fee:  sdk.Coins{},
		gas:  100_000,
	}
	_, _, err = checker(ctx, tx)
	require.Error(t, err, "nested MsgExec of StoreCommit must require epoch fees")

	tx.fee = sdk.NewCoins(sdk.NewCoin("ngonka", math.NewInt(1_000_000)))
	_, _, err = checker(ctx, tx)
	require.NoError(t, err)
}

func TestGonkaFeeChecker_EmptyMsgExecRejected(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	ir := k.Codec().(codec.ProtoCodecMarshaler).InterfaceRegistry()
	authztypes.RegisterInterfaces(ir)

	checker := GonkaFeeChecker(&k)
	tx := testFeeTx{
		msgs: []sdk.Msg{&authztypes.MsgExec{Grantee: "cosmos1test"}},
		fee:  sdk.Coins{},
		gas:  100_000,
	}
	_, _, err := checker(ctx, tx)
	require.Error(t, err, "empty MsgExec must not be treated as fee-less")
}

func TestGonkaFeeChecker_OneLevelMsgExecPaysInner(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	ir := k.Codec().(codec.ProtoCodecMarshaler).InterfaceRegistry()
	authztypes.RegisterInterfaces(ir)
	inferencetypes.RegisterInterfaces(ir)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	fp := inferencetypes.DefaultFeeParams()
	epoch := fp.GroupByName(inferencetypes.FeeGroupEpoch)
	require.NotNil(t, epoch)
	epoch.MinGasPrice = 10
	fp.EnabledFeeGroups = []string{inferencetypes.FeeGroupEpoch}
	params.FeeParams = fp
	require.NoError(t, k.SetParams(ctx, params))

	grantee := sdk.AccAddress("granteeaddr________")
	exec := authztypes.NewMsgExec(grantee, []sdk.Msg{&inferencetypes.MsgPoCV2StoreCommit{}})
	checker := GonkaFeeChecker(&k)
	tx := testFeeTx{
		msgs: []sdk.Msg{&exec},
		fee:  sdk.Coins{},
		gas:  100_000,
	}
	_, _, err = checker(ctx, tx)
	require.Error(t, err, "one-level MsgExec of StoreCommit must require epoch fees")

	tx.fee = sdk.NewCoins(sdk.NewCoin("ngonka", math.NewInt(1_000_000)))
	_, _, err = checker(ctx, tx)
	require.NoError(t, err)

	mixed := authztypes.NewMsgExec(grantee, []sdk.Msg{
		&inferencetypes.MsgClaimRewards{},
		&inferencetypes.MsgPoCV2StoreCommit{},
	})
	tx.msgs = []sdk.Msg{&mixed}
	tx.fee = sdk.Coins{}
	_, _, err = checker(ctx, tx)
	require.Error(t, err, "mixed duty+paying MsgExec must use the paying inner's price")

	tx.fee = sdk.NewCoins(sdk.NewCoin("ngonka", math.NewInt(1_000_000)))
	_, _, err = checker(ctx, tx)
	require.NoError(t, err)
}

type extraGasRecorder struct {
	storetypes.GasMeter
	extra storetypes.Gas
}

func newExtraGasRecorder() *extraGasRecorder {
	return &extraGasRecorder{GasMeter: storetypes.NewInfiniteGasMeter()}
}

func (r *extraGasRecorder) ConsumeGas(amount storetypes.Gas, descriptor string) {
	if descriptor == "fee_group_period_base" || descriptor == "fee_group_extra" {
		r.extra += amount
	}
	r.GasMeter.ConsumeGas(amount, descriptor)
}

func TestFeeGroupRepeatedLenDecorator_ChargesWithoutHandler(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	fp := inferencetypes.DefaultFeeParams()
	epoch := fp.GroupByName(inferencetypes.FeeGroupEpoch)
	require.NotNil(t, epoch)
	epoch.Msgs = append(epoch.Msgs, &inferencetypes.MsgGasRule{
		TypeUrl: sdk.MsgTypeURL(&inferencetypes.MsgSubmitPocValidationsV2{}),
		Func: &inferencetypes.MsgGasRule_RepeatedLen{
			RepeatedLen: &inferencetypes.RepeatedLenParams{GasPerUnit: 10, Field: "validations"},
		},
	})
	require.NoError(t, fp.Validate())
	params.FeeParams = fp
	require.NoError(t, k.SetParams(ctx, params))

	rec := newExtraGasRecorder()
	ctx = ctx.WithGasMeter(rec)
	decorator := FeeGroupRepeatedLenDecorator{InferenceKeeper: &k}
	tx := testFeeTx{
		msgs: []sdk.Msg{&inferencetypes.MsgSubmitPocValidationsV2{
			Validations: []*inferencetypes.PoCValidationEntryV2{{}, {}, {}},
		}},
	}
	nextCalled := false
	_, err = decorator.AnteHandle(ctx, tx, false, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		nextCalled = true
		return ctx, nil
	})
	require.NoError(t, err)
	require.True(t, nextCalled)
	require.Equal(t, storetypes.Gas(30), rec.extra, "repeated_len must charge from FeeParams without a handler call")
}

func TestFeeGroupRepeatedLenDecorator_SkipsStoreCommit(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	fp := inferencetypes.DefaultFeeParams()
	epoch := fp.GroupByName(inferencetypes.FeeGroupEpoch)
	require.NotNil(t, epoch)
	for _, rule := range epoch.Msgs {
		if d := rule.GetStoredDelta(); d != nil {
			d.GasPerUnit = 100
			if rule.Base != nil {
				rule.Base.Gas = 500_000
			}
		}
	}
	params.FeeParams = fp
	require.NoError(t, k.SetParams(ctx, params))

	rec := newExtraGasRecorder()
	decorator := FeeGroupRepeatedLenDecorator{InferenceKeeper: &k}
	tx := testFeeTx{
		msgs: []sdk.Msg{&inferencetypes.MsgPoCV2StoreCommit{
			PocStageStartBlockHeight: 100,
			Entries:                  []*inferencetypes.PoCV2CommitEntry{{ModelId: "m", Count: 8}},
		}},
	}
	_, err = decorator.AnteHandle(ctx.WithGasMeter(rec), tx, false, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		return ctx, nil
	})
	require.NoError(t, err)
	require.Equal(t, storetypes.Gas(0), rec.extra, "stored_delta still belongs to the StoreCommit handler")
}
