package app

import (
	"context"
	"time"

	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	authz "github.com/cosmos/cosmos-sdk/x/authz"
)

// errNestedMsgExec is returned when an admission decorator encounters nested
// MsgExec. Production wrapping is one level (warm key → participant).
var errNestedMsgExec = sdkerrors.ErrInvalidRequest.Wrap("nested MsgExec is not allowed")

// AuthzAuthorizationKeeper is the grant-lookup surface used by
// MsgExecAuthorizationDecorator. *authzkeeper.Keeper implements this.
type AuthzAuthorizationKeeper interface {
	GetAuthorization(ctx context.Context, grantee, granter sdk.AccAddress, msgType string) (authz.Authorization, *time.Time)
}

// MsgExecAuthorizationDecorator rejects fee-exempt MsgExec transactions that
// lack a live GenericAuthorization grant for each inner message.
//
// It runs only during CheckTx and only when NetworkDutyFeeBypassDecorator
// applied the fee bypass. Paid MsgExec still goes through x/authz execution
// in DeliverTx. Calling Authorization.Accept here is avoided because some
// implementations mutate or delete the grant during real execution.
type MsgExecAuthorizationDecorator struct {
	cdc         codec.Codec
	authzKeeper AuthzAuthorizationKeeper
}

func NewMsgExecAuthorizationDecorator(cdc codec.Codec, ak AuthzAuthorizationKeeper) MsgExecAuthorizationDecorator {
	return MsgExecAuthorizationDecorator{
		cdc:         cdc,
		authzKeeper: ak,
	}
}

func (d MsgExecAuthorizationDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (sdk.Context, error) {
	if simulate {
		return next(ctx, tx, simulate)
	}
	if !ctx.IsCheckTx() {
		return next(ctx, tx, simulate)
	}
	if !IsNetworkDutyBypassed(ctx) {
		return next(ctx, tx, simulate)
	}

	for _, msg := range tx.GetMsgs() {
		execMsg, ok := msg.(*authz.MsgExec)
		if !ok {
			continue
		}
		if err := d.checkFeeExemptMsgExec(ctx, execMsg); err != nil {
			return ctx, err
		}
	}

	return next(ctx, tx, simulate)
}

func (d MsgExecAuthorizationDecorator) checkFeeExemptMsgExec(ctx sdk.Context, execMsg *authz.MsgExec) error {
	if d.cdc == nil || d.authzKeeper == nil {
		return sdkerrors.ErrInvalidRequest.Wrap("msgexec authorization check: codec and authz keeper are required")
	}

	grantee, err := sdk.AccAddressFromBech32(execMsg.Grantee)
	if err != nil {
		return err
	}

	for _, innerAny := range execMsg.Msgs {
		var inner sdk.Msg
		if err := d.cdc.UnpackAny(innerAny, &inner); err != nil {
			return err
		}
		if _, nested := inner.(*authz.MsgExec); nested {
			return errNestedMsgExec
		}
		if err := d.checkInnerGrant(ctx, grantee, inner); err != nil {
			return err
		}
	}

	return nil
}

func (d MsgExecAuthorizationDecorator) checkInnerGrant(ctx sdk.Context, grantee sdk.AccAddress, inner sdk.Msg) error {
	signers, _, err := d.cdc.GetMsgV1Signers(inner)
	if err != nil {
		return err
	}
	if len(signers) != 1 {
		return authz.ErrAuthorizationNumOfSigners
	}

	granter := sdk.AccAddress(signers[0])
	if granter.Equals(grantee) {
		return nil
	}

	authorization, _ := d.authzKeeper.GetAuthorization(
		ctx,
		grantee,
		granter,
		sdk.MsgTypeURL(inner),
	)
	if authorization == nil {
		return authz.ErrNoAuthorizationFound
	}
	if _, ok := authorization.(*authz.GenericAuthorization); !ok {
		return authz.ErrNoAuthorizationFound
	}
	return nil
}
