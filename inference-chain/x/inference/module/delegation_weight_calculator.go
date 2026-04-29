package inference

import (
	"slices"

	mathsdk "cosmossdk.io/math"
	"github.com/productscience/inference/x/inference/types"
)

// ParticipationMode defines how a participant relates to a model group.
type ParticipationMode int

const (
	ModeDirect   ParticipationMode = iota // Member of the group (has MLNode deployed)
	ModeRefuse                            // Explicitly refused delegation
	ModeDelegate                          // Delegates consensus weight to a group member
	ModeNone                              // No valid delegation, no refusal, no direct membership
)

func (m ParticipationMode) String() string {
	switch m {
	case ModeDirect:
		return "DIRECT"
	case ModeRefuse:
		return "REFUSE"
	case ModeDelegate:
		return "DELEGATE"
	case ModeNone:
		return "NONE"
	}
	return "UNKNOWN"
}

// GroupData holds per-model group information.
type GroupData struct {
	Members          []string          // addresses of direct group members
	MemberPocWeights map[string]int64  // member -> pocWeight in this group
	ConsensusKoeff   mathsdk.LegacyDec // coefficient for this model
	IsInitialGroup   bool              // exempt from cap
}

// WeightParams holds governance parameters for delegation.
type WeightParams struct {
	WThreshold mathsdk.LegacyDec // min fraction of total weight from members for eligibility
	VMin       int64             // min hosts with non-zero consensus weight
	CapFactor  mathsdk.LegacyDec // fraction of N-1 total network weight allowed per non-initial group
}

// DelegationWeightCalculator sits above PoCWeightCalculator and handles
// cross-group concerns: eligibility, caps, consensus weight, delegation modes,
// and per-group voting power.
type DelegationWeightCalculator struct {
	Groups                     map[string]*GroupData        // model_id -> group data
	ConsensusWeights           map[string]int64             // participant -> ActiveParticipant.Weight from N-1
	UpcomingActiveParticipants map[string]bool              // post-PoC-validation upcoming active participant set
	TotalNetworkWeight         int64                        // sum(ConsensusWeights)
	Delegations                map[string]map[string]string // model_id -> (delegator -> delegate_to)
	Refusals                   map[string]map[string]bool   // model_id -> (participant -> true)
	Params                     WeightParams
}

// participates returns true if p has positive N-1 consensus weight or is in
// the upcoming-epoch active participant set.
func (wc *DelegationWeightCalculator) participates(p string) bool {
	if w, ok := wc.ConsensusWeights[p]; ok && w > 0 {
		return true
	}
	return wc.UpcomingActiveParticipants[p]
}

func buildWeightParams(params types.Params) WeightParams {
	wp := WeightParams{
		WThreshold: mathsdk.LegacyZeroDec(),
		VMin:       0,
		CapFactor:  mathsdk.LegacyZeroDec(),
	}
	if params.DelegationParams != nil {
		dp := params.DelegationParams
		wp.WThreshold = protoDecToLegacy(dp.WThreshold)
		wp.VMin = dp.VMin
		wp.CapFactor = protoDecToLegacy(dp.CapFactor)
	}
	return wp
}

// IsGovernanceApproved checks if a model group exists with a defined coefficient.
func (wc *DelegationWeightCalculator) IsGovernanceApproved(modelID string) bool {
	g, ok := wc.Groups[modelID]
	if !ok {
		return false
	}
	return g.ConsensusKoeff.IsPositive()
}

// MeetsWeightThreshold checks if members' consensus weight >= W_threshold * total network weight.
func (wc *DelegationWeightCalculator) MeetsWeightThreshold(modelID string) bool {
	if wc.Params.WThreshold.IsZero() {
		return true
	}
	g, ok := wc.Groups[modelID]
	if !ok {
		return false
	}
	memberWeight := int64(0)
	for _, m := range g.Members {
		memberWeight += wc.ConsensusWeights[m]
	}
	threshold := wc.Params.WThreshold.MulInt64(wc.TotalNetworkWeight).TruncateInt64()
	return memberWeight >= threshold
}

// MeetsMinHosts checks if at least V_min members have non-zero consensus weight.
func (wc *DelegationWeightCalculator) MeetsMinHosts(modelID string) bool {
	if wc.Params.VMin <= 0 {
		return true
	}
	g, ok := wc.Groups[modelID]
	if !ok {
		return false
	}
	count := int64(0)
	for _, m := range g.Members {
		if wc.ConsensusWeights[m] > 0 {
			count++
		}
	}
	return count >= wc.Params.VMin
}

// IsGroupPreEligible checks all pre-eligibility conditions.
func (wc *DelegationWeightCalculator) IsGroupPreEligible(modelID string) bool {
	return wc.IsGovernanceApproved(modelID) &&
		wc.MeetsWeightThreshold(modelID) &&
		wc.MeetsMinHosts(modelID)
}

