package app

import (
	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	authz "github.com/cosmos/cosmos-sdk/x/authz"

	blstypes "github.com/productscience/inference/x/bls/types"
)

// BlsDealerAttemptKeeper is the x/bls surface used by BlsDealerAttemptDecorator.
type BlsDealerAttemptKeeper interface {
	CheckAndRecordDealerPartAttempt(ctx sdk.Context, msg *blstypes.MsgSubmitDealerPart, recordAttempt bool) error
}

// BlsDealerAttemptDecorator enforces cheap dealing preconditions and the
// governed per-(epoch, participant) attempt ceiling on MsgSubmitDealerPart.
//
// Placed after SigVerificationDecorator. For MsgExec wrappers it verifies a
// live GenericAuthorization grant in every execution mode (not only fee-exempt
// CheckTx) before touching the participant's attempt counter, so unauthorized
// wrappers can never burn an honest participant's attempts.
//
// BaseApp commits the ante store branch before running message handlers, so a
// later crypto rejection inside SubmitDealerPart still consumes the attempt.
type BlsDealerAttemptDecorator struct {
	blsKeeper   BlsDealerAttemptKeeper
	cdc         codec.Codec
	authzKeeper AuthzAuthorizationKeeper
}

func NewBlsDealerAttemptDecorator(
	bk BlsDealerAttemptKeeper,
	cdc codec.Codec,
	ak AuthzAuthorizationKeeper,
) BlsDealerAttemptDecorator {
	return BlsDealerAttemptDecorator{
		blsKeeper:   bk,
		cdc:         cdc,
		authzKeeper: ak,
	}
}

func (d BlsDealerAttemptDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (sdk.Context, error) {
	if d.blsKeeper == nil {
		return next(ctx, tx, simulate)
	}

	recordAttempt := shouldRecordDealerAttempt(ctx)
	for _, msg := range tx.GetMsgs() {
		if err := d.checkMessage(ctx, msg, recordAttempt); err != nil {
			return ctx, err
		}
	}
	return next(ctx, tx, simulate)
}

func (d BlsDealerAttemptDecorator) checkMessage(ctx sdk.Context, msg sdk.Msg, recordAttempt bool) error {
	switch m := msg.(type) {
	case *blstypes.MsgSubmitDealerPart:
		return d.blsKeeper.CheckAndRecordDealerPartAttempt(ctx, m, recordAttempt)

	case *authz.MsgExec:
		return d.checkMsgExec(ctx, m, recordAttempt)
	}
	return nil
}

func (d BlsDealerAttemptDecorator) checkMsgExec(ctx sdk.Context, execMsg *authz.MsgExec, recordAttempt bool) error {
	if execMsg == nil || len(execMsg.Msgs) == 0 {
		return nil
	}
	if !msgExecTouchesDealerPart(d.cdc, execMsg) {
		return nil
	}
	if d.cdc == nil || d.authzKeeper == nil {
		return sdkerrors.ErrInvalidRequest.Wrap("bls dealer attempt check: codec and authz keeper are required")
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
		if nested, ok := inner.(*authz.MsgExec); ok {
			if msgExecTouchesDealerPart(d.cdc, nested) {
				return errNestedMsgExec
			}
			continue
		}
		dealerMsg, ok := inner.(*blstypes.MsgSubmitDealerPart)
		if !ok {
			continue
		}
		if err := dealerMsg.ValidateBasic(); err != nil {
			return err
		}
		if err := d.checkDealerGrant(ctx, grantee, dealerMsg); err != nil {
			return err
		}
		if err := d.blsKeeper.CheckAndRecordDealerPartAttempt(ctx, dealerMsg, recordAttempt); err != nil {
			return err
		}
	}
	return nil
}

func (d BlsDealerAttemptDecorator) checkDealerGrant(
	ctx sdk.Context,
	grantee sdk.AccAddress,
	dealerMsg *blstypes.MsgSubmitDealerPart,
) error {
	granter, err := sdk.AccAddressFromBech32(dealerMsg.Creator)
	if err != nil {
		return err
	}
	if granter.Equals(grantee) {
		return nil
	}
	authorization, _ := d.authzKeeper.GetAuthorization(
		ctx,
		grantee,
		granter,
		sdk.MsgTypeURL(dealerMsg),
	)
	if authorization == nil {
		return authz.ErrNoAuthorizationFound
	}
	if _, ok := authorization.(*authz.GenericAuthorization); !ok {
		return authz.ErrNoAuthorizationFound
	}
	return nil
}

// msgExecTouchesDealerPart reports whether execMsg (at any nesting depth up to
// maxFeeExecDepth+1) carries a MsgSubmitDealerPart. Unrelated MsgExec payloads
// are left untouched by this decorator.
func msgExecTouchesDealerPart(cdc codec.Codec, execMsg *authz.MsgExec) bool {
	return msgExecTouchesDealerPartAt(cdc, execMsg, 0)
}

func msgExecTouchesDealerPartAt(cdc codec.Codec, execMsg *authz.MsgExec, depth int) bool {
	if execMsg == nil || depth > maxFeeExecDepth+1 {
		return false
	}
	dealerTypeURL := sdk.MsgTypeURL((*blstypes.MsgSubmitDealerPart)(nil))
	execTypeURL := sdk.MsgTypeURL((*authz.MsgExec)(nil))
	for _, innerAny := range execMsg.Msgs {
		if innerAny == nil {
			continue
		}
		if innerAny.TypeUrl == dealerTypeURL {
			return true
		}
		if innerAny.TypeUrl == execTypeURL && cdc != nil {
			var nested sdk.Msg
			if err := cdc.UnpackAny(innerAny, &nested); err != nil {
				// Fail closed on unparseable nested MsgExec so it cannot hide a dealer part.
				return true
			}
			if nestedExec, ok := nested.(*authz.MsgExec); ok && msgExecTouchesDealerPartAt(cdc, nestedExec, depth+1) {
				return true
			}
		}
	}
	return false
}

func shouldRecordDealerAttempt(ctx sdk.Context) bool {
	switch ctx.ExecMode() {
	case sdk.ExecModePrepareProposal,
		sdk.ExecModeProcessProposal,
		sdk.ExecModeVoteExtension,
		sdk.ExecModeVerifyVoteExtension:
		return false
	default:
		return true
	}
}
