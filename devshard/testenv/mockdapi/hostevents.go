package mockdapi

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"

	"common/chainoracle/blocks"
	"common/chainoracle/blocks/nmrpc"
	"common/nodemanager/gen"
	"devshard/chainoracle/params"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// hostEventsMaxWaitCap bounds server-side long-poll duration in the mock.
const hostEventsMaxWaitCap = 30 * time.Second

// maxHostEventsPage is the most events one GetHostEvents response may hold.
const maxHostEventsPage = 1000

var errHostEventsNoHeight = errors.New("no height committed")

// hostEventRing is an in-memory append-only log that answers the production
// GetHostEvents cursor table. It is not a store.
type hostEventRing struct {
	mu          sync.Mutex
	generation  uint64
	events      []*gen.HostEvent
	nextSeq     uint64
	emitted     int64
	firstHeight int64
	blockSeq    map[int64]uint64
	stalled     bool
	waiters     map[chan struct{}]struct{}
}

func newHostEventRing() *hostEventRing {
	return &hostEventRing{
		generation: uint64(time.Now().UnixNano()),
		nextSeq:    1,
		blockSeq:   make(map[int64]uint64),
		waiters:    make(map[chan struct{}]struct{}),
	}
}

func (r *hostEventRing) headLocked() uint64 {
	if r.nextSeq == 0 {
		return 0
	}
	return r.nextSeq - 1
}

func (r *hostEventRing) wakeLocked() {
	for ch := range r.waiters {
		close(ch)
		delete(r.waiters, ch)
	}
}

func (r *hostEventRing) appendLocked(ev *gen.HostEvent) uint64 {
	ev.Seq = r.nextSeq
	r.nextSeq++
	if ev.ObservedAtUnix == 0 {
		ev.ObservedAtUnix = time.Now().Unix()
	}
	r.events = append(r.events, ev)
	r.wakeLocked()
	return ev.Seq
}

// AppendEscrowCreated records an escrow-created event and wakes waiters.
func (r *hostEventRing) AppendEscrowCreated(p *gen.EscrowPayload) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.appendLocked(&gen.HostEvent{
		Kind:   gen.HostEventKind_HOST_EVENT_KIND_ESCROW_CREATED,
		Escrow: p,
	})
}

// AppendWarmKey records a grant or a revoke and wakes waiters.
func (r *hostEventRing) AppendWarmKey(p *gen.WarmKeyPayload) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	kind := gen.HostEventKind_HOST_EVENT_KIND_WARM_KEY_GRANTED
	if p != nil && p.GetEnd() != "" {
		kind = gen.HostEventKind_HOST_EVENT_KIND_WARM_KEY_REVOKED
	}
	return r.appendLocked(&gen.HostEvent{
		Kind:    kind,
		WarmKey: p,
	})
}

// AppendBlock records BLOCK(H) and raises emitted_height. Wakes waiters.
func (r *hostEventRing) AppendBlock(p *gen.BlockPayload) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	seq := r.appendLocked(&gen.HostEvent{
		Kind:  gen.HostEventKind_HOST_EVENT_KIND_BLOCK,
		Block: p,
	})
	if p != nil && p.GetHeight() > 0 {
		h := p.GetHeight()
		if _, ok := r.blockSeq[h]; !ok {
			r.blockSeq[h] = seq
		}
		if r.firstHeight == 0 {
			r.firstHeight = h
		}
		if h > r.emitted {
			r.emitted = h
		}
	}
	return seq
}

// SetStalled makes GetHostEvents return Unavailable until cleared.
func (r *hostEventRing) SetStalled(stalled bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stalled = stalled
	if !stalled {
		r.wakeLocked()
	}
}

func (r *hostEventRing) Stalled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stalled
}

// BumpGeneration starts a new generation at the current tip with no rows below it.
func (r *hostEventRing) BumpGeneration() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.generation++
	if r.generation == 0 {
		r.generation = 1
	}
	r.events = nil
	r.blockSeq = make(map[int64]uint64)
	r.firstHeight = 0
	r.wakeLocked()
	return r.generation
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

