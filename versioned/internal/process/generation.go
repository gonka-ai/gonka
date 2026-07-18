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

func validGenerationTransition(from, to generationState) bool {
	if from == to {
		return true
	}
	switch from {
	case statusPreparing:
		return to == statusStarting || to == statusRetiring ||
			to == statusStopping || to == statusFailed || to == statusStopped
	case statusStarting:
		return to == statusRunning || to == statusRetiring ||
			to == statusStopping || to == statusFailed || to == statusStopped
	case statusRunning:
		return to == statusRetiring || to == statusStopping ||
			to == statusFailed || to == statusStopped
	case statusRetiring:
		return to == statusDraining || to == statusStopping || to == statusStopped
	case statusDraining:
		return to == statusStopping || to == statusStopped
	case statusStopping:
		return to == statusStopped
	case statusFailed:
		return to == statusStarting || to == statusRetiring ||
			to == statusStopping || to == statusStopped
	case statusStopped:
		return false
	default:
		return false
	}
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
	switch state {
	case statusPreparing:
		return 0
	case statusStarting:
		return 1
	case statusRunning:
		return 2
	case statusRetiring:
		return 3
	case statusDraining:
		return 4
	case statusStopping:
		return 5
	case statusStopped:
		return 6
	default:
		return -1
	}
}

func transitionGenerationFailureLocked(c *child) {
	if c.restart {
		transitionGenerationLocked(c, statusFailed)
		return
	}
	transitionGenerationLocked(c, statusStopping)
}

func healthGenerationStatus(state generationState) string {
	switch state {
	case statusRunning:
		return "running"
	case statusRetiring, statusDraining, statusStopping:
		return "draining"
	case statusPreparing, statusStarting:
		return "starting"
	default:
		return "stopped"
	}
}
