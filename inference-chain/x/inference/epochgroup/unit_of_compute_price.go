package epochgroup

import (
	"context"
	"github.com/productscience/inference/x/inference/types"
	"sort"
)

type weightedProposal struct {
	Participant string
	Weight      int64
	Price       uint64
}

func (eg *EpochGroup) ComputeUnitOfComputePrice(ctx context.Context, proposals []*types.UnitOfComputePriceProposal, defaultProposal uint64) (uint64, error) {
	members, err := eg.GetGroupMembers(ctx)
	eg.Logger.LogInfo("unitOfCompute: ", types.Pricing, "len(members)", len(members))
	if err != nil {
		return 0, err
	}

	proposalsByMember := make(map[string]uint64)
	for _, proposal := range proposals {
		proposalsByMember[proposal.Participant] = proposal.Price
	}

	weightedProposals := make([]*weightedProposal, 0, len(members))
	for _, member := range members {
		price, exists := proposalsByMember[member.Member.Address]
		if !exists {
			eg.Logger.LogInfo("No proposal found for member. Falling back to default.", types.Pricing, "member", member.Member.Address, "defaultProposal", defaultProposal)
			price = defaultProposal
		}

		proposal := &weightedProposal{
			Participant: member.Member.Address,
			Weight:      getWeight(member),
			Price:       price,
		}

		weightedProposals = append(weightedProposals, proposal)
	}

	eg.Logger.LogInfo("unitOfCompute: ", types.Pricing, "weightedProposals", weightedProposals)

	medianProposal := weightedMedian(weightedProposals)

	return medianProposal, nil
}

func weightedMedian(proposals []*weightedProposal) uint64 {
	// Edge case: if there are no proposals, decide on a default (e.g., 0).
	if len(proposals) == 0 {
		return 0
	}

	// 1. Sum up total weights
	var totalWeight int64
	for _, p := range proposals {
		totalWeight += p.Weight
	}

	// If totalWeight == 0, you might want a special case here as well.
	if totalWeight <= 0 {
		// Return 0 or decide what makes sense for your application
		return 0
	}

	// 2. Sort by Price in ascending order
	sort.Slice(proposals, func(i, j int) bool {
		return proposals[i].Price < proposals[j].Price
	})

	// 3. Traverse until cumulative weight >= half of the total
	// Use (totalWeight + 1) / 2 to ensure correct handling of odd sums.
	half := (totalWeight + 1) / 2

	var cumulative int64
	for _, p := range proposals {
		cumulative += p.Weight
		if cumulative >= half {
			// 4. p.Price is the weighted median
			return p.Price
		}
	}

	// Fallback (should not happen if weights are nonzero)
	return proposals[len(proposals)-1].Price
}
