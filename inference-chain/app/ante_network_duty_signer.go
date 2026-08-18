package app

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"

	inferencemodulekeeper "github.com/productscience/inference/x/inference/keeper"
	inferencetypes "github.com/productscience/inference/x/inference/types"
)

// NetworkDutySignerDecorator rejects fee-exempt network-duty transactions during
// CheckTx when the actor named in the message is not authorized to perform that
// duty (#1539).
//
// Why an ante gate is needed at all: NetworkDutyFeeBypassDecorator waives fees
// for ~12 duty message types based on the Go type alone, and it runs before
// SigVerificationDecorator. The real authorization for those types lives in the
// message handlers, which SDK 0.53 only runs in DeliverTx — after mempool
// admission and block inclusion. So any funded account could get zero-fee duty
// transactions gossiped, sig-verified by every validator and written into a
// block, only to fail later with "participant is not active".
//
// Why rejecting and not merely withholding the waiver: FeeParams
// .MinGasPriceNgonka is 0 on the live network (set by the v0_2_12 upgrade
// handler, "temporary due to issue in gas estimations"). With a zero minimum,
// GonkaFeeChecker accepts any fee whether or not the bypass flag is set, so
// withholding the waiver on its own does not currently keep anything out of the
// mempool. Withholding is kept as defense in depth for when governance raises
// the minimum; this decorator is what closes the gap today.
//
// CheckTx-only, matching PocPeriodValidationDecorator and
// BridgeExchangeEarlyRejectDecorator: DeliverTx still runs the full handler, so
// mempool admission is tightened without altering the state machine.
type NetworkDutySignerDecorator struct {
	inferenceKeeper *inferencemodulekeeper.Keeper
}

func NewNetworkDutySignerDecorator(ik *inferencemodulekeeper.Keeper) NetworkDutySignerDecorator {
	return NetworkDutySignerDecorator{inferenceKeeper: ik}
}