func (wc *DelegationWeightCalculator) ProjectedReachableVotingPower(modelID string) int64 {
	g, ok := wc.Groups[modelID]
	if !ok {
		return 0
	}

	memberSet := make(map[string]bool, len(g.Members))
	reachable := int64(0)
	for _, m := range g.Members {
		memberSet[m] = true
		reachable += wc.ConsensusWeights[m]
	}

	for delegator, target := range wc.Delegations[modelID] {
		if !memberSet[target] || memberSet[delegator] {
			continue
		}
		reachable += wc.ConsensusWeights[delegator]
	}

	return reachable
}

func (wc *DelegationWeightCalculator) MeetsReachabilityThreshold(modelID string) bool {
	if wc.TotalNetworkWeight <= 0 {
		return false
	}
	reachable := mathsdk.LegacyNewDec(wc.ProjectedReachableVotingPower(modelID))
	total := mathsdk.LegacyNewDec(wc.TotalNetworkWeight)
	return reachable.MulInt64(3).GT(total.MulInt64(2))
}

// IsGroupEligible checks post-PoC eligibility. VMin counts only established
// members: those who committed this epoch (pocWeight > 0) AND had consensus
// weight in the previous epoch (ConsensusWeights > 0).
//
// For the initial model at genesis, VMin is not enforced until the model has
// previously had >= VMin members with consensus weight. Once reached, VMin is
// enforced permanently. Non-initial models always enforce VMin.
func (wc *DelegationWeightCalculator) IsGroupEligible(modelID string) bool {
	if !wc.IsGovernanceApproved(modelID) {
		return false
	}
	if !wc.MeetsWeightThreshold(modelID) {
		return false
	}
	g := wc.Groups[modelID]
	if wc.Params.VMin > 0 {
		// Count established members: committed this epoch AND had weight in N-1.
		count := int64(0)
		for _, m := range g.Members {
			if g.MemberPocWeights[m] > 0 && wc.ConsensusWeights[m] > 0 {
				count++
			}
		}
		if count >= wc.Params.VMin {
			return true
		}
		// VMin not met. Initial model only: skip enforcement during genesis growth.
		if g.IsInitialGroup && wc.isGenesisBootstrap() {
			return true
		}
		return false
	}
	return true
}

// isGenesisBootstrap returns true if the total chain has fewer than VMin
// participants with previous consensus weight. At genesis nobody has N-1
// weight, so VMin cannot be enforced until the network has grown enough.
func (wc *DelegationWeightCalculator) isGenesisBootstrap() bool {
	prevWithWeight := int64(0)
	for _, w := range wc.ConsensusWeights {
		if w > 0 {
			prevWithWeight++
		}
	}
	return prevWithWeight < wc.Params.VMin
}

// ResolveGroupParticipation returns participation mode for each participant
// in the union of N-1 ConsensusWeights and N UpcomingActiveParticipants, for
// one model group.
//
// Bootstrap direct intent is handled earlier by the bootstrap snapshot and never
// enters this next-epoch calculator. By the time this runs, a participant is
// either DIRECT, REFUSE, DELEGATE, or NONE for the group.
func (wc *DelegationWeightCalculator) ResolveGroupParticipation(modelID string) map[string]ParticipationMode {
	g, ok := wc.Groups[modelID]
	if !ok {
		return nil
	}

	memberSet := make(map[string]bool, len(g.Members))
	for _, m := range g.Members {
		memberSet[m] = true
	}

	participants := make(map[string]struct{}, len(wc.ConsensusWeights)+len(wc.UpcomingActiveParticipants))
	for p := range wc.ConsensusWeights {
		participants[p] = struct{}{}
	}
	for p := range wc.UpcomingActiveParticipants {
		participants[p] = struct{}{}
	}
	sortedParticipants := make([]string, 0, len(participants))
	for p := range participants {
		sortedParticipants = append(sortedParticipants, p)
	}
	slices.Sort(sortedParticipants)

	modes := make(map[string]ParticipationMode)
	for _, p := range sortedParticipants {
		if !wc.participates(p) {
			continue
		}
		if memberSet[p] {
			modes[p] = ModeDirect
			continue
		}
		if refusals, ok := wc.Refusals[modelID]; ok && refusals[p] {
			modes[p] = ModeRefuse
			continue
		}
		if delegations, ok := wc.Delegations[modelID]; ok {
			if target, hasDelegation := delegations[p]; hasDelegation {
				if memberSet[target] && wc.participates(target) {
					modes[p] = ModeDelegate
				} else {
					modes[p] = ModeNone
				}
				continue
			}
		}
		modes[p] = ModeNone
	}
	return modes
}

