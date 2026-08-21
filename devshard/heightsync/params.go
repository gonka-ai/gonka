package heightsync

import (
	"fmt"
	"sync/atomic"
	"time"

	commrc "common/runtimeconfig"
)

const (
	// DefaultHeartbeatInterval is the round-trip budget: a full height-sync
	// turnover must land at least this often. Wall clock, not blocks — mainnet
	// height is the *result* of a turnover, so no party can schedule the next
	// one from a height it has not learned yet.
	DefaultHeartbeatInterval = 3 * time.Second
	// DefaultTurnTimeoutMultiple sets TurnTimeout = 2 * Interval.
	//
	// Patience equal to the interval leaves a turn none: the span is dispatched
	// slot by slot and only then waits for acks, so a turn is abandoned at the
	// exact moment the next becomes due. A session whose round trips run past one
	// interval then reopens forever and records no turnover at all — the failure
	// is total rather than degraded, which is why this needs real headroom.
	DefaultTurnTimeoutMultiple = 2
	// DefaultIdleMultiple sets T_idle = 4 * Interval: how long a host tolerates
	// user silence before it arms close-ready. It must exceed
	// (1 + DefaultTurnTimeoutMultiple) * Interval, the worst-case cost of one
	// lost turnover, or a host arms while the producer is still mid-retry.
	DefaultIdleMultiple = 4

	// MinAckDeadlineBlocks is the floor on D_ack. Zero would make an ack late
	// the instant a block ticks, which no round trip can beat.
	MinAckDeadlineBlocks uint64 = 1

	DefaultSyncDeltaBlocks uint64 = 2 // D; Strong escalation is spec §8

	// DefaultConfirmWindowBlocks is W_conf: the span of heights the protocol
	// treats as contemporaneous. It bounds which attestations may enter the
	// confirmation index, how far one signer may raise the log's floor
	// unaided, and how far above its own tip a producer will carry a floor.
	// All three ask the same question — "is this height a plausible neighbour
	// of the one I hold?" — so they share one constant.
	DefaultConfirmWindowBlocks uint64 = 256

	// DefaultBlockTime is the assumed mainnet block interval, and the only
	// deployment fact in this file.
	//
	// It exists because the schedule is wall clock while the log can only count
	// blocks, so converting one to the other needs a rate. The default is the
	// fastest chain we ship against (mock-dapi ticks every second), which is the
	// safe direction: assuming fast blocks yields a wider ack window, and a
	// window that is too wide only makes the log notice a stalled turn later,
	// while one that is too narrow calls honest acks late. Deployments with
	// slower blocks should say so — height_sync_block_time_ms — and get earlier
	// degradation and earlier repair probes for it.
	DefaultBlockTime = time.Second

	DefaultRepairStagger = time.Second
)

// DefaultHeartbeatIdleTimeout is T_idle over the shipped interval.
const DefaultHeartbeatIdleTimeout = DefaultIdleMultiple * DefaultHeartbeatInterval

// HeartbeatConfig is the log-plane height cadence (spec §20).
//
// The knobs split by who needs a "now" to use them:
//
//   - Scheduling (Interval, TurnTimeout, IdleTimeout) is wall clock. A producer
//     with no fresh height cannot tell that a block has passed, and a host that
//     has heard nothing cannot either, so a block-denominated schedule would be
//     circular: it needs the very sync it is supposed to trigger.
//   - Evaluation (AckDeadlineBlocks, DeltaBlocks) stays in blocks. Those
//     compare two claims that are already in Diff, so every replaying verifier
//     recomputes the same verdict without consulting any clock.
//
// Nothing on the scheduling side is ever folded into Diff or into a
// SyncTurnRecord: turn state must stay a pure function of the log.
//
// One knob straddles the split, and BlockTime is why it can. D_ack answers a
// scheduling question — "did this answer arrive while the request still stood?" —
// with the log's only clock, which is height. The two are the same budget in
// different units, so D_ack is derived from the schedule rather than shipped as
// a constant, and Validate holds the conversion to account.
type HeartbeatConfig struct {
	Interval    time.Duration // max gap between full height-sync turnovers
	TurnTimeout time.Duration // producer patience on one open turn before it reopens
	IdleTimeout time.Duration // T_idle: user silence a host tolerates before arming
	BlockTime   time.Duration // assumed chain block interval; converts the two units

	AckDeadlineBlocks uint64 // D_ack; ack window after h_req, derived from the schedule
	DeltaBlocks       uint64 // D; reported as CATCHING_UP until Strong lands
	WindowBlocks      uint64 // W_conf; plausible neighbourhood of a height
}

// TurnoverBudget is the producer's own worst case for one turnover: Interval to
// notice that a turnover is due, plus TurnTimeout of patience on the turn it
// then opens. It is the deadline Heartbeat.Deadline reports and the span T_idle
// must exceed, so every party's patience is stated against this one quantity.
func (c HeartbeatConfig) TurnoverBudget() time.Duration {
	c = c.withDefaults()
	return c.Interval + c.TurnTimeout
}

