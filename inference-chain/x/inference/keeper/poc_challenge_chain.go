package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/keeper/pocchallenge"
	"github.com/productscience/inference/x/inference/types"
	"github.com/productscience/inference/x/inference/utils"
)

var _ pocchallenge.Chain = (*Keeper)(nil)

func (k Keeper) FixedEpochRewardAmount(epochsSinceGenesis uint64, initialReward uint64, decayRate *types.Decimal) (uint64, error) {
	return CalculateFixedEpochReward(epochsSinceGenesis, initialReward, decayRate)
}

func (k Keeper) SendCoinsFromAccountToModule(ctx context.Context, sender sdk.AccAddress, module string, amt sdk.Coins, memo string) error {
	return k.BankKeeper.SendCoinsFromAccountToModule(ctx, sender, module, amt, memo)
}

func (k Keeper) SendCoinsFromModuleToAccount(ctx context.Context, module string, recipient sdk.AccAddress, amt sdk.Coins, memo string) error {
	return k.BankKeeper.SendCoinsFromModuleToAccount(ctx, module, recipient, amt, memo)
}

func (k Keeper) SendCoinsFromModuleToModule(ctx context.Context, sender, recipient string, amt sdk.Coins, memo string) error {
	return k.BankKeeper.SendCoinsFromModuleToModule(ctx, sender, recipient, amt, memo)
}

func (k Keeper) IsChallengeGenerating(ctx context.Context, addr string) bool {
	return k.PoCChallenge.IsChallengeGenerating(ctx, addr)
}

func (k Keeper) HasOpenChallenge(ctx context.Context, addr string) bool {
	return k.PoCChallenge.HasOpenChallenge(ctx, addr)
}

func (k Keeper) ConfirmationEvaluationSkipSet(ctx context.Context, triggerHeight int64) map[string]struct{} {
	set, err := k.PoCChallenge.ConfirmationEvaluationSkipSet(ctx, triggerHeight)
	if err != nil {
		return nil
	}
	return set
}

func (k Keeper) IsMissedRequestWaived(ctx context.Context, addr string, height int64) bool {
	return pocchallenge.IsMissedRequestWaived(ctx, k.PoCChallenge, addr, height)
}

func (k Keeper) EligibleChallengeVoter(ctx context.Context, epochIndex uint64, modelID, voter string) bool {
	var snapHeight int64
	if ev, ok, err := k.GetActiveConfirmationPoCEvent(ctx); err == nil && ok && ev != nil {
		snapHeight = ev.TriggerHeight
	} else if up, found := k.GetUpcomingEpoch(ctx); found && up != nil {
		snapHeight = up.PocStartBlockHeight
	}
	if snapHeight == 0 {
		return false
	}
	snap, found, err := k.GetPoCValidationSnapshot(ctx, snapHeight)
	if err != nil || !found {
		return false
	}
	for _, mvw := range snap.ModelVotingPowers {
		if mvw == nil || mvw.ModelId != modelID {
			continue
		}
		for _, e := range mvw.VotingPowers {
			if e != nil && e.Address == voter {
				return true
			}
		}
	}
	if k.GetGenesisGuardianEnabled(ctx) {
		for _, addr := range k.GetGenesisGuardianAddresses(ctx) {
			acc, err := utils.OperatorAddressToAccAddress(addr)
			if err == nil && acc == voter {
				return true
			}
		}
	}
	return false
}
