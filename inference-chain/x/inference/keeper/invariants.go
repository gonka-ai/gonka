package keeper

import (
	"context"
	"fmt"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	"github.com/productscience/inference/x/inference/types"
)

// RegisterInvariants wires this module's invariants into the crisis
// module's registry. The simulation framework can also run them via a
// post-run callback; see app/sim_test.go.
//
// Each invariant returns (description, broken). When broken == true, the
// crisis module logs the description and (depending on config) halts the
// chain. In sim, broken == true causes the test to fail with the message.
func RegisterInvariants(ir sdk.InvariantRegistry, k Keeper) {
	ir.RegisterRoute(types.ModuleName, "bank-backs-positive-balance", BankBacksPositiveBalanceInvariant(k))
	ir.RegisterRoute(types.ModuleName, "no-stuck-voting", NoStuckVotingInvariant(k))
	ir.RegisterRoute(types.ModuleName, "effective-epoch-fresh", EffectiveEpochFreshInvariant(k))
	ir.RegisterRoute(types.ModuleName, "active-invalidations-ref-live", ActiveInvalidationsRefLiveInvariant(k))
}

// AllInvariants returns every invariant as a single function. Used by
// the sim post-run callback so a violation logs which invariant fired.
func AllInvariants(k Keeper) sdk.Invariant {
	invs := []struct {
		name string
		fn   sdk.Invariant
	}{
		{"bank-backs-positive-balance", BankBacksPositiveBalanceInvariant(k)},
		{"no-stuck-voting", NoStuckVotingInvariant(k)},
		{"effective-epoch-fresh", EffectiveEpochFreshInvariant(k)},
		{"active-invalidations-ref-live", ActiveInvalidationsRefLiveInvariant(k)},
	}
	return func(ctx sdk.Context) (string, bool) {
		for _, inv := range invs {
			if msg, broken := inv.fn(ctx); broken {
				return sdk.FormatInvariant(types.ModuleName, inv.name, msg), true
			}
		}
		return "", false
	}
}

// --- A1 ----------------------------------------------------------------

// BankBacksPositiveBalanceInvariant verifies that the inference module
// account holds at least as many ngonka coins as the sum of all positive
// participant CoinBalances. Catches: settle/payout flow paying out more
// than the module can back, double-credit in validation share, drain via
// underflow in invalidation refund.
func BankBacksPositiveBalanceInvariant(k Keeper) sdk.Invariant {
	return func(ctx sdk.Context) (string, bool) {
		var positiveSum int64
		err := iteratePositiveBalances(ctx, k, func(_ string, balance int64) {
			positiveSum += balance
		})
		if err != nil {
			return fmt.Sprintf("iterate participants: %v", err), true
		}
		moduleAddr := authtypes.NewModuleAddress(types.ModuleName)
		moduleBalance := k.BankView.SpendableCoin(ctx, moduleAddr, types.BaseCoin).Amount.Int64()
		if moduleBalance < positiveSum {
			return fmt.Sprintf("module account %s has %d %s but participants are owed %d",
				moduleAddr, moduleBalance, types.BaseCoin, positiveSum), true
		}
		return "", false
	}
}

// --- B -----------------------------------------------------------------

// NoStuckVotingInvariant verifies that no Inference is stuck in VOTING
// more than 2 epochs past its own EpochId. Catches: x/group proposal
// failed to execute (quorum miss); inference status transition lost;
// `Revalidation:true` vote never reached.
func NoStuckVotingInvariant(k Keeper) sdk.Invariant {
	return func(ctx sdk.Context) (string, bool) {
		currentEpoch, found := k.GetEffectiveEpochIndex(ctx)
		if !found {
			return "", false
		}
		var stuck string
		iter, err := k.Inferences.Iterate(ctx, nil)
		if err != nil {
			return fmt.Sprintf("iterate inferences: %v", err), true
		}
		defer iter.Close()
		for ; iter.Valid(); iter.Next() {
			inf, err := iter.Value()
			if err != nil {
				return fmt.Sprintf("decode inference: %v", err), true
			}
			if inf.Status != types.InferenceStatus_VOTING {
				continue
			}
			if inf.EpochId+2 < currentEpoch {
				stuck = fmt.Sprintf("inference %s stuck in VOTING since epoch %d (current epoch %d)",
					inf.InferenceId, inf.EpochId, currentEpoch)
				break
			}
		}
		if stuck != "" {
			return stuck, true
		}
		return "", false
	}
}

