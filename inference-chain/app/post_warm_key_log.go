package app

import (
	"fmt"

	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authz "github.com/cosmos/cosmos-sdk/x/authz"

	inferencekeeper "github.com/productscience/inference/x/inference/keeper"
)

// maxWarmKeyLogExecDepth bounds the MsgExec nesting the decorator walks. The
// ante handler refuses nested MsgExec, so one level is what reaches here;
// the bound only keeps the walk finite if that changes.
const maxWarmKeyLogExecDepth = 2

// WarmKeyLogDecorator mirrors authz grants and revokes of the warm-key marker
// into the inference module's warm-key log. It runs after the messages
// executed, for a transaction whose messages all succeeded, so the log records
// exactly the grants authz stored. Simulation does the same writes on its
// discarded cache: the gas meter has to see them, or the estimate is short of
// what FinalizeBlock charges. CheckTx does not execute messages. A failure to
// record fails the transaction, which reverts the grant with it.
type WarmKeyLogDecorator struct {
	cdc    codec.Codec
	keeper *inferencekeeper.Keeper
}

func NewWarmKeyLogDecorator(cdc codec.Codec, k *inferencekeeper.Keeper) WarmKeyLogDecorator {
	return WarmKeyLogDecorator{cdc: cdc, keeper: k}
}

func (d WarmKeyLogDecorator) PostHandle(ctx sdk.Context, tx sdk.Tx, simulate, success bool, next sdk.PostHandler) (sdk.Context, error) {
	if !success || !warmKeyLogMeters(ctx.ExecMode()) {
		return next(ctx, tx, simulate, success)
	}
	for _, msg := range tx.GetMsgs() {
		if err := d.record(ctx, msg, 0); err != nil {
			return ctx, err
		}
	}
	return next(ctx, tx, simulate, success)
}

// warmKeyLogMeters reports the modes that execute messages and charge gas for
// them. Finalize persists the log. Simulate charges the same writes and drops
// the cache.
func warmKeyLogMeters(mode sdk.ExecMode) bool {
	return mode == sdk.ExecModeFinalize || mode == sdk.ExecModeSimulate
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
			return nil
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
