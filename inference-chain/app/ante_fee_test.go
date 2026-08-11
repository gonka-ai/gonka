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

	testkeeper "github.com/productscience/inference/testutil/keeper"
	inferencemodulekeeper "github.com/productscience/inference/x/inference/keeper"
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
func (t testFeeTx) FeePayer() []byte                      { return nil }
func (t testFeeTx) FeeGranter() []byte                    { return nil }

// --- NetworkDutyFeeBypassDecorator tests ---

// exemptDutyMsgs is the full fee-exempt duty set, each with its protocol actor
// field populated so the message is well-formed rather than merely well-typed.
func exemptDutyMsgs(actor string) map[string]sdk.Msg {
	return map[string]sdk.Msg{
		"MsgSubmitPocBatch":                    &inferencetypes.MsgSubmitPocBatch{Creator: actor},
		"MsgSubmitSeed":                        &inferencetypes.MsgSubmitSeed{Creator: actor},
		"MsgMLNodeWeightDistribution":          &inferencetypes.MsgMLNodeWeightDistribution{Creator: actor},
		"MsgSubmitPocValidationsV2":            &inferencetypes.MsgSubmitPocValidationsV2{Creator: actor},
		"MsgSubmitHardwareDiff":                &inferencetypes.MsgSubmitHardwareDiff{Creator: actor},
		"MsgClaimRewards":                      &inferencetypes.MsgClaimRewards{Creator: actor},
		"MsgSettleDevshardEscrow":              &inferencetypes.MsgSettleDevshardEscrow{Settler: actor},
		"MsgSubmitDealerPart":                  &blstypes.MsgSubmitDealerPart{Creator: actor},
		"MsgSubmitVerificationVector":          &blstypes.MsgSubmitVerificationVector{Creator: actor},
		"MsgSubmitGroupKeyValidationSignature": &blstypes.MsgSubmitGroupKeyValidationSignature{Creator: actor},
		"MsgSubmitPartialSignature":            &blstypes.MsgSubmitPartialSignature{Creator: actor},
		"MsgRespondDealerComplaints":           &blstypes.MsgRespondDealerComplaints{Creator: actor},
	}
}

// TestNetworkDutyBypass_NilKeeperFailsClosed asserts that a well-formed duty
// message does NOT receive the fee waiver when the keeper is absent: without a
// keeper the actor's authorization cannot be established, and #1539 requires
// the waiver be withheld rather than granted on type alone.
func TestNetworkDutyBypass_NilKeeperFailsClosed(t *testing.T) {
	for name, msg := range exemptDutyMsgs("gonka1duty") {
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
				require.False(t, IsNetworkDutyBypassed(ctx), "bypass flag must not be set without a keeper")
				require.NotEmpty(t, ctx.MinGasPrices(), "min gas prices must not be cleared without a keeper")
				return ctx, nil
			})
			require.NoError(t, err, "the tx still passes through; it just pays fees")
			require.True(t, nextCalled, "next handler should be called")
		})
	}
}

