package app

import (
	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/limits"
)

// modelCapacity's per-weight allowance follows the raw chain phase: it bounds what the hosts can do.
type modelCapacity struct {
	capacity     *limits.Capacity
	snapshots    *chain.PhaseObserver
	config       *config.Holder
	participants *limits.ParticipantLimiter
}

func (m modelCapacity) ForModel(model string) limits.ModelCapacity {
	weights := m.ModelWeights(model)
	concurrency := m.config.Load().Limits.Concurrency
	perWeight := concurrency.RequestsPer10000Weight
	if m.snapshots.Snapshot().RequestsBlocked {
		perWeight = concurrency.PoCRequestsPer10000Weight
	}
	return limits.ModelCapacity{
		ScaleFactor:                 weights.ScaleFactor,
		CurrentWeight:               weights.CurrentWeight,
		BaselineWeight:              weights.BaselineWeight,
		MaxConcurrentPer10000Weight: perWeight,
		HostWindowRequests:          m.participants.ModelConcurrency(model),
	}
}

// ModelWeights folds in relaxed mode first, or PoC zeroes every cap. See ../README.md, "Relaxed mode, in one place".
func (m modelCapacity) ModelWeights(model string) limits.ModelWeights {
	return m.capacity.ModelWeights(model, m.config.Load().Modes.BlocksRequests(m.snapshots.Snapshot()))
}

// participantWeightsOf is what each host earned for each model, taking the lower of the two views the chain
// keeps: a current weight above the full one must not buy a wider window. See capacity.md, "What a host's weight buys".
func participantWeightsOf(snapshot chain.PhaseSnapshot) map[string]map[string]float64 {
	weights := make(map[string]map[string]float64, len(snapshot.FullWeightsByModel))
	for model, full := range snapshot.FullWeightsByModel {
		current, byModel := snapshot.CurrentWeightsByModel[model]
		if !byModel {
			current = snapshot.CurrentWeights
		}
		earned := make(map[string]float64, len(full))
		for participant, fullWeight := range full {
			earned[participant] = min(fullWeight, current[participant])
		}
		weights[model] = earned
	}
	return weights
}

// contextWindowsOf is the context length governance reports per model. See capacity.md, "The participant limiter: IOCW".
func contextWindowsOf(snapshot chain.PhaseSnapshot) map[string]int64 {
	windows := make(map[string]int64, len(snapshot.Models))
	for model, params := range snapshot.Models {
		if params.ContextWindow > 0 && params.ContextWindow <= config.MaxContextTokens {
			windows[model] = int64(params.ContextWindow)
		}
	}
	return windows
}
