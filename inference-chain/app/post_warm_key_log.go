package app

import (
	"fmt"

	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	authz "github.com/cosmos/cosmos-sdk/x/authz"

	inferencekeeper "github.com/productscience/inference/x/inference/keeper"
)

// maxWarmKeyLogExecDepth bounds the MsgExec nesting the decorator walks. The
// CheckTx ante handler refuses nested MsgExec, but a proposer can include a
// deeper nesting in a block, so a nesting past the bound fails the
// transaction rather than letting a grant or revoke escape the log.
const maxWarmKeyLogExecDepth = 8

// errWarmKeyLogExecTooDeep is a MsgExec nested past maxWarmKeyLogExecDepth.
var errWarmKeyLogExecTooDeep = sdkerrors.ErrInvalidRequest.Wrap("warm-key log: MsgExec nested too deep")

// WarmKeyLogDecorator mirrors authz grants and revokes of the warm-key marker
// into the inference module's warm-key log. It runs after the messages
// executed, in FinalizeBlock and in simulation (so a simulated gas limit
// covers the log's writes), and only for a transaction whose messages all
// succeeded, so the log records exactly the grants authz stored. A failure
// to record fails the transaction, which reverts the grant with it: the log
// and authz never disagree.
type WarmKeyLogDecorator struct {
	cdc    codec.Codec
	keeper *inferencekeeper.Keeper
}

func NewWarmKeyLogDecorator(cdc codec.Codec, k *inferencekeeper.Keeper) WarmKeyLogDecorator {
	return WarmKeyLogDecorator{cdc: cdc, keeper: k}
}

func (d WarmKeyLogDecorator) PostHandle(ctx sdk.Context, tx sdk.Tx, simulate, success bool, next sdk.PostHandler) (sdk.Context, error) {
	if !success || (ctx.ExecMode() != sdk.ExecModeFinalize && ctx.ExecMode() != sdk.ExecModeSimulate) {
		return next(ctx, tx, simulate, success)
	}
	for _, msg := range tx.GetMsgs() {
		if err := d.record(ctx, msg, 0); err != nil {
			return ctx, err
		}
	}
	return next(ctx, tx, simulate, success)
}

func (d WarmKeyLogDecorator) record(ctx sdk.Context, msg sdk.Msg, depth int) error {
	switch m := msg.(type) {
	case *authz.MsgGrant:
		auth, err := m.Grant.GetAuthorization()
		if err != nil {
			return fmt.Errorf("warm-key log: grant authorization: %w", err)
		}
		generic, ok := auth.(*authz.GenericAuthorization)
		if !ok || !inferencekeeper.IsWarmKeyMarker(generic.Msg) {
			return nil
		}
		granter, grantee, err := grantParties(m.Granter, m.Grantee)
		if err != nil {
			return err
		}
		_, err = d.keeper.RecordWarmKeyGrant(ctx, granter, grantee, m.Grant.Expiration)
		return err
	case *authz.MsgRevoke:
		if !inferencekeeper.IsWarmKeyMarker(m.MsgTypeUrl) {
			return nil
		}
		granter, grantee, err := grantParties(m.Granter, m.Grantee)
		if err != nil {
			return err
		}
		_, err = d.keeper.RecordWarmKeyRevoke(ctx, granter, grantee)
		return err
	case *authz.MsgExec:
		if depth >= maxWarmKeyLogExecDepth {
			return errWarmKeyLogExecTooDeep
		}
		for _, innerAny := range m.Msgs {
			var inner sdk.Msg
			if err := d.cdc.UnpackAny(innerAny, &inner); err != nil {
				return fmt.Errorf("warm-key log: unpack MsgExec message: %w", err)
			}
			if err := d.record(ctx, inner, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func grantParties(granter, grantee string) (sdk.AccAddress, sdk.AccAddress, error) {
	granterAddr, err := sdk.AccAddressFromBech32(granter)
	if err != nil {
		return nil, nil, fmt.Errorf("warm-key log: granter: %w", err)
	}
	granteeAddr, err := sdk.AccAddressFromBech32(grantee)
	if err != nil {
		return nil, nil, fmt.Errorf("warm-key log: grantee: %w", err)
	}
	return granterAddr, granteeAddr, nil
}