// ComputeGroupCap returns the maximum consensus weight a non-initial group can
// contribute, expressed as a fraction of the N-1 total network weight.
// Returns -1 (uncapped) for the initial model.
func (wc *DelegationWeightCalculator) ComputeGroupCap(modelID string) int64 {
	g, ok := wc.Groups[modelID]
	if !ok {
		return 0
	}
	if g.IsInitialGroup {
		return -1
	}
	cap := wc.Params.CapFactor.MulInt64(wc.TotalNetworkWeight).TruncateInt64()
	if cap < 0 {
		return 0
	}
	return cap
}

// EligibleGroups returns a sorted list of eligible model IDs.
func (wc *DelegationWeightCalculator) EligibleGroups() []string {
	var eligible []string
	for _, modelID := range sortedKeys(wc.Groups) {
		if wc.IsGroupEligible(modelID) {
			eligible = append(eligible, modelID)
		}
	}
	return eligible
}

type GroupSummary struct {
	ModelID  string
	Coeff    mathsdk.LegacyDec
	RawTotal int64
	Cap      int64
	Scale    mathsdk.LegacyDec
}

// ComputeConsensusWeights produces final ActiveParticipant.Weight for each
// participant across all eligible models, applying coefficients and caps.
// Returns the per-participant weight and a per-group summary for diagnostics.
func (wc *DelegationWeightCalculator) ComputeConsensusWeights(eligibleModels []string) (map[string]int64, []GroupSummary) {
	result := make(map[string]int64)
	summaries := make([]GroupSummary, 0, len(eligibleModels))

	// A sole eligible group has nothing to cap against; treating it as capped
	// collapses its weight to 0 when N-1 was also single-group.
	soleGroup := len(eligibleModels) == 1

	for _, modelID := range eligibleModels {
		g := wc.Groups[modelID]
		if g == nil {
			continue
		}

		rawContributions := make(map[string]int64)
		rawTotal := int64(0)
		for _, m := range g.Members {
			contrib := g.ConsensusKoeff.MulInt64(g.MemberPocWeights[m]).TruncateInt64()
			rawContributions[m] = contrib
			rawTotal += contrib
		}

		cap := int64(-1)
		if !soleGroup {
			cap = wc.ComputeGroupCap(modelID)
		}
		scaleFactor := mathsdk.LegacyOneDec()
		if cap >= 0 && rawTotal > cap && rawTotal > 0 {
			scaleFactor = mathsdk.LegacyNewDec(cap).Quo(mathsdk.LegacyNewDec(rawTotal))
		}

		for _, m := range sortedKeys(rawContributions) {
			scaled := scaleFactor.MulInt64(rawContributions[m]).TruncateInt64()
			result[m] += scaled
		}

		summaries = append(summaries, GroupSummary{
			ModelID:  modelID,
			Coeff:    g.ConsensusKoeff,
			RawTotal: rawTotal,
			Cap:      cap,
			Scale:    scaleFactor,
		})
	}

	return result, summaries
}

// ComputeGroupVotingPowers resolves delegation for one model group and returns
// per-DIRECT-member voting power. A DIRECT member's voting power includes their
// own consensus weight plus all consensus weight delegated to them.
//
// finalWeights are the post-adjustment consensus weights (after delegation adjustment,
// collateral, and power capping).
//
// Returns map[participant_address]voting_power. Only DIRECT members get entries.
//
// Direct membership is read from g.Members (authoritative) rather than from
// the modes map. The modes map is built by ResolveGroupParticipation, which
// skips participants with zero prior-epoch consensus weight — a fresh direct
// member who only earned weight this epoch would therefore be absent from
// modes. Reading modes[m] for such an absent key returns the zero value of
// ParticipationMode, which happens to equal ModeDirect (because the iota
// starts at zero). Relying on that coincidence is fragile: any future enum
// change that shifts ModeDirect away from zero would silently strip fresh
// members of their voting power. Build a local memberSet from g.Members and
// check membership explicitly everywhere below.
func (wc *DelegationWeightCalculator) ComputeGroupVotingPowers(
	modelID string,
	modes map[string]ParticipationMode,
	finalWeights map[string]int64,
) map[string]int64 {
	g, ok := wc.Groups[modelID]
	if !ok {
		return nil
	}

	// Every entry in g.Members is DIRECT by construction: g.Members is the
	// set of participants that committed PoC work for this model in the
	// current epoch. Seed each member's voting power from their final
	// post-adjustment weight.
	memberSet := make(map[string]bool, len(g.Members))
	votingPower := make(map[string]int64, len(g.Members))
	for _, m := range g.Members {
		memberSet[m] = true
		votingPower[m] = finalWeights[m]
	}

	// Add delegated weight. A delegator contributes only if
	// ResolveGroupParticipation explicitly resolved it to DELEGATE for this
	// model, and the target must be an actual direct member of the group.
	delegations := wc.Delegations[modelID]
	for _, delegator := range sortedKeys(delegations) {
		if modes[delegator] != ModeDelegate {
			continue
		}
		target := delegations[delegator]
		if memberSet[target] {
			votingPower[target] += finalWeights[delegator]
		}
	}

	return votingPower
}