type hostEventPage struct {
	Events     []*gen.HostEvent
	NextCursor uint64
	Generation uint64
	Reset      bool
	Resolved   uint64
}

func (r *hostEventRing) startAt(cursor, afterHeight uint64) (start uint64, reset bool, err error) {
	head := r.headLocked()
	if cursor > 0 {
		return cursor, false, nil
	}
	if afterHeight == 0 {
		return 0, false, nil
	}
	if r.emitted == 0 {
		return 0, false, errHostEventsNoHeight
	}
	if afterHeight > uint64(math.MaxInt64) || int64(afterHeight) > r.emitted {
		return head, false, nil
	}
	if seq, ok := r.blockSeq[int64(afterHeight)]; ok {
		return seq, false, nil
	}
	if r.firstHeight > 0 && int64(afterHeight)+1 == r.firstHeight {
		return 0, false, nil
	}
	if int64(afterHeight) == r.emitted {
		return head, false, nil
	}
	return 0, true, nil
}

func (r *hostEventRing) collect(cursor, clientGen, afterHeight uint64, want map[gen.HostEventKind]struct{}) (hostEventPage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	head := r.headLocked()
	out := hostEventPage{
		NextCursor: head,
		Generation: r.generation,
		Resolved:   cursor,
	}
	if clientGen != 0 && clientGen != r.generation {
		out.Reset = true
		return out, nil
	}
	start, reset, err := r.startAt(cursor, afterHeight)
	out.Resolved = start
	if err != nil {
		return out, err
	}
	if reset {
		out.Reset = true
		out.NextCursor = head
		return out, nil
	}
	if start > head {
		out.Reset = true
		return out, nil
	}
	var evs []*gen.HostEvent
	for _, ev := range r.events {
		if ev.Seq <= start {
			continue
		}
		if _, ok := want[ev.Kind]; !ok {
			continue
		}
		evs = append(evs, ev)
		if len(evs) >= maxHostEventsPage {
			out.Events = evs
			out.NextCursor = ev.Seq
			return out, nil
		}
	}
	out.Events = evs
	out.NextCursor = head
	return out, nil
}

// nodeManagerServer augments the chainoracle params NodeManager (GetRuntimeConfig
// + AcquireMLNode + …) with a mock GetHostEvents producer for the escrow
// long-poll warm scenario. Other RPCs are inherited from the embedded params server.
//
// When a test-only ML pool is configured, AcquireMLNode still goes through
// params.Server so mlnode_acquire/release Stage lines and lock tracking stay
// truthful; this wrapper only tallies /testenv/ml-allocations.
type nodeManagerServer struct {
	*params.Server
	ring         *hostEventRing
	blocks       blocks.BlockOracle
	omitBlocks   bool
	mlNodes      []MLNode
	allocationMu sync.Mutex
	allocations  map[string]uint64
}

func newNodeManagerServer(paramsSrv *params.Server, ring *hostEventRing, oracle blocks.BlockOracle, omitBlocks bool, configured ...[]MLNode) *nodeManagerServer {
	var nodes []MLNode
	if len(configured) > 0 {
		nodes = append([]MLNode(nil), configured[0]...)
	}
	return &nodeManagerServer{
		Server: paramsSrv, ring: ring, blocks: oracle, omitBlocks: omitBlocks,
		mlNodes: nodes, allocations: make(map[string]uint64, len(nodes)),
	}
}

// AcquireMLNode delegates to params.Server (stage logs + lock table) and, when
// a test-only pool is configured, increments /testenv/ml-allocations.
func (s *nodeManagerServer) AcquireMLNode(ctx context.Context, req *gen.AcquireMLNodeRequest) (*gen.AcquireMLNodeResponse, error) {
	resp, err := s.Server.AcquireMLNode(ctx, req)
	if err != nil || len(s.mlNodes) == 0 {
		return resp, err
	}
	s.allocationMu.Lock()
	s.allocations[resp.NodeId]++
	s.allocationMu.Unlock()
	return resp, nil
}

