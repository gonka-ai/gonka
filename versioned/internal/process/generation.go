package process

import "log/slog"

type generationState string

const (
	statusPreparing generationState = "preparing"
	statusStarting  generationState = "starting"
	statusRunning   generationState = "running"
	statusRetiring  generationState = "retiring"
	statusDraining  generationState = "draining"
	statusStopping  generationState = "stopping"
	statusStopped   generationState = "stopped"
	statusFailed    generationState = "failed"
)

const unorderedGenerationPhase = -1

type generationStateSpec struct {
	phase        int
	healthStatus string
	targets      []generationState
}

var generationStateTable = map[generationState]generationStateSpec{
	statusPreparing: {
		phase:        0,
		healthStatus: "starting",
		targets: []generationState{
			statusStarting,
			statusRetiring,
			statusStopping,
			statusFailed,
			statusStopped,
		},
	},
	statusStarting: {
		phase:        1,
		healthStatus: "starting",
		targets: []generationState{
			statusRunning,
			statusRetiring,
			statusStopping,
			statusFailed,
			statusStopped,
		},
	},
	statusRunning: {
		phase:        2,
		healthStatus: "running",
		targets: []generationState{
			statusRetiring,
			statusStopping,
			statusFailed,
			statusStopped,
		},
	},
	statusRetiring: {
		phase:        3,
		healthStatus: "draining",
		targets: []generationState{
			statusDraining,
			statusStopping,
			statusStopped,
		},
	},
	statusDraining: {
		phase:        4,
		healthStatus: "draining",
		targets: []generationState{
			statusStopping,
			statusStopped,
		},
	},
	statusStopping: {
		phase:        5,
		healthStatus: "draining",
		targets:      []generationState{statusStopped},
	},
	statusStopped: {
		phase:        6,
		healthStatus: "stopped",
	},
	statusFailed: {
		phase:        unorderedGenerationPhase,
		healthStatus: "stopped",
		targets: []generationState{
			statusStarting,
			statusRetiring,
			statusStopping,
			statusStopped,
		},
	},
}

func validGenerationTransition(from, to generationState) bool {
	spec, fromKnown := generationStateTable[from]
	_, toKnown := generationStateTable[to]
	if !fromKnown || !toKnown {
		return false
	}
	if from == to {
		return true
	}
	for _, target := range spec.targets {
		if target == to {
			return true
		}
	}
	return false
}

// transitionGenerationLocked advances one child generation. Manager.mu must
// be held so lifecycle changes and route publication remain one transaction.
func transitionGenerationLocked(c *child, next generationState) bool {
	if validGenerationTransition(c.status, next) {
		c.status = next
		return true
	}
	// Drain and shutdown run concurrently. A later phase may win before a
	// slower worker publishes its earlier phase; that stale transition is
	// already satisfied and must not move the generation backwards.
	if generationPhase(c.status) >= 0 &&
		generationPhase(next) >= 0 &&
		generationPhase(c.status) > generationPhase(next) {
		return true
	}
	slog.Error(
		"invalid child generation transition",
		"version", c.version.Name,
		"sha256", c.archiveSHA256,
		"from", c.status,
		"to", next,
	)
	return false
}

func generationPhase(state generationState) int {
	spec, ok := generationStateTable[state]
	if !ok {
		return unorderedGenerationPhase
	}
	return spec.phase
}

func transitionGenerationFailureLocked(c *child) {
	if c.restart {
		transitionGenerationLocked(c, statusFailed)
		return
	}
	transitionGenerationLocked(c, statusStopping)
}

func healthGenerationStatus(state generationState) string {
	spec, ok := generationStateTable[state]
	if !ok {
		return "stopped"
	}
	return spec.healthStatus
}
