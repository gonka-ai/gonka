package app

import (
	"fmt"

	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/cosmos/cosmos-sdk/x/auth/ante"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"

	inferencemodulekeeper "github.com/productscience/inference/x/inference/keeper"
	inferencetypes "github.com/productscience/inference/x/inference/types"

	blstypes "github.com/productscience/inference/x/bls/types"
)

// --- Context key for fee bypass flag ---

type networkDutyFeeBypassKey struct{}

// IsNetworkDutyBypassed returns true if the NetworkDutyFeeBypassDecorator has
// determined that all messages in the transaction are fee-exempt network duties.
func IsNetworkDutyBypassed(ctx sdk.Context) bool {
	v, ok := ctx.Value(networkDutyFeeBypassKey{}).(bool)
	return ok && v
}

// --- NetworkDutyFeeBypassDecorator ---

// NetworkDutyFeeBypassDecorator exempts transactions containing only protocol-duty
// messages from fee requirements. It clears min gas prices and sets a context flag
// that the custom TxFeeChecker respects.
//
// Placed before DeductFeeDecorator in the ante chain.
// Follows the same pattern as LiquidityPoolFeeBypassDecorator.
type NetworkDutyFeeBypassDecorator struct {
	InferenceKeeper *inferencemodulekeeper.Keeper
	GasCap          uint64 // maximum gas for bypassed txs to prevent block-space abuse
	Priority        int64  // priority boost so zero-fee duty txs aren't starved
}

func (d NetworkDutyFeeBypassDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (sdk.Context, error) {
	msgs := tx.GetMsgs()
	if len(msgs) == 0 {
		return next(ctx, tx, simulate)
	}

	// Check if ALL messages are fee-exempt network duties performed by an
	// authorized actor. An unauthorized actor simply does not get the waiver, so
	// GonkaFeeChecker goes on to enforce the enabled fee groups against the tx.
	//
	// Withholding rather than rejecting here keeps a false negative from
	// turning into a liveness failure for consensus-critical PoC / BLS traffic;
	// NetworkDutySignerDecorator is what rejects unauthorized duty txs outright.
	allExempt := true
	for _, msg := range msgs {
		if !isNetworkDuty(ctx, msg, d.InferenceKeeper) {
			allExempt = false
			break
		}
	}

	if !allExempt {
		return next(ctx, tx, simulate)
	}

	// Enforce gas cap on bypassed transactions.
	if feeTx, ok := tx.(sdk.FeeTx); ok {
		if d.GasCap > 0 && feeTx.GetGas() > d.GasCap {
			return ctx, fmt.Errorf("fee-bypass: gas %d exceeds cap %d for network-duty tx", feeTx.GetGas(), d.GasCap)
		}
	}

	if d.InferenceKeeper != nil {
		d.InferenceKeeper.LogDebug("AnteHandle: NetworkDutyFeeBypass - applying fee bypass",
			inferencetypes.System)
	}

	// Clear min gas prices and set bypass flag for the custom TxFeeChecker.
	ctx = ctx.WithMinGasPrices(sdk.DecCoins{})
	ctx = ctx.WithValue(networkDutyFeeBypassKey{}, true)
	if d.Priority != 0 {
		ctx = ctx.WithPriority(d.Priority)
	}

	return next(ctx, tx, simulate)
}

// isNetworkDuty checks if a message is a fee-exempt network duty whose actor is
// authorized to claim the exemption. It unwraps x/authz MsgExec exactly one
// level (the DAPI's normal use case), then checks the inner messages. Nested
// MsgExec wrappers are not allowed — they fail closed. Real-world use has no
// need for nested MsgExec and allowing arbitrary recursion is unnecessary
// complexity.
//
// Not recursing is safe here because the failure mode is withholding the waiver,
// never granting it. Nested wrappers cannot be used to smuggle a duty past this
// function into a free transaction. NetworkDutySignerDecorator does descend
// (see checkMessage), because there the failure mode is admitting a tx.
func isNetworkDuty(ctx sdk.Context, msg sdk.Msg, ik *inferencemodulekeeper.Keeper) bool {
	if execMsg, ok := msg.(*authztypes.MsgExec); ok {
		if ik == nil {
			return false // fail closed
		}
		if len(execMsg.Msgs) == 0 {
			return false // empty MsgExec is not a network duty
		}
		for _, innerMsg := range execMsg.Msgs {
			var unwrapped sdk.Msg
			if err := ik.Codec().UnpackAny(innerMsg, &unwrapped); err != nil {
				return false // fail closed on unpack error
			}
			// One level only: if the inner message is another MsgExec,
			// fail closed instead of recursing.
			if _, isNestedExec := unwrapped.(*authztypes.MsgExec); isNestedExec {
				return false
			}
			if !isAuthorizedNetworkDuty(ctx, unwrapped, ik) {
				return false
			}
		}
		return true
	}
	return isAuthorizedNetworkDuty(ctx, msg, ik)
}

// isAuthorizedNetworkDuty reports whether msg is an exempt duty type AND its
// protocol actor is authorized to perform that duty.
//
// The type check alone is not sufficient (#1539): the ante chain waives fees at
// index 11 but only verifies signatures at index 19, so a type-only exemption
// hands any funded account free, unauthenticated block space. The real
// authorization for these types runs in the message handlers, i.e. in
// DeliverTx — after mempool admission and block inclusion.
//
// The actor is read from the message body, never from the tx signer: in
// warm-key mode the DAPI wraps duty messages in authz MsgExec signed by the
// grantee while the Creator/Settler field names the cold account that is the
// actual protocol participant (tx_manager.go broadcastMessagesAtAttempt).
// Checking the signer would reject that production path.
//
// Fails closed on a nil keeper so a misconfigured ante chain cannot grant the
// waiver.
func isAuthorizedNetworkDuty(ctx sdk.Context, msg sdk.Msg, ik *inferencemodulekeeper.Keeper) bool {
	auth, exempt := dutyAuthorizationFor(msg)
	if !exempt {
		return false
	}
	if ik == nil {
		return false // fail closed
	}
	if auth.escrowAllowList {
		return ik.IsAllowedEscrowCreator(ctx, auth.actor)
	}
	return ik.IsRegisteredParticipant(ctx, auth.actor)
}

// dutyAuthorization describes who must be authorized for a fee-exempt duty and
// against which registry, mirroring the handler's own permission requirement.
type dutyAuthorization struct {
	// actor is the address named in the message body as the protocol
	// participant performing the duty (Creator, or Settler for escrow
	// settlement) — not the tx signer.
	actor string
	// escrowAllowList selects the devshard escrow allowlist instead of the
	// participant registry, matching EscrowAllowListPermission.
	escrowAllowList bool
}

// dutyAuthorizationFor returns the authorization requirement for a fee-exempt
// duty message, and whether the type is exempt at all.
//
// The exempt set here must stay identical to inferencetypes.IsNetworkDuty,
// which is the single source of truth for *which types* are fee-exempt duties.
// This function only adds *who* may claim the waiver for each. In particular
// MsgSubmitHardwareDiff is deliberately NOT here: IsNetworkDuty excludes it
// (it belongs to the paid "epoch" fee group), so it must not receive the
// waiver even from an authorized participant.
func dutyAuthorizationFor(msg sdk.Msg) (dutyAuthorization, bool) {
	switch m := msg.(type) {
	// Participant-gated duties. Handlers require ParticipantPermission
	// (PoC batch / seed), ActiveParticipantPermission OR
	// PreviousActiveParticipantPermission (claim rewards), or a blocklist
	// check on Creator (PoC V2 validations, weight distribution — declared
	// NoPermission). Registration is a superset of all of these.
	case *inferencetypes.MsgSubmitPocBatch:
		return dutyAuthorization{actor: m.Creator}, true
	case *inferencetypes.MsgSubmitPocValidationsV2:
		return dutyAuthorization{actor: m.Creator}, true
	case *inferencetypes.MsgMLNodeWeightDistribution:
		return dutyAuthorization{actor: m.Creator}, true
	case *inferencetypes.MsgSubmitSeed:
		return dutyAuthorization{actor: m.Creator}, true
	case *inferencetypes.MsgClaimRewards:
		return dutyAuthorization{actor: m.Creator}, true

	// Devshard escrow settlement is allowlist-restricted rather than
	// participant-gated (EscrowAllowListPermission).
	case *inferencetypes.MsgSettleDevshardEscrow:
		return dutyAuthorization{actor: m.Settler, escrowAllowList: true}, true

	// BLS DKG duties. Each handler scans the epoch's own participant list for
	// Creator; requiring registration is weaker and cannot reject a member of
	// that list, while still excluding arbitrary accounts.
	case *blstypes.MsgSubmitDealerPart:
		return dutyAuthorization{actor: m.Creator}, true
	case *blstypes.MsgSubmitVerificationVector:
		return dutyAuthorization{actor: m.Creator}, true
	case *blstypes.MsgSubmitGroupKeyValidationSignature:
		return dutyAuthorization{actor: m.Creator}, true
	case *blstypes.MsgSubmitPartialSignature:
		return dutyAuthorization{actor: m.Creator}, true
	case *blstypes.MsgRespondDealerComplaints:
		return dutyAuthorization{actor: m.Creator}, true

	default:
		return dutyAuthorization{}, false
	}
}

// unwrapFeeMsgs expands MsgExec wrappers so fee-group classification sees
// inner messages. Nested MsgExec is recursed up to maxFeeExecDepth. Unpack
// failure, a missing codec, or depth overflow returns an error — never a
// leftover MsgExec (ungrouped would be free).
const maxFeeExecDepth = 4

func unwrapFeeMsgs(msgs []sdk.Msg, ik *inferencemodulekeeper.Keeper) ([]sdk.Msg, error) {
	return unwrapFeeMsgsAt(msgs, ik, 0)
}

func unwrapFeeMsgsAt(msgs []sdk.Msg, ik *inferencemodulekeeper.Keeper, depth int) ([]sdk.Msg, error) {
	out := make([]sdk.Msg, 0, len(msgs))
	for _, msg := range msgs {
		execMsg, ok := msg.(*authztypes.MsgExec)
		if !ok {
			out = append(out, msg)
			continue
		}
		if depth >= maxFeeExecDepth {
			return nil, fmt.Errorf("nested MsgExec exceeds max depth %d", maxFeeExecDepth)
		}
		if ik == nil {
			return nil, fmt.Errorf("MsgExec cannot be classified without a codec")
		}
		if len(execMsg.Msgs) == 0 {
			return nil, fmt.Errorf("empty MsgExec cannot be classified")
		}
		inners := make([]sdk.Msg, 0, len(execMsg.Msgs))
		for _, inner := range execMsg.Msgs {
			var unwrapped sdk.Msg
			if err := ik.Codec().UnpackAny(inner, &unwrapped); err != nil {
				return nil, fmt.Errorf("failed to unpack MsgExec inner message: %w", err)
			}
			inners = append(inners, unwrapped)
		}
		nested, err := unwrapFeeMsgsAt(inners, ik, depth+1)
		if err != nil {
			return nil, err
		}
		out = append(out, nested...)
	}
	return out, nil
}

// FeeGroupRepeatedLenDecorator consumes extra gas for MsgGasRule.repeated_len
// during ante so those rules run without each handler calling ChargeExtraGas.
// stored_delta / stored_bytes still charge from handlers (canonical-state qty).
type FeeGroupRepeatedLenDecorator struct {
	InferenceKeeper *inferencemodulekeeper.Keeper
}

func (d FeeGroupRepeatedLenDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (sdk.Context, error) {
	if d.InferenceKeeper == nil {
		return next(ctx, tx, simulate)
	}
	inners, err := unwrapFeeMsgs(tx.GetMsgs(), d.InferenceKeeper)
	if err != nil {
		return ctx, err
	}
	for _, msg := range inners {
		if err := d.InferenceKeeper.ChargeMessageRuleGas(ctx, msg); err != nil {
			return ctx, err
		}
	}
	return next(ctx, tx, simulate)
}

// --- Custom TxFeeChecker ---

// GonkaFeeChecker returns a TxFeeChecker that enforces a consensus-level minimum
// gas price read from on-chain FeeParams. It respects the bypass flag set by
// NetworkDutyFeeBypassDecorator. This checker runs inside DeductFeeDecorator
// during both CheckTx and DeliverTx.
func GonkaFeeChecker(inferenceKeeper *inferencemodulekeeper.Keeper) ante.TxFeeChecker {
	return func(ctx sdk.Context, tx sdk.Tx) (sdk.Coins, int64, error) {
		// If bypass flag is set, allow zero fees but preserve priority
		// set by the bypass decorator.
		if IsNetworkDutyBypassed(ctx) {
			return sdk.Coins{}, ctx.Priority(), nil
		}

		feeTx, ok := tx.(sdk.FeeTx)
		if !ok {
			return nil, 0, errorsmod.Wrap(sdkerrors.ErrTxDecode, "Tx must implement FeeTx")
		}

		feeCoins := feeTx.GetFee()
		gas := feeTx.GetGas()

		var fp *inferencetypes.FeeParams
		if inferenceKeeper != nil {
			params, err := inferenceKeeper.GetParams(ctx)
			if err == nil {
				fp = params.FeeParams
			}
		}

		inners, err := unwrapFeeMsgs(tx.GetMsgs(), inferenceKeeper)
		if err != nil {
			return nil, 0, errorsmod.Wrap(sdkerrors.ErrInvalidRequest, err.Error())
		}

		price := uint64(0)
		if fp != nil {
			price = fp.EnabledPayingPrice(inners, inferencetypes.IsNetworkDuty)
		}

		if price == 0 {
			priority := getTxPriority(feeCoins, gas)
			return feeCoins, priority, nil
		}

		// Calculate required fee using big-int math to avoid uint64 overflow.
		requiredAmount := math.NewIntFromUint64(gas).Mul(math.NewIntFromUint64(price))
		requiredFee := sdk.NewCoin("ngonka", requiredAmount)

		// Check the ngonka amount specifically — sdk.Coins.IsAnyGTE compares
		// amounts across all denoms, so a payment in some other denom could
		// satisfy an ngonka fee requirement. We only accept fees denominated
		// in ngonka, so we compare the ngonka amount directly.
		paidNgonka := feeCoins.AmountOf("ngonka")
		if paidNgonka.LT(requiredFee.Amount) {
			return nil, 0, errorsmod.Wrapf(sdkerrors.ErrInsufficientFee,
				"insufficient fee: got %s, required at least %s (gas=%d, min_gas_price=%dngonka)",
				feeCoins, requiredFee, gas, price)
		}

		priority := getTxPriority(feeCoins, gas)
		return feeCoins, priority, nil
	}
}

// getTxPriority calculates transaction priority from fee and gas.
// Higher fee per gas = higher priority.
func getTxPriority(feeCoins sdk.Coins, gas uint64) int64 {
	if gas == 0 {
		return 0
	}

	// Clamp gas to max int64 to avoid overflow in QuoRaw.
	const maxInt64 = int64(^uint64(0) >> 1)
	divisor := maxInt64
	if gas <= uint64(maxInt64) {
		divisor = int64(gas)
	}

	var priority int64
	for _, coin := range feeCoins {
		gasPrice := coin.Amount.QuoRaw(divisor)
		// Clamp to max int64 if the result overflows.
		if gasPrice.GT(math.NewInt(maxInt64)) {
			return maxInt64
		}
		amt := gasPrice.Int64()
		if amt > priority {
			priority = amt
		}
	}
	return priority
}
