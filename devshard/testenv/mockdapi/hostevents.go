package mockdapi

import (
	"context"
	"sync"
	"time"

	"common/nodemanager/gen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"devshard/chainoracle/params"
)

// hostEventsMaxWaitCap bounds server-side long-poll duration in the mock.
const hostEventsMaxWaitCap = 30 * time.Second

// hostEventRing is a minimal append-only log of host events with a subscribe /
// notify registry for the GetHostEvents long-poll. It mirrors the production
// dapi HostEventRing semantics scoped to what the escrow long-poll warm scenario
// needs (escrow-created events feeding the devshardd escrow long-poll warm).
type hostEventRing struct {
	mu         sync.Mutex
	generation uint64
	events     []*gen.HostEvent
	nextSeq    uint64
	waiters    map[chan struct{}]struct{}
}

func newHostEventRing() *hostEventRing {
	return &hostEventRing{
		generation: uint64(time.Now().UnixNano()),
		nextSeq:    1,
		waiters:    make(map[chan struct{}]struct{}),
	}
}

// AppendEscrowCreated records an escrow-created event and wakes waiters.
func (r *hostEventRing) AppendEscrowCreated(p *gen.EscrowPayload) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	ev := &gen.HostEvent{
		Seq:            r.nextSeq,
		Kind:           gen.HostEventKind_HOST_EVENT_KIND_ESCROW_CREATED,
		ObservedAtUnix: time.Now().Unix(),
		Escrow:         p,
	}
	r.nextSeq++
	r.events = append(r.events, ev)
	for ch := range r.waiters {
		close(ch)
		delete(r.waiters, ch)
	}
	return ev.GetSeq()
}

func (r *hostEventRing) subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{})
	r.mu.Lock()
	r.waiters[ch] = struct{}{}
	r.mu.Unlock()
	return ch, func() {
		r.mu.Lock()
		delete(r.waiters, ch)
		r.mu.Unlock()
	}
}

// since returns subscribed events with seq > cursor plus the head seq and
// generation. reset is true on generation mismatch or a cursor ahead of head.
func (r *hostEventRing) since(cursor, clientGen uint64, want map[gen.HostEventKind]struct{}) (evs []*gen.HostEvent, head, generation uint64, reset bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	head = r.nextSeq - 1
	generation = r.generation
	if clientGen != 0 && clientGen != r.generation {
		return nil, head, generation, true
	}
	if cursor > head {
		return nil, head, generation, true
	}
	for _, ev := range r.events {
		if ev.GetSeq() <= cursor {
			continue
		}
		if _, ok := want[ev.GetKind()]; !ok {
			continue
		}
		evs = append(evs, ev)
	}
	return evs, head, generation, false
}

// nodeManagerServer augments the chainoracle params NodeManager (GetRuntimeConfig
// + AcquireMLNode + …) with a mock GetHostEvents producer for the escrow
// long-poll warm scenario. Other RPCs are inherited from the embedded params server.
type nodeManagerServer struct {
	*params.Server
	ring *hostEventRing
}

func newNodeManagerServer(paramsSrv *params.Server, ring *hostEventRing) *nodeManagerServer {
	return &nodeManagerServer{Server: paramsSrv, ring: ring}
}

// GetHostEvents implements the NodeManager long-poll over the mock ring.
func (s *nodeManagerServer) GetHostEvents(ctx context.Context, req *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
	if s.ring == nil {
		return nil, status.Error(codes.FailedPrecondition, "host events: ring not configured")
	}
	if len(req.GetSubscribe()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "host events: subscribe must be non-empty")
	}
	want := make(map[gen.HostEventKind]struct{}, len(req.GetSubscribe()))
	for _, k := range req.GetSubscribe() {
		want[k] = struct{}{}
	}
	cursor := req.GetCursor()
	clientGen := req.GetGeneration()
	maxWait := clampMockHostEventsMaxWait(req.GetMaxWaitSeconds())

	for {
		wake, release := s.ring.subscribe()
		evs, head, generation, reset := s.ring.since(cursor, clientGen, want)
		if reset {
			release()
			return &gen.GetHostEventsResponse{NeedsReset: true, NextCursor: head, Generation: generation}, nil
		}
		if len(evs) > 0 {
			release()
			return &gen.GetHostEventsResponse{Events: evs, NextCursor: head, Generation: generation}, nil
		}
		if maxWait <= 0 {
			release()
			return &gen.GetHostEventsResponse{Unchanged: true, NextCursor: head, Generation: generation}, nil
		}
		select {
		case <-ctx.Done():
			release()
			return nil, status.FromContextError(ctx.Err()).Err()
		case <-wake:
			release()
			// Woken by an append; loop and re-read since the cursor.
		case <-time.After(maxWait):
			release()
			evs2, head2, gen2, reset2 := s.ring.since(cursor, clientGen, want)
			if reset2 {
				return &gen.GetHostEventsResponse{NeedsReset: true, NextCursor: head2, Generation: gen2}, nil
			}
			if len(evs2) > 0 {
				return &gen.GetHostEventsResponse{Events: evs2, NextCursor: head2, Generation: gen2}, nil
			}
			return &gen.GetHostEventsResponse{Unchanged: true, NextCursor: head2, Generation: gen2}, nil
		}
	}
}

func clampMockHostEventsMaxWait(sec uint32) time.Duration {
	if sec == 0 {
		return 0
	}
	d := time.Duration(sec) * time.Second
	if d > hostEventsMaxWaitCap {
		return hostEventsMaxWaitCap
	}
	return d
}