// TestDutyAuthorizationFor asserts the actor is read from the message body and
// that escrow settlement is routed to the allowlist rather than the participant
// registry. Reading the signer instead of the body would break the DAPI's
// warm-key authz path, where the grantee signs but Creator names the cold
// account (tx_manager.go broadcastMessagesAtAttempt).
func TestDutyAuthorizationFor(t *testing.T) {
	const actor = "gonka1actor"

	for name, msg := range exemptDutyMsgs(actor) {
		t.Run(name, func(t *testing.T) {
			auth, exempt := dutyAuthorizationFor(msg)
			require.True(t, exempt, "%s must be recognised as a duty", name)
			require.Equal(t, actor, auth.actor, "actor must come from the message body")
			require.Equal(t, name == "MsgSettleDevshardEscrow", auth.escrowAllowList,
				"only escrow settlement uses the allowlist registry")
		})
	}

	// The exempt set here must match isExemptMessageType exactly.
	for name, msg := range exemptDutyMsgs(actor) {
		_, exempt := dutyAuthorizationFor(msg)
		require.Equal(t, isExemptMessageType(msg), exempt,
			"dutyAuthorizationFor and isExemptMessageType disagree on %s", name)
	}

	for _, msg := range []sdk.Msg{
		&banktypes.MsgSend{},
		&inferencetypes.MsgPoCV2StoreCommit{},
		&inferencetypes.MsgCreateDevshardEscrow{},
		&blstypes.MsgRequestThresholdSignature{},
	} {
		_, exempt := dutyAuthorizationFor(msg)
		require.False(t, exempt, "%T must not be a duty", msg)
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

// dutyActor is a deterministic 20-byte address used as the protocol actor in
// bypass tests.
func dutyActor() sdk.AccAddress { return sdk.AccAddress([]byte("duty-actor-000000000")) }

// registeredDutyKeeper returns a real inference keeper with actor registered as
// a participant. After #1539 the waiver depends on the actor's authorization,
// so tests that exercise the bypass path (rather than the fail-closed path)
// need real keeper state instead of a nil keeper.
func registeredDutyKeeper(t *testing.T, actor sdk.AccAddress) (*inferencemodulekeeper.Keeper, sdk.Context) {
	t.Helper()
	k, ctx := testkeeper.InferenceKeeper(t)
	require.NoError(t, k.Participants.Set(ctx, actor, inferencetypes.Participant{
		Index:   actor.String(),
		Address: actor.String(),
	}))
	return &k, ctx.WithMinGasPrices(sdk.DecCoins{sdk.NewDecCoin("ngonka", math.NewInt(10))})
}

// TestNetworkDutyBypass_RegisteredActorIsBypassed asserts the intended
// behaviour is preserved: a duty message whose actor is a registered
// participant still gets the fee waiver and the priority boost.
func TestNetworkDutyBypass_RegisteredActorIsBypassed(t *testing.T) {
	actor := dutyActor()
	ik, ctx := registeredDutyKeeper(t, actor)
	decorator := NetworkDutyFeeBypassDecorator{
		InferenceKeeper: ik,
		GasCap:          10_000_000,
		Priority:        500_000,
	}

	tx := testFeeTx{msgs: []sdk.Msg{&inferencetypes.MsgClaimRewards{Creator: actor.String()}}, gas: 100_000}

	nextCalled := false
	_, err := decorator.AnteHandle(ctx, tx, false, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		nextCalled = true
		require.True(t, IsNetworkDutyBypassed(ctx), "registered actor should keep the waiver")
		require.Empty(t, ctx.MinGasPrices(), "min gas prices should be cleared")
		require.Equal(t, int64(500_000), ctx.Priority(), "priority boost should be applied")
		return ctx, nil
	})
	require.NoError(t, err)
	require.True(t, nextCalled)
}

// TestNetworkDutyBypass_UnregisteredActorNotBypassed is the #1539 fix: a
// structurally valid duty message from an account that is not a participant
// does not get the waiver, so GonkaFeeChecker goes on to enforce
// MinGasPriceNgonka and a zero-fee spam tx fails CheckTx instead of occupying
// block space for free.
func TestNetworkDutyBypass_UnregisteredActorNotBypassed(t *testing.T) {
	actor := dutyActor()
	ik, ctx := registeredDutyKeeper(t, actor)
	decorator := NetworkDutyFeeBypassDecorator{
		InferenceKeeper: ik,
		GasCap:          10_000_000,
		Priority:        500_000,
	}

	attacker := sdk.AccAddress([]byte("attacker-00000000000"))
	for name, msg := range exemptDutyMsgs(attacker.String()) {
		if name == "MsgSettleDevshardEscrow" {
			// Escrow settlement is allowlist-gated, and an empty allowlist
			// means "everyone" for both ante and handler. Covered separately.
			continue
		}
		t.Run(name, func(t *testing.T) {
			tx := testFeeTx{msgs: []sdk.Msg{msg}, gas: 100_000}
			_, err := decorator.AnteHandle(ctx, tx, false, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
				require.False(t, IsNetworkDutyBypassed(ctx), "unregistered actor must not get the waiver")
				require.NotEmpty(t, ctx.MinGasPrices(), "min gas prices must stay enforced")
				return ctx, nil
			})
			require.NoError(t, err, "the tx passes through the decorator; it just pays fees")
		})
	}
}

