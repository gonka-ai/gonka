package mlnode

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"devshard/nodemanager/gen"
	"devshard/observability"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	// DefaultCapacityPollInterval is how often ListNodeCapacity is polled while supported.
	DefaultCapacityPollInterval = 5 * time.Second
	// DefaultUnsupportedRetryInterval is the slow poll used after Unimplemented.
	DefaultUnsupportedRetryInterval = 5 * time.Minute
	// DefaultEscrowLoadStaleTTL is how long the last escrow_load map remains usable
	// for the divisor after delivery (tier 1). After this, Divisor falls back to 4.
	DefaultEscrowLoadStaleTTL = 10 * time.Minute
	// MinFallbackDivisor is the floor applied to numberOfEscrows.
	MinFallbackDivisor = 4
	// DefaultUnknownMaxConcurrent is the synthetic max_concurrent assumed for a
	// fallback node that PickNode returned but ListNodeCapacity has never
	// reported. Its effective bound is DefaultUnknownMaxConcurrent / Divisor()
	// (floored at 1), so an unknown node stays usable but bounded instead of
	// bypassing the semaphore entirely.
	DefaultUnknownMaxConcurrent = 50

	SourceDAPI  = "dapi"
	SourceStale = "stale"

	divisorSourceLoadMap = "load_map"
	divisorSourceFloor4  = "floor4"
)

// NodeCapacity is one node's observed concurrency limits.
type NodeCapacity struct {
	NodeID        string
	Model         string
	MaxConcurrent int
	LockCount     int
	UpdatedAt     time.Time
	Source        string // "dapi" | "stale"
}

// CapacityClient is the subset of NodeManager used by Cache.
type CapacityClient interface {
	ListNodeCapacity(ctx context.Context, in *gen.ListNodeCapacityRequest, opts ...grpc.CallOption) (*gen.ListNodeCapacityResponse, error)
}

// ActiveLoadFunc returns the last active-escrow load map and when it was delivered.
// Idle escrows are already omitted. deliveredAt is zero when never delivered.
type ActiveLoadFunc func() (byEscrow map[uint64]float64, deliveredAt time.Time)

// CacheOptions configures NewCache.
type CacheOptions struct {
	ActiveLoad               ActiveLoadFunc
	PollInterval             time.Duration
	UnsupportedRetryInterval time.Duration
	EscrowLoadStaleTTL       time.Duration
	// UnknownMaxConcurrent is the synthetic max_concurrent for capacity-unknown
	// fallback nodes (DefaultUnknownMaxConcurrent when non-positive).
	UnknownMaxConcurrent int
	Now                  func() time.Time
	Log                  *slog.Logger
}

type nodeCap struct {
	nodeID        string
	models        map[string]struct{}
	maxConcurrent int
	lockCount     int
	updatedAt     time.Time
	source        string
}

// Cache observes ListNodeCapacity and the host-events escrow_load map, and
// exposes EffectiveMax / AvailableSlots / Divisor for the fallback bound
// (wired in a later step). It does not import decentralized-api packages.
type Cache struct {
	client CapacityClient

	mu    sync.Mutex
	nodes map[string]*nodeCap // key: nodeID

	// inFlight is keyed by (nodeID, model) — physical vLLM occupancy.
	inFlight map[nodeModelKey]int
	// unknownInFlight tracks in-flight slots for capacity-unknown fallback nodes
	// (not in c.nodes). Kept separate so applyPoll's prune never wipes live
	// counts for a node dapi has not (yet) reported.
	unknownInFlight map[nodeModelKey]int

	activeLoad ActiveLoadFunc
	now        func() time.Time
	log        *slog.Logger

	pollInterval             time.Duration
	unsupportedRetryInterval time.Duration
	escrowLoadStaleTTL       time.Duration
	unknownMaxConcurrent     int

	observed    bool // at least one successful ListNodeCapacity
	unsupported bool // Unimplemented seen; rare retry only
}

