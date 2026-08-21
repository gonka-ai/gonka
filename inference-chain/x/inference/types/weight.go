package types

import (
	"math/big"
	"slices"

	mathsdk "cosmossdk.io/math"
)

// EffectiveConfirmedWeight scales a participant's consensus weight by the
// confirmed fraction observed via confirmation PoC (cPoC):
//
//	effective = weight * confirmationWeight / rawConfirmationTotal
//
// confirmationWeight and rawConfirmationTotal must both be computed with the
// same per-model confirmation weight coefficients (see
// ConfirmationWeightOfModelNodesWithCoefficients) so the ratio is a pure,
// coefficient-normalized fraction in [0, 1]. The result is clamped to [0, weight].
//
// A non-positive rawConfirmationTotal yields 0 (nothing confirmable). When every
// confirmable model has trusted voting power this is also the settlement and cap
// result. Mixed trusted/untrusted models use EffectiveWeightFromModels so the two
// paths can apply different policies to untrusted weight.
func EffectiveConfirmedWeight(weight, confirmationWeight, rawConfirmationTotal int64) int64 {
	if weight <= 0 {
		return 0
	}
	if rawConfirmationTotal <= 0 {
		return 0
	}
	if confirmationWeight < 0 {
		confirmationWeight = 0
	}
	// Ratio >= 1 is clamped to weight. Do this before Int64() so an
	// over-confirmed MaxInt64 weight cannot wrap to a negative. After this
	// guard the quotient is strictly less than weight, so it always fits in int64.
	if confirmationWeight >= rawConfirmationTotal {
		return weight
	}
	ew := big.NewInt(confirmationWeight)
	ew.Mul(ew, big.NewInt(weight))
	ew.Div(ew, big.NewInt(rawConfirmationTotal))
	return ew.Int64()
}

// EffectiveWeightPolicy selects how models without trusted cPoC voting power
// contribute to EffectiveConfirmedWeight.
type EffectiveWeightPolicy int

const (
	// RewardWeightPolicy keeps untrusted models' raw scaled weight in the
	// numerator. Lack of trust voting power does not itself remove rewards.
	RewardWeightPolicy EffectiveWeightPolicy = iota
	// TrustWeightPolicy contributes zero for untrusted/unconfirmed models, so
	// they cannot grow next epoch's CapWeight until trusted confirmation exists.
	TrustWeightPolicy
)

func ConfirmationWeightCoefficients(scales []*ConfirmationWeightScale) map[string]mathsdk.LegacyDec {
	return confirmationCoefficients(scales)
}

// FilterTrustedConfirmationScales returns the scales used for cPoC confirmation
// accounting. Legacy epochs (accountingSeparated == false) keep every scale.
func FilterTrustedConfirmationScales(scales []*ConfirmationWeightScale, accountingSeparated bool) []*ConfirmationWeightScale {
	if !accountingSeparated {
		return scales
	}
	trusted := make([]*ConfirmationWeightScale, 0, len(scales))
	for _, scale := range scales {
		if scale != nil && scale.HasTrustedVotingPower {
			trusted = append(trusted, scale)
		}
	}
	return trusted
}

// ConfirmationWeightCoefficientsTrusted is the coefficient map for cPoC
// confirmation (numerator in trusted-model units). Legacy epochs use every scale.
func ConfirmationWeightCoefficientsTrusted(scales []*ConfirmationWeightScale, accountingSeparated bool) map[string]mathsdk.LegacyDec {
	return confirmationCoefficients(FilterTrustedConfirmationScales(scales, accountingSeparated))
}

