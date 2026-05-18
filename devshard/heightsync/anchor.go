package heightsync

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"devshard/blockoracle"
)

const (
	// AnchorProofType marks light (no-proof) anchor attestations.
	AnchorProofType = "height-anchor-v1"
	defaultAnchorK  = uint64(10)
	defaultSlotsNum = uint64(1)
)

var (
	ErrNoOracle        = errors.New("heightsync: no block oracle configured")
	ErrNilOracleHeader = errors.New("heightsync: block oracle returned nil header")
	ErrInvalidConfig   = errors.New("heightsync: invalid scheduler config (K < SlotsNum)")
)

// HeightSyncSection is the light attestation payload attached to an envelope.
//
// In this PoC we only emit Omit (nil section) or Anchor with
// proof_type=height-anchor-v1, mainnet_height, and mainnet_block_hash_hex.
type HeightSyncSection struct {
	ChainID             string `json:"chain_id,omitempty"`
	ProofType           string `json:"proof_type,omitempty"`
	MainnetHeight       int64  `json:"mainnet_height,omitempty"`
	MainnetBlockHashHex string `json:"mainnet_block_hash_hex,omitempty"`
	TimestampUnixMs     int64  `json:"timestamp_unix_ms,omitempty"`
	Direction           string `json:"direction,omitempty"`
	// OriginatorSenderID is the host (or future signed identity) that first
	// observed (MainnetHeight, MainnetBlockHashHex) from its oracle. Preserved
	// across carry-forward; must not be overwritten by the carrier.
	OriginatorSenderID string `json:"originator_sender_id,omitempty"`
	// OriginatorTimestampMs is when the originator built its Anchor; used for
	// freshness gating on carry-forward (see HEIGHT_SYNC_HEADERS_PROPOSAL).
	OriginatorTimestampMs int64 `json:"originator_timestamp_unix_ms,omitempty"`
	// SenderSignature is the host originator signature on the response leg (Step 8).
	// Omitted on courier request-leg carry-forward. JSON/SSE uses base64 encoding.
	SenderSignature []byte `json:"sender_signature,omitempty"`
}

// DecideHints steers when Anchor must be emitted.
//
// Cadence rule applied by AnchorScheduler.Decide:
//
//	inSyncTurn(nonce) ==
//	  (nonce >= 1 && nonce <= SlotsNum) ||                // initial sync turn
//	  (nonce >= K && nonce % K < SlotsNum)                // periodic sync turns
//
//	emit Anchor IFF
//	  escrow forced window ||
//	  ForceAnchor (legacy single-message; tests only) ||
//	  SessionStart ||
//	  inSyncTurn(Nonce) && !cadenceSwallowedTail(Nonce)
//
// All other nonces are Omit. Constraint: K >= SlotsNum so sync turns
// never overlap.
type DecideHints struct {
	// Nonce is the monotonic outgoing envelope nonce in this session
	// direction. Cadence is sampled per nonce.
	Nonce uint64
	// SessionStart is an explicit override; redundant when
	// Nonce <= SlotsNum (cadence already emits Anchor).
	SessionStart bool
	// ForceAnchor is a legacy single-message override when escrow state
	// does not carry a forced turn (tests / hosts without diff tx).
	ForceAnchor bool
	// Escrow carries MsgForceHeightSyncTurn-derived windows from escrow state.
	Escrow *EscrowHeightSyncHints
	// OriginatorSenderID, when non-empty, marks this emission as originating from
	// the local oracle path (host or user with a follower). Courier re-emits
	// (Step 3+) leave this empty so originator fields are taken from the cached
	// section instead of being overwritten here.
	OriginatorSenderID string
	// Recipient identifies the outbound inference peer for lazy carry-forward
	// (courier user). Empty on host-side emitters.
	Recipient string
	// Propagator supplies per-recipient last_propagated for lazy emit in Omit
	// windows. Nil disables lazy emission (host local-oracle path).
	Propagator LazyPropagator
}

// LazyPropagator tracks which mainnet heights were already sent to each peer.
type LazyPropagator interface {
	ShouldPropagateTo(recipient string, h uint64) bool
}

// AnchorScheduler emits periodic Anchor sections using the local block
// oracle. Cadence is nonce-based with sync-turn windows of SlotsNum
// consecutive nonces every K nonces; the rest are Omit unless overridden
// by hints.
type AnchorScheduler struct {
	mu       sync.Mutex
	k        uint64
	slotsNum uint64
	source   OracleSource
	now      func() time.Time
}

// NewAnchorScheduler constructs a sync-turn scheduler backed by src.
//
// k       — envelope nonces between sync turns (defaults to 10 when 0).
// slots   — sync-turn window width (defaults to 1 when 0).
//
// Returns ErrInvalidConfig when k < slots, since overlapping sync
// turns make the cadence semantics ambiguous.
func NewAnchorScheduler(k, slots uint64, src OracleSource) (*AnchorScheduler, error) {
	if k == 0 {
		k = defaultAnchorK
	}
	if slots == 0 {
		slots = defaultSlotsNum
	}
	if k < slots {
		return nil, fmt.Errorf("%w: K=%d slots=%d", ErrInvalidConfig, k, slots)
	}
	return &AnchorScheduler{
		k:        k,
		slotsNum: slots,
		source:   src,
		now:      time.Now,
	}, nil
}