// NewCache returns a capacity cache. Call Start to begin polling.
func NewCache(client CapacityClient, opts CacheOptions) *Cache {
	if opts.PollInterval <= 0 {
		opts.PollInterval = DefaultCapacityPollInterval
	}
	if opts.UnsupportedRetryInterval <= 0 {
		opts.UnsupportedRetryInterval = DefaultUnsupportedRetryInterval
	}
	if opts.EscrowLoadStaleTTL <= 0 {
		opts.EscrowLoadStaleTTL = DefaultEscrowLoadStaleTTL
	}
	if opts.UnknownMaxConcurrent <= 0 {
		opts.UnknownMaxConcurrent = DefaultUnknownMaxConcurrent
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	active := opts.ActiveLoad
	if active == nil {
		active = func() (map[uint64]float64, time.Time) { return nil, time.Time{} }
	}
	return &Cache{
		client:                   client,
		nodes:                    make(map[string]*nodeCap),
		inFlight:                 make(map[nodeModelKey]int),
		unknownInFlight:          make(map[nodeModelKey]int),
		activeLoad:               active,
		now:                      opts.Now,
		log:                      opts.Log,
		pollInterval:             opts.PollInterval,
		unsupportedRetryInterval: opts.UnsupportedRetryInterval,
		escrowLoadStaleTTL:       opts.EscrowLoadStaleTTL,
		unknownMaxConcurrent:     opts.UnknownMaxConcurrent,
	}
}

// Start polls ListNodeCapacity until ctx is cancelled.
func (c *Cache) Start(ctx context.Context) {
	if c == nil || c.client == nil {
		return
	}
	go c.pollLoop(ctx)
}

func (c *Cache) pollLoop(ctx context.Context) {
	interval := c.pollInterval
	for {
		c.pollOnce(ctx)
		c.mu.Lock()
		if c.unsupported {
			interval = c.unsupportedRetryInterval
		} else {
			interval = c.pollInterval
		}
		c.mu.Unlock()

		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

func (c *Cache) pollOnce(ctx context.Context) {
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	resp, err := c.client.ListNodeCapacity(callCtx, &gen.ListNodeCapacityRequest{})
	cancel()
	if err != nil {
		if status.Code(err) == codes.Unimplemented {
			c.mu.Lock()
			first := !c.unsupported
			c.unsupported = true
			c.mu.Unlock()
			if first {
				c.log.Info("mlnode capacity: ListNodeCapacity Unimplemented; disabling local capacity bound")
			}
			return
		}
		if ctx.Err() != nil {
			return
		}
		c.markStale()
		c.log.Warn("mlnode capacity: poll failed; retaining last-seen", "err", err)
		return
	}

	c.applyPoll(resp.GetNodes())
}

func (c *Cache) markStale() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, n := range c.nodes {
		n.source = SourceStale
	}
}

func (c *Cache) applyPoll(entries []*gen.NodeCapacityEntry) {
	now := c.now()
	next := make(map[string]*nodeCap, len(entries))
	for _, e := range entries {
		if e == nil || e.GetNodeId() == "" {
			continue
		}
		id := e.GetNodeId()
		nc, ok := next[id]
		if !ok {
			nc = &nodeCap{
				nodeID: id,
				models: make(map[string]struct{}),
			}
			next[id] = nc
		}
		if m := e.GetModel(); m != "" {
			nc.models[m] = struct{}{}
		}
		nc.maxConcurrent = int(e.GetMaxConcurrent())
		nc.lockCount = int(e.GetLockCount())
		nc.updatedAt = now
		nc.source = SourceDAPI
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.nodes = next
	c.observed = true
	c.unsupported = false
	c.pruneInFlightLocked()
}

// HasObservedCapacity reports whether ListNodeCapacity has succeeded at least once.
// False for old DAPI (Unimplemented) → callers must leave fallback unbounded.
func (c *Cache) HasObservedCapacity() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.observed && !c.unsupported
}

// Get returns a copy of the capacity row for nodeID, if present.
func (c *Cache) Get(nodeID string) (NodeCapacity, bool) {
	if c == nil || nodeID == "" {
		return NodeCapacity{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	n, ok := c.nodes[nodeID]
	if !ok {
		return NodeCapacity{}, false
	}
	return toNodeCapacity(n), true
}

func toNodeCapacity(n *nodeCap) NodeCapacity {
	model := ""
	for m := range n.models {
		model = m
		break
	}
	return NodeCapacity{
		NodeID:        n.nodeID,
		Model:         model,
		MaxConcurrent: n.maxConcurrent,
		LockCount:     n.lockCount,
		UpdatedAt:     n.updatedAt,
		Source:        n.source,
	}
}

// Divisor returns max(numberOfEscrows, 4) when the load map is ≤ escrowLoadStaleTTL
// old; otherwise 4. Emits devshardd_fallback_divisor.
func (c *Cache) Divisor() int {
	if c == nil {
		return MinFallbackDivisor
	}
	now := c.now()
	byEscrow, deliveredAt := c.activeLoad()
	divisor := MinFallbackDivisor
	source := divisorSourceFloor4
	if !deliveredAt.IsZero() && now.Sub(deliveredAt) <= c.escrowLoadStaleTTL {
		n := len(byEscrow)
		if n > MinFallbackDivisor {
			divisor = n
		}
		source = divisorSourceLoadMap
	}
	observability.SetFallbackDivisor(source, divisor)
	return divisor
}

// EffectiveMax returns max(1, registeredMax / Divisor()) for nodeID, or 0 if unknown.
func (c *Cache) EffectiveMax(nodeID string) int {
	if c == nil || nodeID == "" {
		return 0
	}
	c.mu.Lock()
	n, ok := c.nodes[nodeID]
	c.mu.Unlock()
	if !ok {
		return 0
	}
	div := c.Divisor()
	if div < 1 {
		div = 1
	}
	eff := n.maxConcurrent / div
	if eff < 1 {
		return 1
	}
	return eff
}

// AvailableSlots returns max(0, effectiveMax - chargedLockCount - localInFlight)
// for nodeID. localInFlight is the sum across models for that node. See
// chargedLockCountLocked for why lockCount is dropped during an outage.
func (c *Cache) AvailableSlots(nodeID string) int {
	if c == nil || nodeID == "" {
		return 0
	}
	div := c.Divisor()
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.availableSlotsLocked(nodeID, div)
}

// AvailableSlotsForModel returns the maximum AvailableSlots among nodes that
// serve model (for target selection). Returns 0 when none.
func (c *Cache) AvailableSlotsForModel(model string) int {
	if c == nil || model == "" {
		return 0
	}
	div := c.Divisor()
	c.mu.Lock()
	defer c.mu.Unlock()
	best := 0
	for id, n := range c.nodes {
		if _, ok := n.models[model]; !ok {
			continue
		}
		if s := c.availableSlotsLocked(id, div); s > best {
			best = s
		}
	}
	return best
}

// TryAcquire reserves one local in-flight slot for (nodeID, model) if
// AvailableSlots(nodeID) > 0. Returns false when no slot remains.
func (c *Cache) TryAcquire(nodeID, model string) bool {
	if c == nil || nodeID == "" {
		return false
	}
	div := c.Divisor()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.availableSlotsLocked(nodeID, div) <= 0 {
		return false
	}
	c.inFlight[nodeModelKey{nodeID: nodeID, model: model}]++
	return true
}

// availableSlotsLocked computes free fallback slots for nodeID under the given
// divisor. Caller holds c.mu. div should be Cache.Divisor() computed before
// locking (Divisor reads the injected load map and emits a metric).
func (c *Cache) availableSlotsLocked(nodeID string, div int) int {
	n, ok := c.nodes[nodeID]
	if !ok {
		return 0
	}
	if div < 1 {
		div = 1
	}
	eff := n.maxConcurrent / div
	if eff < 1 {
		eff = 1
	}
	slots := eff - chargedLockCountLocked(n) - c.inFlightSumLocked(nodeID)
	if slots < 0 {
		return 0
	}
	return slots
}

// chargedLockCountLocked is the broker lockCount charged against the fallback
// budget. While polls succeed (SourceDAPI) the last-seen locks approximate real
// vLLM occupancy, so fallback subtracts them to avoid piling onto a busy node.
// Once a poll has failed (SourceStale) DAPI is unreachable — no new brokered
// acquires happen and existing ones drain within the HTTP timeout — while
// lockCount stays frozen at the last (possibly busy) poll. Continuing to
// subtract that phantom load would starve fallback for the whole outage
// (e.g. max=40, lockCount=30, divisor=4 → effectiveMax=10 → 0 slots), so stale
// rows charge 0 and bound only by effectiveMax - localInFlight.
func chargedLockCountLocked(n *nodeCap) int {
	if n.source == SourceStale {
		return 0
	}
	return n.lockCount
}

// Release frees one local in-flight slot for (nodeID, model).
func (c *Cache) Release(nodeID, model string) {
	if c == nil || nodeID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key := nodeModelKey{nodeID: nodeID, model: model}
	if c.inFlight[key] <= 1 {
		delete(c.inFlight, key)
		return
	}
	c.inFlight[key]--
}

// unknownEffectiveMax is the synthetic per-node bound for a capacity-unknown
// fallback node: unknownMaxConcurrent / Divisor(), floored at 1. Divisor floors
// at MinFallbackDivisor (4), so with an idle/fresh load map this is
// unknownMaxConcurrent / 4, shrinking further as active escrows grow.
func (c *Cache) unknownEffectiveMax() int {
	div := c.Divisor()
	if div < 1 {
		div = 1
	}
	eff := c.unknownMaxConcurrent / div
	if eff < 1 {
		eff = 1
	}
	return eff
}

// TryAcquireUnknown reserves one in-flight slot for a capacity-unknown fallback
// node — one PickNode returned but ListNodeCapacity has never reported, so it
// has no observed max_concurrent. Instead of bypassing the semaphore, it is
// bounded by unknownEffectiveMax(). Returns false when that budget is exhausted.
// Pair every true return with exactly one ReleaseUnknown.
func (c *Cache) TryAcquireUnknown(nodeID, model string) bool {
	if c == nil || nodeID == "" {
		return false
	}
	eff := c.unknownEffectiveMax() // reads load map; must not hold c.mu
	c.mu.Lock()
	defer c.mu.Unlock()
	if inFlightSum(c.unknownInFlight, nodeID) >= eff {
		return false
	}
	c.unknownInFlight[nodeModelKey{nodeID: nodeID, model: model}]++
	return true
}

// ReleaseUnknown frees one in-flight slot taken by TryAcquireUnknown.
func (c *Cache) ReleaseUnknown(nodeID, model string) {
	if c == nil || nodeID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key := nodeModelKey{nodeID: nodeID, model: model}
	if c.unknownInFlight[key] <= 1 {
		delete(c.unknownInFlight, key)
		return
	}
	c.unknownInFlight[key]--
}

// nodeModelKey identifies one (node, model) in-flight counter.
type nodeModelKey struct {
	nodeID string
	model  string
}

func inFlightSum(m map[nodeModelKey]int, nodeID string) int {
	sum := 0
	for k, v := range m {
		if k.nodeID == nodeID {
			sum += v
		}
	}
	return sum
}

func (c *Cache) inFlightSumLocked(nodeID string) int {
	return inFlightSum(c.inFlight, nodeID)
}

func (c *Cache) pruneInFlightLocked() {
	for k := range c.inFlight {
		if _, exists := c.nodes[k.nodeID]; !exists {
			delete(c.inFlight, k)
		}
	}
}

// ApplyPollForTest applies a successful poll snapshot (tests only).
func (c *Cache) ApplyPollForTest(entries []*gen.NodeCapacityEntry) {
	c.applyPoll(entries)
}

// MarkStaleForTest marks all rows stale without clearing (tests only).
func (c *Cache) MarkStaleForTest() {
	c.markStale()
}

// SetUnsupportedForTest marks capacity as unsupported (tests only).
func (c *Cache) SetUnsupportedForTest() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.unsupported = true
	c.observed = false
	c.nodes = make(map[string]*nodeCap)
}