// TestNetworkDutyBypass_EscrowAllowList asserts escrow settlement is checked
// against the devshard allowlist, matching EscrowAllowListPermission. With an
// empty allowlist IsAllowedEscrowCreator admits everyone — the same answer the
// handler gives — so ante and DeliverTx cannot disagree.
func TestNetworkDutyBypass_EscrowAllowList(t *testing.T) {
	actor := dutyActor()
	ik, ctx := registeredDutyKeeper(t, actor)
	decorator := NetworkDutyFeeBypassDecorator{
		InferenceKeeper: ik,
		GasCap:          10_000_000,
		Priority:        500_000,
	}

	settler := sdk.AccAddress([]byte("settler-000000000000"))
	require.True(t, ik.IsAllowedEscrowCreator(ctx, settler.String()),
		"precondition: empty allowlist admits everyone, as the handler does")

	tx := testFeeTx{
		msgs: []sdk.Msg{&inferencetypes.MsgSettleDevshardEscrow{Settler: settler.String()}},
		gas:  100_000,
	}
	_, err := decorator.AnteHandle(ctx, tx, false, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		require.True(t, IsNetworkDutyBypassed(ctx), "allowlisted settler keeps the waiver")
		return ctx, nil
	})
	require.NoError(t, err)
}

func TestNetworkDutyBypass_GasCapEnforced(t *testing.T) {
	actor := dutyActor()
	ik, ctx := registeredDutyKeeper(t, actor)
	decorator := NetworkDutyFeeBypassDecorator{
		InferenceKeeper: ik,
		GasCap:          10_000_000,
		Priority:        500_000,
	}

	// Gas exceeds cap: should reject. The cap only applies to txs that qualify
	// for the bypass, so the actor must be authorized to reach this check.
	tx := testFeeTx{
		msgs: []sdk.Msg{&inferencetypes.MsgClaimRewards{Creator: actor.String()}},
		gas:  20_000_000, // exceeds 10M cap
	}

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
	require.False(t, isExemptMessageType(&blstypes.MsgRequestThresholdSignature{}))  // open to anyone, no rate limit
	require.False(t, isExemptMessageType(&inferencetypes.MsgPoCV2StoreCommit{}))     // intentional sybil-defense fee via chargePoCV2StoreCommitGas
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
	require.False(t, isNetworkDuty(newTestContext(), execMsg, nil),
		"MsgExec should fail closed with nil keeper")
}

func TestIsNetworkDuty_NonExecNonExempt(t *testing.T) {
	// Non-MsgExec, non-exempt message
	require.False(t, isNetworkDuty(newTestContext(), &banktypes.MsgSend{}, nil))
	require.False(t, isNetworkDuty(newTestContext(), &inferencetypes.MsgPoCV2StoreCommit{}, nil))
}

// TestIsNetworkDuty_ExemptTypeAloneIsNotEnough is the regression guard for
// #1539: an exempt *type* no longer implies the waiver. Authorization of the
// actor must be established, so a nil keeper fails closed even for a duty type
// that would previously have been bypassed on type alone.
func TestIsNetworkDuty_ExemptTypeAloneIsNotEnough(t *testing.T) {
	require.False(t, isNetworkDuty(newTestContext(), &blstypes.MsgSubmitDealerPart{Creator: "gonka1duty"}, nil),
		"exempt type must not be bypassed without an authorized actor")
	require.True(t, isExemptMessageType(&blstypes.MsgSubmitDealerPart{}),
		"the type itself is still a duty type")
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