// NewAnchorSchedulerFromOracle is a convenience wrapper for host-side callers.
func NewAnchorSchedulerFromOracle(k, slots uint64, oracle blockoracle.BlockOracle) (*AnchorScheduler, error) {
	return NewAnchorScheduler(k, slots, NewLocalOracleSource(oracle))
}

// K returns the scheduler K (sync-turn spacing).
func (s *AnchorScheduler) K() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.k
}

// SlotsNum returns the sync-turn window width.
func (s *AnchorScheduler) SlotsNum() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.slotsNum
}

// MustNewAnchorScheduler panics if the configuration is invalid; useful
// in tests and call-sites that already validated the config.
func MustNewAnchorScheduler(k, slots uint64, src OracleSource) *AnchorScheduler {
	s, err := NewAnchorScheduler(k, slots, src)
	if err != nil {
		panic(err)
	}
	return s
}

// MustNewAnchorSchedulerFromOracle is a convenience wrapper for host-side callers.
func MustNewAnchorSchedulerFromOracle(k, slots uint64, oracle blockoracle.BlockOracle) *AnchorScheduler {
	return MustNewAnchorScheduler(k, slots, NewLocalOracleSource(oracle))
}

// Decide returns an Anchor section when requested by hints or cadence.
// It returns nil for Omit mode when an Anchor is not due.
// The third bool is oracleMiss: true when an Anchor was required but the
// oracle returned an error or nil header (including ErrNoOracle when
// forceOracle is set).
func (s *AnchorScheduler) Decide(ctx context.Context, h DecideHints) (*HeightSyncSection, error, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cadenceEmit := s.shouldEmit(h)
	lazyEmit := false
	if !cadenceEmit {
		lazyEmit = s.shouldLazyEmit(ctx, h)
	}
	if !cadenceEmit && !lazyEmit {
		return nil, nil, false
	}

	forceOracle := h.ForceAnchor || inEscrowForcedWindow(h)

	if s.source == nil {
		if forceOracle {
			return nil, ErrNoOracle, true
		}
		return nil, nil, false
	}

	if s.source.Stale() {
		if forceOracle {
			return nil, ErrNoOracle, true
		}
		return nil, nil, true
	}

	sec, err := s.source.LatestSection(ctx)
	if err != nil {
		if forceOracle {
			return nil, fmt.Errorf("latest section: %w", err), true
		}
		return nil, nil, true
	}
	if sec == nil {
		if forceOracle {
			return nil, ErrNilOracleHeader, true
		}
		return nil, nil, true
	}

	nowMs := s.now().UnixMilli()
	out := *sec
	out.TimestampUnixMs = nowMs
	if id := h.OriginatorSenderID; id != "" {
		out.OriginatorSenderID = id
		out.OriginatorTimestampMs = nowMs
	}
	return &out, nil, false
}

func inEscrowForcedWindow(h DecideHints) bool {
	if h.Escrow == nil || h.Escrow.ForcedEnd == 0 {
		return false
	}
	return h.Nonce >= h.Escrow.ForcedStart && h.Nonce <= h.Escrow.ForcedEnd
}

func (s *AnchorScheduler) shouldEmit(h DecideHints) bool {
	k, slots := s.k, s.slotsNum
	if h.Escrow != nil {
		if h.Escrow.TurnK != 0 {
			k = h.Escrow.TurnK
		}
		if h.Escrow.TurnSlots != 0 {
			slots = h.Escrow.TurnSlots
		}
	}

	if inEscrowForcedWindow(h) || h.ForceAnchor || h.SessionStart {
		return true
	}
	if h.Nonce == 0 {
		return false
	}
	// Initial sync turn: nonces 1..slotsNum (scheduler slots).
	if h.Nonce <= slots {
		return true
	}
	// Periodic sync turn: every K nonces a new window of slotsNum
	// consecutive nonces opens at A=K, 2K, 3K, ...
	if h.Nonce >= k && h.Nonce%k < slots {
		if h.Escrow != nil && h.Escrow.CadenceSwallowUntil != 0 {
			if h.Nonce > h.Escrow.SwallowFe && h.Nonce <= h.Escrow.CadenceSwallowUntil {
				return false
			}
		}
		return true
	}
	return false
}

// shouldLazyEmit is true in Omit windows when the courier has a fresh tip that
// has not yet been sent to Recipient (see HEIGHT_SYNC_HEADERS_PROPOSAL lazy carry).
func (s *AnchorScheduler) shouldLazyEmit(ctx context.Context, h DecideHints) bool {
	if h.Recipient == "" || h.Propagator == nil {
		return false
	}
	if s.source == nil || s.source.Stale() {
		return false
	}
	sec, err := s.source.LatestSection(ctx)
	if err != nil || sec == nil || sec.MainnetHeight <= 0 {
		return false
	}
	return h.Propagator.ShouldPropagateTo(h.Recipient, uint64(sec.MainnetHeight))
}
