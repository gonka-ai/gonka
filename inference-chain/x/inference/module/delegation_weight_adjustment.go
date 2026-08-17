package inference

import (
	"cmp"
	"slices"

	mathsdk "cosmossdk.io/math"
	"github.com/productscience/inference/x/inference/types"
	"github.com/shopspring/decimal"
)

type DelegationAdjustmentParams struct {
	RefusalPenalty         mathsdk.LegacyDec
	NoParticipationPenalty mathsdk.LegacyDec
	DelegationShare        mathsdk.LegacyDec
}

func (p DelegationAdjustmentParams) IsNoOp() bool {
	return p.RefusalPenalty.IsZero() && p.NoParticipationPenalty.IsZero() && p.DelegationShare.IsZero()
}

// PenaltyAccumulator collects penalty fractions from all sources (delegation +
// bootstrap) and emits them as reward-only penalties capped at 1.0.
//
// DIRECT participants are never penalized. For non-DIRECT participants:
//   - REFUSE:   fraction += refusal_penalty per model
//   - NONE:     fraction += no_participation_penalty per model
//
// Penalties sum across models and cap at 1.0.
// When all delegation adjustment values are 0, this is a complete no-op.
type PenaltyAccumulator struct {
	penalties      map[string]mathsdk.LegacyDec
	originalWeight map[string]int64
}

func NewPenaltyAccumulator(participants []*types.ActiveParticipant) *PenaltyAccumulator {
	original := make(map[string]int64, len(participants))
	for _, p := range participants {
		original[p.Index] = p.Weight
	}
	return &PenaltyAccumulator{
		penalties:      make(map[string]mathsdk.LegacyDec),
		originalWeight: original,
	}
}

func (pa *PenaltyAccumulator) AppliedFraction(addr string) mathsdk.LegacyDec {
	if pa == nil {
		return mathsdk.LegacyZeroDec()
	}
	f, ok := pa.penalties[addr]
	if !ok {
		return mathsdk.LegacyZeroDec()
	}
	one := mathsdk.LegacyOneDec()
	if f.GT(one) {
		return one
	}
	return f
}

func (pa *PenaltyAccumulator) AddPenalty(addr string, fraction mathsdk.LegacyDec) {
	if existing, ok := pa.penalties[addr]; ok {
		pa.penalties[addr] = existing.Add(fraction)
	} else {
		pa.penalties[addr] = fraction
	}
}

func (pa *PenaltyAccumulator) RewardPenalties() []*types.DelegationRewardPenalty {
	if pa == nil {
		return nil
	}
	penalties := make([]*types.DelegationRewardPenalty, 0, len(pa.penalties))
	for _, addr := range sortedKeys(pa.penalties) {
		if pa.originalWeight[addr] <= 0 {
			continue
		}
		fraction := pa.AppliedFraction(addr)
		if fraction.IsZero() {
			continue
		}
		penalties = append(penalties, &types.DelegationRewardPenalty{
			Participant:     addr,
			PenaltyFraction: legacyDecToProto(fraction),
		})
	}
	return penalties
}

type DelegationRewardTransfers struct {
	transfers []*types.DelegationRewardTransfer
}

func (rt *DelegationRewardTransfers) Records() []*types.DelegationRewardTransfer {
	if rt == nil {
		return nil
	}
	return rt.transfers
}

func legacyDecToProto(d mathsdk.LegacyDec) *types.Decimal {
	parsed, err := decimal.NewFromString(d.String())
	if err != nil {
		return &types.Decimal{Value: 0, Exponent: 0}
	}
	return types.DecimalFromDecimal(parsed)
}

func penaltyStartReached(modelID string, upcomingEpochIndex uint64, penaltyStartEpochByModel map[string]uint64) bool {
	startEpoch, found := penaltyStartEpochByModel[modelID]
	if !found {
		return true
	}
	return upcomingEpochIndex >= startEpoch
}

// AccumulateDelegationPenalties adds penalty fractions for each participant's
// non-DIRECT modes across all eligible model groups.
func AccumulateDelegationPenalties(
	acc *PenaltyAccumulator,
	dwc *DelegationWeightCalculator,
	eligibleModels []string,
	modes map[string]map[string]ParticipationMode,
	params DelegationAdjustmentParams,
	upcomingEpochIndex uint64,
	penaltyStartEpochByModel map[string]uint64,
) {
	if params.IsNoOp() {
		return
	}

	for _, modelID := range eligibleModels {
		if !penaltyStartReached(modelID, upcomingEpochIndex, penaltyStartEpochByModel) {
			continue
		}
		groupModes := modes[modelID]
		if groupModes == nil {
			continue
		}

		for _, addr := range sortedKeys(groupModes) {
			mode := groupModes[addr]
			if acc.originalWeight[addr] <= 0 {
				continue
			}

			switch mode {
			case ModeDirect:
				continue
			case ModeRefuse:
				if !params.RefusalPenalty.IsZero() {
					acc.AddPenalty(addr, params.RefusalPenalty)
				}
			case ModeNone:
				if !params.NoParticipationPenalty.IsZero() {
					acc.AddPenalty(addr, params.NoParticipationPenalty)
				}
			case ModeDelegate:
				continue
			}
		}
	}
}

func BuildDelegationRewardTransfers(
	dwc *DelegationWeightCalculator,
	eligibleModels []string,
	modes map[string]map[string]ParticipationMode,
	params DelegationAdjustmentParams,
	upcomingEpochIndex uint64,
	penaltyStartEpochByModel map[string]uint64,
) *DelegationRewardTransfers {
	if dwc == nil || params.DelegationShare.IsZero() {
		return &DelegationRewardTransfers{}
	}

	var transfers []*types.DelegationRewardTransfer
	for _, modelID := range eligibleModels {
		if !penaltyStartReached(modelID, upcomingEpochIndex, penaltyStartEpochByModel) {
			continue
		}
		groupModes := modes[modelID]
		if groupModes == nil {
			continue
		}
		modelDelegations := dwc.Delegations[modelID]
		if modelDelegations == nil {
			continue
		}

		for _, addr := range sortedKeys(groupModes) {
			if groupModes[addr] != ModeDelegate {
				continue
			}
			delegateTo := modelDelegations[addr]
			if delegateTo == "" {
				continue
			}
			transfers = append(transfers, &types.DelegationRewardTransfer{
				ModelId: modelID,
				From:    addr,
				To:      delegateTo,
				Share:   legacyDecToProto(params.DelegationShare),
			})
		}
	}

	sortDelegationRewardTransfers(transfers)
	return &DelegationRewardTransfers{transfers: transfers}
}

func sortDelegationRewardTransfers(transfers []*types.DelegationRewardTransfer) {
	slices.SortFunc(transfers, func(a, b *types.DelegationRewardTransfer) int {
		return cmp.Or(
			cmp.Compare(a.GetFrom(), b.GetFrom()),
			cmp.Compare(a.GetTo(), b.GetTo()),
			cmp.Compare(a.GetModelId(), b.GetModelId()),
		)
	})
}
