package app

import (
	"fmt"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cosmos/cosmos-sdk/baseapp"
	sdk "github.com/cosmos/cosmos-sdk/types"

	inferencemodulekeeper "github.com/productscience/inference/x/inference/keeper"
	inferencetypes "github.com/productscience/inference/x/inference/types"
)

// setGroupGasPrepareProposal keeps the default mempool selection, then drops
// transactions whose declared gas would pass a capped group's block budget.
// ProcessProposal is unchanged: a proposal of individually valid transactions
// is accepted, and FinalizeBlock fails the excess ones before their handlers.
func (app *App) setGroupGasPrepareProposal() {
	base := baseapp.NewDefaultProposalHandler(app.Mempool(), app).PrepareProposalHandler()
	decoder := app.txConfig.TxDecoder()
	keeper := &app.InferenceKeeper
	app.SetPrepareProposal(func(ctx sdk.Context, req *abci.RequestPrepareProposal) (*abci.ResponsePrepareProposal, error) {
		resp, err := base(ctx, req)
		if err != nil || resp == nil {
			return resp, err
		}
		resp.Txs = omitExcessGroupGas(ctx, keeper, decoder, resp.Txs)
		return resp, nil
	})
}

type txSignerIDs interface {
	GetSigners() ([][]byte, error)
}

// omitExcessGroupGas walks txs in proposal order. A transaction that would
// exceed a WASM or IBC block budget is left out, and so is every later
// transaction from one of its signers, so account sequences stay contiguous.
// The running totals are local to this call. They are not written to state.
func omitExcessGroupGas(
	ctx sdk.Context,
	k *inferencemodulekeeper.Keeper,
	decode func([]byte) (sdk.Tx, error),
	raw [][]byte,
) [][]byte {
	if k == nil || decode == nil {
		return raw
	}
	params, err := k.GetParams(ctx)
	if err != nil || params.FeeParams == nil {
		return raw
	}
	fp := params.FeeParams
	used := map[string]uint64{}
	blocked := map[string]struct{}{}
	out := make([][]byte, 0, len(raw))
	for _, bz := range raw {
		tx, decErr := decode(bz)
		if decErr != nil {
			break
		}
		signers, signerErr := proposalSignerIDs(tx)
		if signersBlocked(signers, blocked) {
			blockSigners(signers, blocked)
			continue
		}
		if !groupGasFits(k, fp, tx, used) {
			if signerErr != nil || len(signers) == 0 {
				break
			}
			blockSigners(signers, blocked)
			continue
		}
		if feeTx, ok := tx.(sdk.FeeTx); ok {
			if groups, err := cappedGroupsOf(k, fp, tx); err == nil {
				for _, group := range groups {
					used[group.Name] += feeTx.GetGas()
				}
			}
		}
		out = append(out, bz)
	}
	return out
}

func groupGasFits(k *inferencemodulekeeper.Keeper, fp *inferencetypes.FeeParams, tx sdk.Tx, used map[string]uint64) bool {
	feeTx, ok := tx.(sdk.FeeTx)
	if !ok {
		return false
	}
	groups, err := cappedGroupsOf(k, fp, tx)
	if err != nil {
		return false
	}
	gas := feeTx.GetGas()
	for _, group := range groups {
		if gas > group.MaxGas || inferencetypes.GroupGasExceeds(used[group.Name], gas, group.MaxGas) {
			return false
		}
	}
	return true
}

func cappedGroupsOf(k *inferencemodulekeeper.Keeper, fp *inferencetypes.FeeParams, tx sdk.Tx) ([]*inferencetypes.FeeGroup, error) {
	inners, err := unwrapFeeMsgs(tx.GetMsgs(), k)
	if err != nil {
		return nil, err
	}
	return fp.CappedGroups(inners)
}

func proposalSignerIDs(tx sdk.Tx) ([]string, error) {
	sigTx, ok := tx.(txSignerIDs)
	if !ok {
		return nil, fmt.Errorf("tx %T does not expose signers", tx)
	}
	raw, err := sigTx.GetSigners()
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(raw))
	for i, signer := range raw {
		ids[i] = string(signer)
	}
	return ids, nil
}

func signersBlocked(ids []string, blocked map[string]struct{}) bool {
	for _, id := range ids {
		if _, ok := blocked[id]; ok {
			return true
		}
	}
	return false
}

func blockSigners(ids []string, blocked map[string]struct{}) {
	for _, id := range ids {
		blocked[id] = struct{}{}
	}
}