func (s *nodeManagerServer) AllocationCounts() map[string]uint64 {
	s.allocationMu.Lock()
	defer s.allocationMu.Unlock()
	counts := make(map[string]uint64, len(s.allocations))
	for id, count := range s.allocations {
		counts[id] = count
	}
	return counts
}

func (s *nodeManagerServer) GetBlockHeader(ctx context.Context, req *gen.GetBlockHeaderRequest) (*gen.GetBlockHeaderResponse, error) {
	if s.omitBlocks {
		return nil, status.Error(codes.Unimplemented, "method GetBlockHeader not implemented")
	}
	return nmrpc.GetBlockHeader(ctx, s.blocks, req)
}

func (s *nodeManagerServer) ProveBlockPath(ctx context.Context, req *gen.ProveBlockPathRequest) (*gen.ProveBlockPathResponse, error) {
	if s.omitBlocks {
		return nil, status.Error(codes.Unimplemented, "method ProveBlockPath not implemented")
	}
	return nmrpc.ProveBlockPath(ctx, s.blocks, req)
}

func mockSupportedKinds() []gen.HostEventKind {
	return []gen.HostEventKind{
		gen.HostEventKind_HOST_EVENT_KIND_BLOCK,
		gen.HostEventKind_HOST_EVENT_KIND_WARM_KEY_GRANTED,
		gen.HostEventKind_HOST_EVENT_KIND_WARM_KEY_REVOKED,
		gen.HostEventKind_HOST_EVENT_KIND_ESCROW_CREATED,
		gen.HostEventKind_HOST_EVENT_KIND_ESCROW_SETTLED,
	}
}

func (s *nodeManagerServer) hostEventsResponse(page hostEventPage, unchanged, needsReset bool) *gen.GetHostEventsResponse {
	return &gen.GetHostEventsResponse{
		Unchanged:      unchanged,
		Events:         page.Events,
		NextCursor:     page.NextCursor,
		Generation:     page.Generation,
		NeedsReset:     needsReset,
		SupportedKinds: mockSupportedKinds(),
	}
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
	afterHeight := req.GetAfterHeight()
	maxWait := clampMockHostEventsMaxWait(req.GetMaxWaitSeconds())
	var start uint64
	haveStart := false

	for {
		if s.ring.Stalled() {
			return nil, status.Error(codes.Unavailable, "host events: stalled")
		}
		var wake <-chan struct{}
		var release func()
		if maxWait > 0 {
			wake, release = s.ring.subscribe()
		}
		cur, after := cursor, afterHeight
		if haveStart {
			cur = start
			after = 0
		}
		page, err := s.ring.collect(cur, clientGen, after, want)
		if err != nil {
			if release != nil {
				release()
			}
			if errors.Is(err, errHostEventsNoHeight) {
				return nil, status.Error(codes.Unavailable, "host events: no height committed")
			}
			return nil, status.Error(codes.Unavailable, err.Error())
		}
		if !haveStart {
			start = page.Resolved
			haveStart = true
			afterHeight = 0
		}
		if page.Reset || len(page.Events) > 0 || page.NextCursor > start {
			if release != nil {
				release()
			}
			return s.hostEventsResponse(page, len(page.Events) == 0 && !page.Reset, page.Reset), nil
		}
		if maxWait <= 0 {
			if release != nil {
				release()
			}
			return s.hostEventsResponse(page, true, false), nil
		}
		select {
		case <-ctx.Done():
			release()
			return nil, status.FromContextError(ctx.Err()).Err()
		case <-wake:
			release()
		case <-time.After(maxWait):
			release()
			if s.ring.Stalled() {
				return nil, status.Error(codes.Unavailable, "host events: stalled")
			}
			page2, err := s.ring.collect(start, clientGen, 0, want)
			if err != nil {
				if errors.Is(err, errHostEventsNoHeight) {
					return nil, status.Error(codes.Unavailable, "host events: no height committed")
				}
				return nil, status.Error(codes.Unavailable, err.Error())
			}
			return s.hostEventsResponse(page2, len(page2.Events) == 0 && !page2.Reset, page2.Reset), nil
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
