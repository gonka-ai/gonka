package inference

import (
	"cmp"
	"slices"

	mathsdk "cosmossdk.io/math"
	"github.com/productscience/inference/x/inference/types"
)

type DelegationAdjustmentParams struct {
	RefusalPenalty         mathsdk.LegacyDec
	NoParticipationPenalty mathsdk.LegacyDec
	DelegationShare        mathsdk.LegacyDec
}

func (p DelegationAdjustmentParams) IsNoOp() bool {
	return p.RefusalPenalty.IsZero() && p.NoParticipationPenalty.IsZero() && p.DelegationShare.IsZero()
}

type pendingTransfer struct {
	from string
	to   string
	rate mathsdk.LegacyDec
}

// PenaltyAccumulator collects penalty fractions from all sources (delegation +
// bootstrap) and applies them as a single additive deduction capped at 1.0.
//
// DIRECT participants are never penalized. For non-DIRECT participants:
//   - REFUSE:   fraction += refusal_penalty per model
//   - NONE:     fraction += no_participation_penalty per model
//   - DELEGATE: transfers delegation_share of original weight to delegate target
//
// Penalties sum across models and cap at 1.0. Transfer delta is based on
// original weight but clamped to the sender's remaining weight to preserve
// weight conservation.
// When all delegation adjustment values are 0, this is a complete no-op.
type PenaltyAccumulator struct {
	penalties      map[string]mathsdk.LegacyDec
	transfers      []pendingTransfer
	originalWeight map[string]int64

	appliedFraction map[string]mathsdk.LegacyDec
	transferIn      map[string]int64
	transferOut     map[string]int64
}

func NewPenaltyAccumulator(participants []*types.ActiveParticipant) *PenaltyAccumulator {
	original := make(map[string]int64, len(participants))
	for _, p := range participants {
		original[p.Index] = p.Weight
	}
	return &PenaltyAccumulator{
		penalties:       make(map[string]mathsdk.LegacyDec),
		originalWeight:  original,
		appliedFraction: make(map[string]mathsdk.LegacyDec),
		transferIn:      make(map[string]int64),
		transferOut:     make(map[string]int64),
	}
}

func (pa *PenaltyAccumulator) AppliedFraction(addr string) mathsdk.LegacyDec {
	if f, ok := pa.appliedFraction[addr]; ok {
		return f
	}
	return mathsdk.LegacyZeroDec()
}

func (pa *PenaltyAccumulator) TransferIn(addr string) int64  { return pa.transferIn[addr] }
func (pa *PenaltyAccumulator) TransferOut(addr string) int64 { return pa.transferOut[addr] }

func (pa *PenaltyAccumulator) AddPenalty(addr string, fraction mathsdk.LegacyDec) {
	if existing, ok := pa.penalties[addr]; ok {
		pa.penalties[addr] = existing.Add(fraction)
	} else {
		pa.penalties[addr] = fraction
	}
}

func (pa *PenaltyAccumulator) AddTransfer(from, to string, rate mathsdk.LegacyDec) {
	pa.transfers = append(pa.transfers, pendingTransfer{from: from, to: to, rate: rate})
}

func (pa *PenaltyAccumulator) Apply(participants []*types.ActiveParticipant) {
	one := mathsdk.LegacyOneDec()
	weightIndex := make(map[string]*types.ActiveParticipant, len(participants))
	for _, p := range participants {
		weightIndex[p.Index] = p
	}

	for _, addr := range sortedKeys(pa.penalties) {
		totalFrac := pa.penalties[addr]
		p := weightIndex[addr]
		if p == nil || pa.originalWeight[addr] <= 0 {
			continue
		}
		if totalFrac.GT(one) {
			totalFrac = one
		}
		pa.appliedFraction[addr] = totalFrac
		penalty := totalFrac.MulInt64(pa.originalWeight[addr]).TruncateInt64()
		p.Weight -= penalty
		if p.Weight < 0 {
			p.Weight = 0
		}
	}

	slices.SortFunc(pa.transfers, func(a, b pendingTransfer) int {
		return cmp.Or(
			cmp.Compare(a.from, b.from),
			cmp.Compare(a.to, b.to),
		)
	})
	for _, t := range pa.transfers {
		from := weightIndex[t.from]
		if from == nil {
			continue
		}
		to := weightIndex[t.to]
		if to == nil {
			continue
		}
		delta := t.rate.MulInt64(pa.originalWeight[t.from]).TruncateInt64()
		if delta > from.Weight {
			delta = from.Weight
		}
		if delta < 0 {
			delta = 0
		}
		from.Weight -= delta
		to.Weight += delta
		pa.transferOut[t.from] += delta
		pa.transferIn[t.to] += delta
	}
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
				if !params.DelegationShare.IsZero() {
					if modelDelegations, ok := dwc.Delegations[modelID]; ok {
						if delegateTo, ok := modelDelegations[addr]; ok {
							acc.AddTransfer(addr, delegateTo, params.DelegationShare)
						}
					}
				}
			}
		}
	}
}
