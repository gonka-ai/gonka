package runtimeconfig

// runnerEventKind classifies outcomes from the gRPC long-poll runner for the
// adaptive supervisor. The chain runner does not emit events.
type runnerEventKind int

const (
	runnerEventApplied runnerEventKind = iota
	runnerEventPollError
	runnerEventUnimplemented
)

// runnerEvent is sent from grpcRunner.run to the adaptive supervisor.
type runnerEvent struct {
	kind runnerEventKind
	err  error
}
