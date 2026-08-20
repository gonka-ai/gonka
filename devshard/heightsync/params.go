package heightsync

import (
	"fmt"
	"time"

	commrc "common/runtimeconfig"
)

const (
	// DefaultHeartbeatInterval is the round-trip budget: a full height-sync
	// turnover must land at least this often. Wall clock, not blocks — mainnet
	// height is the *result* of a turnover, so no party can schedule the next
	// one from a height it has not learned yet.
	DefaultHeartbeatInterval = 3 * time.Second
	// DefaultIdleMultiple sets T_idle = 4 * Interval: how long a host tolerates
	// user silence before it arms close-ready.
	DefaultIdleMultiple = 4

	// D_ack=1: one block of stamp slack. An ack is on time while
	// observed_height <= h_req + D_ack, so a height change in flight does not
	// degrade the turn. Blocks, not time: both sides are claims already in Diff.
	DefaultHeartbeatAckDeadlineBlocks uint64 = 1
	DefaultSyncDeltaBlocks            uint64 = 2 // D; Strong escalation is Phase F

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
type HeartbeatConfig struct {
	Interval    time.Duration // max gap between full height-sync turnovers
	TurnTimeout time.Duration // producer patience on one open turn before it reopens
	IdleTimeout time.Duration // T_idle: user silence a host tolerates before arming

	AckDeadlineBlocks uint64 // D_ack; stamp slack after h_req
	DeltaBlocks       uint64 // D; reported as CATCHING_UP until Strong lands
}

// RepairConfig budgets host→host repair probes (spec §11.4). MaxProbesPerWindow
// 0 means "use slots_num at the call site".
type RepairConfig struct {
	Stagger            time.Duration
	MaxProbesPerWindow int
}

// DefaultHeartbeatConfig returns the shipped defaults (3s interval, 3s turn
// timeout, 12s idle, D_ack=1).
func DefaultHeartbeatConfig() HeartbeatConfig {
	return HeartbeatConfig{
		Interval:          DefaultHeartbeatInterval,
		TurnTimeout:       DefaultHeartbeatInterval,
		IdleTimeout:       DefaultHeartbeatIdleTimeout,
		AckDeadlineBlocks: DefaultHeartbeatAckDeadlineBlocks,
		DeltaBlocks:       DefaultSyncDeltaBlocks,
	}
}

// DefaultRepairConfig returns δ_probe = 1s and R_max unset (slots_num at use).
func DefaultRepairConfig() RepairConfig {
	return RepairConfig{Stagger: DefaultRepairStagger}
}

// withDefaults fills zero fields. TurnTimeout and IdleTimeout are derived from
// Interval so overriding the interval alone cannot produce a config that
// Validate rejects.
func (c HeartbeatConfig) withDefaults() HeartbeatConfig {
	if c.Interval <= 0 {
		c.Interval = DefaultHeartbeatInterval
	}
	if c.TurnTimeout <= 0 {
		c.TurnTimeout = c.Interval
	}
	if c.IdleTimeout <= 0 {
		c.IdleTimeout = DefaultIdleMultiple * c.Interval
	}
	if c.AckDeadlineBlocks == 0 {
		c.AckDeadlineBlocks = DefaultHeartbeatAckDeadlineBlocks
	}
	if c.DeltaBlocks == 0 {
		c.DeltaBlocks = DefaultSyncDeltaBlocks
	}
	return c
}

func (c RepairConfig) withDefaults() RepairConfig {
	if c.Stagger <= 0 {
		c.Stagger = DefaultRepairStagger
	}
	return c
}

// Validate checks spec §20 constraints. A bad override must fail fast (H25).
//
//	T_idle > Interval + TurnTimeout
//	2 * Interval <= freshness
//
// The second rule is the old K_hb·block_time ≤ F/2 with the block-time
// conversion removed: two turnovers must fit inside the originator freshness
// budget so a height claim cannot go stale between them.
func (c HeartbeatConfig) Validate(freshness time.Duration) error {
	c = c.withDefaults()
	if c.IdleTimeout <= c.Interval+c.TurnTimeout {
		return fmt.Errorf("heightsync: T_idle=%s must be > Interval+TurnTimeout=%s",
			c.IdleTimeout, c.Interval+c.TurnTimeout)
	}
	if freshness <= 0 {
		freshness = DefaultOriginatorFreshness
	}
	if 2*c.Interval > freshness {
		return fmt.Errorf("heightsync: 2*Interval=%s must be ≤ F=%s", 2*c.Interval, freshness)
	}
	return nil
}

// HeartbeatConfigFromSnapshot overlays non-zero snapshot fields on compiled
// defaults. Zero on the wire always means "keep the default", never "disable".
func HeartbeatConfigFromSnapshot(snap commrc.Snapshot) HeartbeatConfig {
	cfg := DefaultHeartbeatConfig()
	if ms := snap.HeightSync.IntervalMs; ms > 0 {
		cfg.Interval = time.Duration(ms) * time.Millisecond
		// Derived knobs follow the overridden interval unless also set below.
		cfg.TurnTimeout = cfg.Interval
		cfg.IdleTimeout = DefaultIdleMultiple * cfg.Interval
	}
	if ms := snap.HeightSync.TurnTimeoutMs; ms > 0 {
		cfg.TurnTimeout = time.Duration(ms) * time.Millisecond
	}
	if ms := snap.HeightSync.IdleTimeoutMs; ms > 0 {
		cfg.IdleTimeout = time.Duration(ms) * time.Millisecond
	}
	if snap.HeightSync.AckDeadlineBlocks > 0 {
		cfg.AckDeadlineBlocks = snap.HeightSync.AckDeadlineBlocks
	}
	return cfg
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
