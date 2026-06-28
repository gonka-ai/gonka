package keeper

import (
	"fmt"
	"strings"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// RegisterInvariants registers all x/inference module invariants.
func RegisterInvariants(ir sdk.InvariantRegistry, k Keeper) {
	ir.RegisterRoute(types.ModuleName, "actual-cost-within-escrow", ActualCostWithinEscrowInvariant(k))
}

// ActualCostWithinEscrowInvariant asserts ActualCost <= EscrowAmount for every started
// inference; a violation means the shared module pool can be drained. Finish-first transients
// (StartInference not yet processed, EscrowAmount==0) are skipped as benign. O(#inferences).
func ActualCostWithinEscrowInvariant(k Keeper) sdk.Invariant {
	return func(ctx sdk.Context) (string, bool) {
		inferences, err := k.GetAllInference(ctx)
		if err != nil {
			return sdk.FormatInvariant(
				types.ModuleName,
				"actual-cost-within-escrow",
				fmt.Sprintf("failed to load inferences: %v", err),
			), true
		}

		var (
			broken    bool
			violators int
			details   strings.Builder
		)
		for _, inf := range inferences {
			if !inf.StartProcessed() {
				continue
			}
			if inf.ActualCost > inf.EscrowAmount {
				broken = true
				violators++
				if violators <= 10 {
					details.WriteString(fmt.Sprintf(
						"\tinference %s: ActualCost=%d > EscrowAmount=%d\n",
						inf.InferenceId, inf.ActualCost, inf.EscrowAmount,
					))
				}
			}
		}

		msg := ""
		if broken {
			msg = fmt.Sprintf(
				"%d inference(s) violate ActualCost <= EscrowAmount (pool over-commitment):\n%s",
				violators, details.String(),
			)
		}
		return sdk.FormatInvariant(types.ModuleName, "actual-cost-within-escrow", msg), broken
	}
}