func (d NetworkDutySignerDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (sdk.Context, error) {
	if simulate {
		return next(ctx, tx, simulate)
	}
	// ReCheckTx is included: a duty actor can be removed from the escrow
	// allowlist while the tx sits in the mempool.
	if !ctx.IsCheckTx() {
		return next(ctx, tx, simulate)
	}
	// Without a keeper we cannot evaluate authorization. Pass through rather
	// than reject so a misconfigured ante chain cannot stall duty traffic; the
	// fee bypass separately fails closed in that case.
	if d.inferenceKeeper == nil {
		return next(ctx, tx, simulate)
	}

	for _, msg := range tx.GetMsgs() {
		if _, err := d.checkMessage(ctx, msg, "", 0); err != nil {
			return ctx, err
		}
	}
	return next(ctx, tx, simulate)
}

// maxMsgExecNestingDepth bounds how far the CheckTx-only ante decorators will
// unwrap nested authz MsgExec wrappers. Shared by NetworkDutySignerDecorator
// and PocPeriodValidationDecorator, which both walk the same structure at the
// same position in the chain.
//
// Production uses exactly one level (the DAPI's warm key), so the limit exists
// only to keep unbounded unwrapping from becoming a DoS surface: ante work
// during CheckTx is not gas-metered, so without a bound an attacker could make
// every node walk an arbitrarily deep message tree for free. Beyond the limit
// the transaction is rejected rather than passed through — at that depth the
// tree cannot be inspected, so it cannot be shown not to carry the message the
// decorator is looking for.
const maxMsgExecNestingDepth = 5

// checkMessage authorizes one message, descending through authz MsgExec
// wrappers. It reports whether the subtree contained a fee-exempt duty, which
// is what decides whether the enclosing MsgExec needs its own grant checked.
//
// executor is the MsgExec grantee that will dispatch msg, or "" when msg is a
// top-level message of the transaction. At top level SigVerificationDecorator
// has already authenticated the declared cosmos.msg.v1.signer, so no grant is
// needed; inside a MsgExec the signature only proves the grantee signed, so the
// grant that DeliverTx would require must be checked here too.
//
// The recursion mirrors authz DispatchActions, which routes a nested MsgExec
// back into itself with the inner grantee: each level requires a grant from the
// next level's signer to the level above. Checking only the innermost duty
// would leave a hole — an attacker could wrap a duty inside a MsgExec naming a
// warm key that genuinely holds the participant's grant, and inherit the
// exemption without holding anything themselves.
//
// Grants are only required when the subtree actually carries a duty, so
// ordinary nested traffic is left to authz and the handlers.
func (d NetworkDutySignerDecorator) checkMessage(ctx sdk.Context, msg sdk.Msg, executor string, depth int) (bool, error) {
	execMsg, isExec := msg.(*authztypes.MsgExec)
	if !isExec {
		if _, isDuty := dutyAuthorizationFor(msg); !isDuty {
			return false, nil
		}
		if err := d.checkDutyActor(ctx, msg); err != nil {
			return true, err
		}
		if executor != "" {
			if err := d.checkExecGrant(ctx, executor, msg); err != nil {
				return true, err
			}
		}
		return true, nil
	}

	if depth >= maxMsgExecNestingDepth {
		d.inferenceKeeper.LogDebug(
			"AnteHandle: NetworkDutySigner - rejecting MsgExec nested past the inspection limit",
			inferencetypes.Messages,
			"depth", depth,
			"grantee", execMsg.Grantee,
		)
		return false, sdkerrors.ErrInvalidRequest.Wrapf(
			"MsgExec nested more than %d levels cannot be authorized during CheckTx", maxMsgExecNestingDepth)
	}

	subtreeHasDuty := false
	for _, innerMsg := range execMsg.Msgs {
		var unwrapped sdk.Msg
		if err := d.inferenceKeeper.Codec().UnpackAny(innerMsg, &unwrapped); err != nil {
			// Undecodable inner message: not provably a duty, so not ours to
			// reject. ValidateBasic / the authz handler will deal with it.
			continue
		}
		innerHasDuty, err := d.checkMessage(ctx, unwrapped, execMsg.Grantee, depth+1)
		if err != nil {
			return innerHasDuty, err
		}
		subtreeHasDuty = subtreeHasDuty || innerHasDuty
	}

	// This MsgExec is itself nested and wraps a duty: the outer executor must
	// hold the grant for MsgExec that authz will require of it in DeliverTx.
	if subtreeHasDuty && executor != "" {
		if !d.inferenceKeeper.HasGrantForMsg(ctx, execMsg.Grantee, executor, sdk.MsgTypeURL(execMsg)) {
			d.inferenceKeeper.LogDebug(
				"AnteHandle: NetworkDutySigner - rejecting nested MsgExec without a grant for the wrapper",
				inferencetypes.Messages,
				"granter", execMsg.Grantee,
				"grantee", executor,
			)
			return true, authztypes.ErrNoAuthorizationFound.Wrapf(
				"grantee %s has no grant from %s for %s", executor, execMsg.Grantee, sdk.MsgTypeURL(execMsg))
		}
	}
	return subtreeHasDuty, nil
}

// checkExecGrant verifies the MsgExec grantee is actually authorized to act for
// the duty actor named in the inner message.
func (d NetworkDutySignerDecorator) checkExecGrant(ctx sdk.Context, grantee string, inner sdk.Msg) error {
	auth, exempt := dutyAuthorizationFor(inner)
	if !exempt {
		return nil
	}
	msgTypeURL := sdk.MsgTypeURL(inner)
	if d.inferenceKeeper.HasGrantForMsg(ctx, auth.actor, grantee, msgTypeURL) {
		return nil
	}
	d.inferenceKeeper.LogDebug(
		"AnteHandle: NetworkDutySigner - rejecting MsgExec duty without an authz grant",
		inferencetypes.Messages,
		"msg_type_url", msgTypeURL,
		"granter", auth.actor,
		"grantee", grantee,
	)
	return authztypes.ErrNoAuthorizationFound.Wrapf(
		"grantee %s has no grant from %s for %s", grantee, auth.actor, msgTypeURL)
}

// checkDutyActor rejects a duty message whose actor is not authorized. The
// actor is read from the message body (Creator, or Settler for escrow
// settlement), never from the tx signer: in warm-key mode the grantee signs
// while the body names the cold account that is the real protocol participant
// (see tx_manager.go broadcastMessagesAtAttempt and cosmosclient.go, which sets
// Creator = icc.Address). Checking the signer would reject that path.
//
// Errors are the registered inference errors, so API-side classifiers can match
// them via types.Err*.Error() instead of raw-log substrings.
func (d NetworkDutySignerDecorator) checkDutyActor(ctx sdk.Context, msg sdk.Msg) error {
	auth, exempt := dutyAuthorizationFor(msg)
	if !exempt {
		return nil
	}

	if auth.escrowAllowList {
		if d.inferenceKeeper.IsAllowedEscrowCreator(ctx, auth.actor) {
			return nil
		}
		d.inferenceKeeper.LogDebug(
			"AnteHandle: NetworkDutySigner - rejecting escrow settlement from non-allowlisted settler",
			inferencetypes.Messages,
			"msg_type_url", sdk.MsgTypeURL(msg),
			"settler", auth.actor,
		)
		return inferencetypes.ErrNotAllowedEscrowCreator.Wrap(auth.actor)
	}

	if d.inferenceKeeper.IsRegisteredParticipant(ctx, auth.actor) {
		return nil
	}
	d.inferenceKeeper.LogDebug(
		"AnteHandle: NetworkDutySigner - rejecting network duty from unregistered participant",
		inferencetypes.Messages,
		"msg_type_url", sdk.MsgTypeURL(msg),
		"creator", auth.actor,
	)
	return inferencetypes.ErrParticipantNotFound.Wrap(auth.actor)
}