// AckWindow is D_ack back in wall clock: how long the log will wait for an ack
// before it treats the turn as overdue.
//
// The invariant Validate enforces is AckWindow >= TurnoverBudget. Below it the
// log declares a turn degraded while the producer is still legitimately waiting
// for the acks it asked for, so honest acks land marked `late`, turns degrade in
// steady state, repair probes fire against nobody's fault, and h_last stops
// advancing. That mismatch — a block-denominated window against a millisecond
// schedule — is the whole of this knob's history.
func (c HeartbeatConfig) AckWindow() time.Duration {
	c = c.withDefaults()
	return time.Duration(c.AckDeadlineBlocks) * c.BlockTime
}

// AckDeadlineBlocksFor is the block window that covers budget at blockTime.
//
// The extra block is the boundary: the request height is read at some arbitrary
// point inside a block, so an elapsed span of n block times can cross n + 1
// boundaries. Rounding up without it would put honest acks one block outside
// their own window whenever a turn opens late in a block.
func AckDeadlineBlocksFor(budget, blockTime time.Duration) uint64 {
	if blockTime <= 0 {
		blockTime = DefaultBlockTime
	}
	if budget <= 0 {
		return MinAckDeadlineBlocks
	}
	blocks := uint64((budget+blockTime-1)/blockTime) + 1
	if blocks < MinAckDeadlineBlocks {
		return MinAckDeadlineBlocks
	}
	return blocks
}

// FloorOutOfReach reports whether a floor sits too far above a producer's own
// tip to carry honestly.
//
// The producer rule offers two branches, max(own_tip, F(m)) or omit, and this
// picks between them: within W_conf the gap is ordinary lag and carrying the
// floor is a truthful statement about shared logical time, but beyond it no
// plausible chain advance explains the distance, so the floor is either poisoned
// or on a branch this producer will never see. Omitting says so without
// propagating the height, which is the difference between one bad claim and a
// roster of honest parties repeating it.
//
// A producer with no reading of its own (ownTip 0, ORACLE_UNAVAILABLE) has
// nothing to judge plausibility against, so the escape does not apply: it
// carries, which is what keeps a blind host inside the cadence.
func (c HeartbeatConfig) FloorOutOfReach(floor, ownTip uint64) bool {
	if ownTip == 0 || floor <= ownTip {
		return false
	}
	return floor-ownTip > c.withDefaults().WindowBlocks
}

// RepairConfig budgets host→host repair probes (spec §11.4) on both the
// prober and the responder. MaxProbesPerWindow 0 means "use slots_num at
// the call site" (`R_max`).
type RepairConfig struct {
	Stagger            time.Duration
	MaxProbesPerWindow int
}

// DefaultHeartbeatConfig returns the shipped defaults: 3s interval, 6s turn
// timeout, 12s idle, 1s blocks, and the D_ack those imply.
func DefaultHeartbeatConfig() HeartbeatConfig {
	return HeartbeatConfig{}.withDefaults()
}

// DefaultRepairConfig returns δ_probe = 1s and R_max unset (slots_num at use).
func DefaultRepairConfig() RepairConfig {
	return RepairConfig{Stagger: DefaultRepairStagger}
}

// withDefaults fills zero fields. TurnTimeout, IdleTimeout and AckDeadlineBlocks
// are all derived from Interval, so overriding the interval alone cannot produce
// a config that Validate rejects — including on the block-denominated side,
// which is what a bare constant could not manage.
func (c HeartbeatConfig) withDefaults() HeartbeatConfig {
	if c.Interval <= 0 {
		c.Interval = DefaultHeartbeatInterval
	}
	if c.TurnTimeout <= 0 {
		c.TurnTimeout = DefaultTurnTimeoutMultiple * c.Interval
	}
	if c.IdleTimeout <= 0 {
		c.IdleTimeout = DefaultIdleMultiple * c.Interval
	}
	if c.BlockTime <= 0 {
		c.BlockTime = DefaultBlockTime
	}
	if c.AckDeadlineBlocks == 0 {
		c.AckDeadlineBlocks = AckDeadlineBlocksFor(c.Interval+c.TurnTimeout, c.BlockTime)
	}
	if c.DeltaBlocks == 0 {
		c.DeltaBlocks = DefaultSyncDeltaBlocks
	}
	if c.WindowBlocks == 0 {
		c.WindowBlocks = DefaultConfirmWindowBlocks
	}
	return c
}

func (c RepairConfig) withDefaults() RepairConfig {
	if c.Stagger <= 0 {
		c.Stagger = DefaultRepairStagger
	}
	return c
}

