package host

import (
	"devshard/types"
)

// PruneReason classifies why a host emitted an InferencePruneEvent so the sink
// can record metrics or log per tier.
type PruneReason uint8

const (
	// PruneReasonTerminal is fired after applying a diff that transitioned the
	// inference to a terminal status (StatusValidated, StatusInvalidated, or
	// StatusTimedOut). No further protocol step needs the payload.
	PruneReasonTerminal PruneReason = iota
	// PruneReasonStaleFinished is fired for inferences that linger in
	// StatusFinished after both seal gates clear (nonce gate plus state-clock
	// grace). A late validator that arrives after this prune
	// will get a 404 and is expected to skip silently via ErrValidationSkipped.
	PruneReasonStaleFinished
)

// String returns a stable label suitable for logs/metrics.
func (r PruneReason) String() string {
	switch r {
	case PruneReasonTerminal:
		return "terminal"
	case PruneReasonStaleFinished:
		return "stale_finished"
	default:
		return "unknown"
	}
}

// InferencePruneEvent describes a single inference whose payload can be
// deleted. Adapters should treat the event as advisory: duplicate events for
// the same InferenceID are safe and must be tolerated.
type InferencePruneEvent struct {
	EscrowID    string
	InferenceID uint64
	Reason      PruneReason
	// PayloadEpoch is the epoch under which the executor stored the payload.
	// It mirrors the host's pinned epoch (see WithEpochID) and is therefore
	// constant for the session lifetime.
	PayloadEpoch uint64
	// PayloadEpochKnown is true when PayloadEpoch is reliable. When false the
	// sink should fall back to a current-epoch supplier.
	PayloadEpochKnown bool
}

// PruneEventSink receives prune notifications from the host. Implementations
// must be safe to call from within the host mutex and should not block: the
// expected pattern is to enqueue and return immediately.
type PruneEventSink interface {
	OnInferencePrunable(event InferencePruneEvent)
}

// PruneEventSinkFunc adapts a plain function to PruneEventSink for tests and
// trivial wiring.
type PruneEventSinkFunc func(event InferencePruneEvent)

func (f PruneEventSinkFunc) OnInferencePrunable(event InferencePruneEvent) {
	if f != nil {
		f(event)
	}
}

// isTerminalStatus returns true for inference statuses that no longer require
// payload retention (validated, invalidated, or timed out).
func isTerminalStatus(s types.InferenceStatus) bool {
	switch s {
	case types.StatusValidated, types.StatusInvalidated, types.StatusTimedOut:
		return true
	default:
		return false
	}
}