// --- C -----------------------------------------------------------------

// EffectiveEpochFreshInvariant verifies that EffectiveEpochIndex tracks
// the highest Epoch.Index recorded in the Epochs map within +1 (the
// upcoming-epoch window). Production lifecycle (epoch.go:73, GetUpcomingEpoch):
// the next-to-become-effective epoch is written to Epochs[N+1] during
// the PoC validation stage, BEFORE EffectiveEpochIndex flips on
// onSetNewValidatorsStage. So a single Epoch.Index = EffectiveEpochIndex+1
// is expected and not broken. Two or more pending upcoming epochs are.
//
// Catches: EffectiveEpochIndex stuck while multiple upcoming Epoch rows
// pile up (epoch transition stalled); rollback bug.
func EffectiveEpochFreshInvariant(k Keeper) sdk.Invariant {
	return func(ctx sdk.Context) (string, bool) {
		current, found := k.GetEffectiveEpochIndex(ctx)
		if !found {
			// Pre-genesis state: no current index set yet. Treat as not broken.
			return "", false
		}
		var maxStored uint64
		iter, err := k.Epochs.Iterate(ctx, nil)
		if err != nil {
			return fmt.Sprintf("iterate epochs: %v", err), true
		}
		defer iter.Close()
		for ; iter.Valid(); iter.Next() {
			e, err := iter.Value()
			if err != nil {
				return fmt.Sprintf("decode epoch: %v", err), true
			}
			if e.Index > maxStored {
				maxStored = e.Index
			}
		}
		if current+1 < maxStored {
			return fmt.Sprintf("EffectiveEpochIndex=%d trails max Epoch.Index=%d by more than the one-upcoming-epoch window",
				current, maxStored), true
		}
		return "", false
	}
}

// --- D -----------------------------------------------------------------

// ActiveInvalidationsRefLiveInvariant verifies that every entry in the
// ActiveInvalidations keyset references an existing Inference. Catches:
// inference deleted while still in active-invalidations lifecycle;
// dangling keyset entry from incomplete invalidation cleanup.
//
// ActiveInvalidations is keyed by (validator AccAddress, inferenceId
// string); we check the second element for an existing Inferences entry.
func ActiveInvalidationsRefLiveInvariant(k Keeper) sdk.Invariant {
	return func(ctx sdk.Context) (string, bool) {
		var dangling string
		err := k.ActiveInvalidations.Walk(ctx, nil,
			func(key collections.Pair[sdk.AccAddress, string]) (stop bool, err error) {
				inferenceId := key.K2()
				has, err := k.Inferences.Has(ctx, inferenceId)
				if err != nil {
					return true, err
				}
				if !has {
					dangling = fmt.Sprintf("ActiveInvalidations references missing inference %q",
						inferenceId)
					return true, nil
				}
				return false, nil
			})
		if err != nil {
			return fmt.Sprintf("walk ActiveInvalidations: %v", err), true
		}
		if dangling != "" {
			return dangling, true
		}
		return "", false
	}
}

// --- helpers -----------------------------------------------------------

func iterateParticipants(ctx context.Context, k Keeper, cb func(types.Participant)) error {
	iter, err := k.Participants.Iterate(ctx, nil)
	if err != nil {
		return err
	}
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		p, err := iter.Value()
		if err != nil {
			return err
		}
		cb(p)
	}
	return nil
}

func iteratePositiveBalances(ctx context.Context, k Keeper, cb func(addr string, balance int64)) error {
	return iterateParticipants(ctx, k, func(p types.Participant) {
		if p.CoinBalance > 0 {
			cb(p.Address, p.CoinBalance)
		}
	})
}