// Validate checks spec §20 constraints. A bad override must fail fast.
//
//	D_ack * BlockTime >= Interval + TurnTimeout
//	T_idle            >  Interval + TurnTimeout
//	2 * Interval      <= freshness
//
// The three read as one chain: the log waits at least as long as the producer
// does, and the host waits longer than either. Breaking the first makes the log
// disown a turn its own producer is still working on; breaking the second arms a
// host over a single lost turnover.
//
// The last rule is the old K_hb·block_time ≤ F/2 with the block-time conversion
// removed: two turnovers must fit inside the originator freshness budget so a
// height claim cannot go stale between them.
func (c HeartbeatConfig) Validate(freshness time.Duration) error {
	c = c.withDefaults()
	budget := c.TurnoverBudget()
	if window := c.AckWindow(); window < budget {
		return fmt.Errorf("heightsync: ack window D_ack*BlockTime=%d*%s=%s must be ≥ Interval+TurnTimeout=%s",
			c.AckDeadlineBlocks, c.BlockTime, window, budget)
	}
	if c.IdleTimeout <= budget {
		return fmt.Errorf("heightsync: T_idle=%s must be > Interval+TurnTimeout=%s",
			c.IdleTimeout, budget)
	}
	if freshness <= 0 {
		freshness = DefaultOriginatorFreshness
	}
	if 2*c.Interval > freshness {
		return fmt.Errorf("heightsync: 2*Interval=%s must be ≤ F=%s", 2*c.Interval, freshness)
	}
	return nil
}

// OverlayResult is a snapshot overlay plus whether it had to be clamped.
type OverlayResult struct {
	Config  HeartbeatConfig
	Clamped bool
	Reason  string
}

var (
	overlayClampCount  atomic.Uint64
	overlayClampReason atomic.Value // string
)

// OverlayClampCount is how many times a runtime overlay was rejected and
// replaced with compiled defaults. Tests assert it; the prometheus counter
// in RegisterAnchorMetrics is the operator-facing copy.
func OverlayClampCount() uint64 { return overlayClampCount.Load() }

// LastOverlayClampReason is the Validate error that caused the most recent clamp.
func LastOverlayClampReason() string {
	v, _ := overlayClampReason.Load().(string)
	return v
}

// HeartbeatConfigFromSnapshot overlays scheduling knobs from the runtime
// snapshot onto compiled defaults. Zero on the wire always means "keep the
// default", never "disable".
//
// Evaluation knobs (AckDeadlineBlocks, DeltaBlocks, WindowBlocks, BlockTime)
// stay compiled. They feed SyncTurnRecord and L0, so two hosts on different
// long-poll snapshots must not compute different Late flags or floors for the
// same log. Scheduling knobs (Interval, TurnTimeout, IdleTimeout) are
// producer-local and overlayable.
//
// An overlay that would fail Validate against those compiled evaluation knobs
// is clamped back to compiled defaults rather than shipped. The clamp is
// observable: OverlayClampCount and LastOverlayClampReason.
func HeartbeatConfigFromSnapshot(snap commrc.Snapshot) HeartbeatConfig {
	return OverlayHeartbeatConfig(snap).Config
}

// OverlayHeartbeatConfig is HeartbeatConfigFromSnapshot with the clamp result.
func OverlayHeartbeatConfig(snap commrc.Snapshot) OverlayResult {
	compiled := DefaultHeartbeatConfig()
	cfg := HeartbeatConfig{
		AckDeadlineBlocks: compiled.AckDeadlineBlocks,
		DeltaBlocks:       compiled.DeltaBlocks,
		WindowBlocks:      compiled.WindowBlocks,
		BlockTime:         compiled.BlockTime,
	}
	if ms := snap.HeightSync.IntervalMs; ms > 0 {
		cfg.Interval = time.Duration(ms) * time.Millisecond
	}
	if ms := snap.HeightSync.TurnTimeoutMs; ms > 0 {
		cfg.TurnTimeout = time.Duration(ms) * time.Millisecond
	}
	if ms := snap.HeightSync.IdleTimeoutMs; ms > 0 {
		cfg.IdleTimeout = time.Duration(ms) * time.Millisecond
	}
	cfg = cfg.withDefaults()
	if err := cfg.Validate(DefaultOriginatorFreshness); err != nil {
		overlayClampCount.Add(1)
		overlayClampReason.Store(err.Error())
		noteOverlayClamped()
		return OverlayResult{Config: compiled, Clamped: true, Reason: err.Error()}
	}
	return OverlayResult{Config: cfg}
}

// RepairConfigFromSnapshot overlays non-zero snapshot fields on compiled defaults.
func RepairConfigFromSnapshot(snap commrc.Snapshot) RepairConfig {
	cfg := DefaultRepairConfig()
	if snap.HeightSync.ProbeStaggerMs > 0 {
		cfg.Stagger = time.Duration(snap.HeightSync.ProbeStaggerMs) * time.Millisecond
	}
	if snap.HeightSync.MaxProbesPerWindow > 0 {
		cfg.MaxProbesPerWindow = int(snap.HeightSync.MaxProbesPerWindow)
	}
	return cfg
}
