package events

import "context"

// EventType identifies the kind of chain event.
type EventType string

const (
	EventInferenceFinished EventType = "inference_finished"
	EventEpochChanged      EventType = "epoch_changed"
)

// Event carries a parsed chain event and its attributes.
type Event struct {
	Type        EventType
	InferenceID string // set for EventInferenceFinished
	EpochID     uint64 // set for EventEpochChanged
}

// Handler is called for each event dispatched by the Listener.
type Handler func(ctx context.Context, event Event)

// Listener subscribes to chain blocks and dispatches parsed events to registered handlers.
// This is a stub — real implementation requires a chain RPC connection.
type Listener struct {
	handlers []Handler
}

// NewListener creates a Listener. rpcURL is not yet used in the stub.
func NewListener(rpcURL string) *Listener {
	return &Listener{}
}

// Register adds a handler that will be called for every incoming event.
func (l *Listener) Register(h Handler) {
	l.handlers = append(l.handlers, h)
}

// Start begins subscribing to chain events. Blocks until ctx is cancelled.
// Stub: returns immediately.
func (l *Listener) Start(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}
