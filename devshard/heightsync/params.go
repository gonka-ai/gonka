package heightsync

import (
	"fmt"
	"time"

	commrc "common/runtimeconfig"
)

const (
	// K_hb=1: one heartbeat cycle per new mainnet block.
	DefaultHeartbeatIntervalBlocks uint64 = 1
	// D_ack=1: one block of stamp slack. An ack is on time while
	// observed_height ≤ h_req + D_ack, so a height change in flight does not
	// degrade the turn.
	DefaultHeartbeatAckDeadlineBlocks uint64 = 1
	DefaultHeartbeatIdleBlocks        uint64 = 3 // T_idle = 3 * K_hb
	DefaultSyncDeltaBlocks            uint64 = 2 // D; Strong escalation is Phase F
	// MinRoundsPerBlock=2: E2 scheduler target — request span, then ack-carrying
	// diffs — compiled only, not a snapshot overlay or verifier deadline.
	DefaultMinRoundsPerBlock uint32 = 2

	// DefaultAssumedBlockTime is the Validate default when chain block time is unknown.
	DefaultAssumedBlockTime = 6 * time.Second

	DefaultRepairStagger = time.Second
)

// HeartbeatConfig is the log-plane height cadence (spec §20).
type HeartbeatConfig struct {
	IntervalBlocks    uint64 // K_hb
	AckDeadlineBlocks uint64 // D_ack; stamp slack after h_req
	IdleBlocks        uint64 // T_idle
	DeltaBlocks       uint64 // D; reported as CATCHING_UP until Strong lands
	MinRoundsPerBlock uint32 // E2 only: ≥2 nonce rounds per cycle
}

// RepairConfig budgets host→host repair probes (spec §11.4). MaxProbesPerWindow
// 0 means "use slots_num at the call site".
type RepairConfig struct {
	Stagger            time.Duration
	MaxProbesPerWindow int
}

// DefaultHeartbeatConfig returns the shipped defaults (K_hb=1, D_ack=1,
// T_idle=3, MinRoundsPerBlock=2).
func DefaultHeartbeatConfig() HeartbeatConfig {
	return HeartbeatConfig{
		IntervalBlocks:    DefaultHeartbeatIntervalBlocks,
		AckDeadlineBlocks: DefaultHeartbeatAckDeadlineBlocks,
		IdleBlocks:        DefaultHeartbeatIdleBlocks,
		DeltaBlocks:       DefaultSyncDeltaBlocks,
		MinRoundsPerBlock: DefaultMinRoundsPerBlock,
	}
}

// DefaultRepairConfig returns δ_probe = 1s and R_max unset (slots_num at use).
func DefaultRepairConfig() RepairConfig {
	return RepairConfig{Stagger: DefaultRepairStagger}
}

func (c HeartbeatConfig) withDefaults() HeartbeatConfig {
	if c.IntervalBlocks == 0 {
		c.IntervalBlocks = DefaultHeartbeatIntervalBlocks
	}
	if c.AckDeadlineBlocks == 0 {
		c.AckDeadlineBlocks = DefaultHeartbeatAckDeadlineBlocks
	}
	if c.IdleBlocks == 0 {
		c.IdleBlocks = DefaultHeartbeatIdleBlocks
	}
	if c.DeltaBlocks == 0 {
		c.DeltaBlocks = DefaultSyncDeltaBlocks
	}
	if c.MinRoundsPerBlock == 0 {
		c.MinRoundsPerBlock = DefaultMinRoundsPerBlock
	}
	return c
}

func (c RepairConfig) withDefaults() RepairConfig {
	if c.Stagger <= 0 {
		c.Stagger = DefaultRepairStagger
	}
	return c
}

// Validate checks spec §20 constraints plus the E2 round floor. A bad override
// must fail fast (H25).
//
//	MinRoundsPerBlock ≥ 2
//	T_idle > K_hb + D_ack
//	K_hb * blockTime ≤ freshness / 2
func (c HeartbeatConfig) Validate(blockTime, freshness time.Duration) error {
	c = c.withDefaults()
	if c.MinRoundsPerBlock < DefaultMinRoundsPerBlock {
		return fmt.Errorf("heightsync: MinRoundsPerBlock=%d must be ≥ %d",
			c.MinRoundsPerBlock, DefaultMinRoundsPerBlock)
	}
	if c.IdleBlocks <= c.IntervalBlocks+c.AckDeadlineBlocks {
		return fmt.Errorf("heightsync: T_idle=%d must be > K_hb+D_ack=%d",
			c.IdleBlocks, c.IntervalBlocks+c.AckDeadlineBlocks)
	}
	if blockTime <= 0 {
		blockTime = DefaultAssumedBlockTime
	}
	if freshness <= 0 {
		freshness = DefaultOriginatorFreshness
	}
	window := time.Duration(c.IntervalBlocks) * blockTime
	if window > freshness/2 {
		return fmt.Errorf("heightsync: K_hb*block_time=%s must be ≤ F/2=%s", window, freshness/2)
	}
	return nil
}

// HeartbeatConfigFromSnapshot overlays non-zero snapshot fields on compiled defaults.
// MinRoundsPerBlock is compiled-only and never overlayed.
func HeartbeatConfigFromSnapshot(snap commrc.Snapshot) HeartbeatConfig {
	cfg := DefaultHeartbeatConfig()
	if snap.HeightSync.IntervalBlocks > 0 {
		cfg.IntervalBlocks = snap.HeightSync.IntervalBlocks
	}
	if snap.HeightSync.AckDeadlineBlocks > 0 {
		cfg.AckDeadlineBlocks = snap.HeightSync.AckDeadlineBlocks
	}
	if snap.HeightSync.IdleBlocks > 0 {
		cfg.IdleBlocks = snap.HeightSync.IdleBlocks
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
