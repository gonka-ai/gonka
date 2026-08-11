package app

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
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
		if err := d.checkMessage(ctx, msg); err != nil {
			return ctx, err
		}
	}
	return next(ctx, tx, simulate)
}

func (d NetworkDutySignerDecorator) checkMessage(ctx sdk.Context, msg sdk.Msg) error {
	// Unwrap exactly one authz MsgExec level, mirroring isNetworkDuty: the DAPI
	// wraps duty messages in MsgExec when a warm key signs for the cold
	// account. Nested MsgExec is not a real-world shape; leave it to the fee
	// bypass (which fails closed) and to the handler.
	if execMsg, ok := msg.(*authztypes.MsgExec); ok {
		for _, innerMsg := range execMsg.Msgs {
			var unwrapped sdk.Msg
			if err := d.inferenceKeeper.Codec().UnpackAny(innerMsg, &unwrapped); err != nil {
				// Undecodable inner message: not provably a duty, so not ours
				// to reject. ValidateBasic / the authz handler will deal with it.
				continue
			}
			if _, isNestedExec := unwrapped.(*authztypes.MsgExec); isNestedExec {
				continue
			}
			if err := d.checkDutyActor(ctx, unwrapped); err != nil {
				return err
			}
			// Inside MsgExec the tx signature only proves the grantee signed,
			// so the actor named in the body is not yet authenticated. Require
			// the grant that authz itself will require in DeliverTx; otherwise
			// any account could name a registered participant as Creator and
			// inherit its exemption.
			if err := d.checkExecGrant(ctx, execMsg.Grantee, unwrapped); err != nil {
				return err
			}
		}
		return nil
	}
	// Direct path: creator/settler is the declared cosmos.msg.v1.signer for
	// every duty type, so SigVerificationDecorator authenticates exactly the
	// address checked here.
	return d.checkDutyActor(ctx, msg)
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