// EffectiveWeightFromModels applies the cap-only policy: every real-weight
// model stays in the denominator, while untrusted models are included in the
// reward numerator and excluded from the trust/cap numerator.
//
// trustedConfirmationWeight must be in trusted-model units (initialized and
// updated with ConfirmationWeightCoefficientsTrusted). Legacy epochs that did
// not separate accounting treat every scale as trusted, which reproduces
// EffectiveConfirmedWeight(rootWeight, trustedConfirmationWeight, rawAll).
func EffectiveWeightFromModels(
	rootWeight int64,
	scales []*ConfirmationWeightScale,
	accountingSeparated bool,
	modelNodes map[string][]*MLNodeInfo,
	trustedConfirmationWeight int64,
	policy EffectiveWeightPolicy,
) int64 {
	rawAll := ConfirmationWeightOfModelNodesWithCoefficients(modelNodes, confirmationCoefficients(scales))
	rawTrusted := ConfirmationWeightOfModelNodesWithCoefficients(
		modelNodes,
		ConfirmationWeightCoefficientsTrusted(scales, accountingSeparated),
	)
	rawUntrusted := rawAll - rawTrusted
	if rawUntrusted < 0 {
		rawUntrusted = 0
	}

	confirmedTrusted := trustedConfirmationWeight
	if confirmedTrusted < 0 {
		confirmedTrusted = 0
	}
	if confirmedTrusted > rawTrusted {
		confirmedTrusted = rawTrusted
	}

	numerator := confirmedTrusted
	if policy == RewardWeightPolicy {
		numerator += rawUntrusted
	}
	return EffectiveConfirmedWeight(rootWeight, numerator, rawAll)
}

func ConfirmationWeightOfParticipant(p *ActiveParticipant, scales []*ConfirmationWeightScale) int64 {
	return ConfirmationWeightOfParticipantWithCoefficients(p, confirmationCoefficients(scales))
}

func ConfirmationWeightOfParticipantWithCoefficients(
	p *ActiveParticipant,
	coefficients map[string]mathsdk.LegacyDec,
) int64 {
	if p == nil {
		return 0
	}
	modelNodes := make(map[string][]*MLNodeInfo, len(p.Models))
	for i, modelID := range p.Models {
		if modelID == "" || i >= len(p.MlNodes) || p.MlNodes[i] == nil {
			continue
		}
		modelNodes[modelID] = append(modelNodes[modelID], p.MlNodes[i].MlNodes...)
	}
	return ConfirmationWeightOfModelNodesWithCoefficients(modelNodes, coefficients)
}

func ConfirmationWeightOfModelNodes(modelNodes map[string][]*MLNodeInfo, scales []*ConfirmationWeightScale) int64 {
	return ConfirmationWeightOfModelNodesWithCoefficients(modelNodes, confirmationCoefficients(scales))
}

func ConfirmationWeightOfModelNodesWithCoefficients(
	modelNodes map[string][]*MLNodeInfo,
	coefficients map[string]mathsdk.LegacyDec,
) int64 {
	total := int64(0)

	modelIDs := make([]string, 0, len(modelNodes))
	for modelID := range modelNodes {
		modelIDs = append(modelIDs, modelID)
	}
	slices.Sort(modelIDs)

	for _, modelID := range modelIDs {
		coeff, ok := coefficients[modelID]
		if !ok {
			continue
		}
		rawModel := int64(0)
		for _, node := range modelNodes[modelID] {
			if node != nil {
				rawModel += node.PocWeight
			}
		}
		total += coeff.MulInt64(rawModel).TruncateInt64()
	}
	return total
}

func confirmationCoefficients(scales []*ConfirmationWeightScale) map[string]mathsdk.LegacyDec {
	coefficients := make(map[string]mathsdk.LegacyDec, len(scales))
	for _, scale := range scales {
		if scale == nil || scale.ModelId == "" {
			continue
		}
		coefficients[scale.ModelId] = confirmationScaleFactor(scale)
	}
	return coefficients
}

func confirmationScaleFactor(scale *ConfirmationWeightScale) mathsdk.LegacyDec {
	if scale == nil {
		return mathsdk.LegacyOneDec()
	}
	return scale.WeightScaleFactor.LegacyDecOrOne()
}
