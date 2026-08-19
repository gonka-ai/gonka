package heightsync

import (
	"context"
	"strings"
	"sync"
	"time"

	"devshard/chainoracle/blocks"
)

// ConfirmState is the discrete height-sync confirmation outcome for downstream consumers.
type ConfirmState int

const (
	ConfirmPending ConfirmState = iota
	ConfirmConfirmed
	ConfirmStale
)

// ConfirmationView exposes the IsStrictlyConfirmed contract (plan §3.6).
type ConfirmationView interface {
	IsStrictlyConfirmed(h uint64) ConfirmState
}

// ConfirmationRule selects which confirmation predicate IsStrictlyConfirmed uses.
type ConfirmationRule int

const (
	// RuleQuorum is (C-quorum): audit-ring originators, the testenv default.
	RuleQuorum ConfirmationRule = iota
	// RuleTurn is (C-turn): a complete SyncTurnRecord with ≥ Q counting acks.
	RuleTurn
	// RuleHybrid is ConfirmConfirmed if either Quorum or Turn clears.
	RuleHybrid
)

// ConfirmationConfig tunes quorum counting for a single verifier V.
type ConfirmationConfig struct {
	// Roster is the deployment host roster (validator addresses). Only these
	// originators count toward quorum.
	Roster []string
	// Quorum is Q; zero uses QuorumForRoster(len(Roster)).
	Quorum int
	// Freshness is F (default DefaultOriginatorFreshness).
	Freshness time.Duration
	// WindowHeights is W_conf (default 256).
	WindowHeights int64
	Oracle        blocks.BlockOracle
	Now           func() time.Time
	// Rule selects (C-quorum) / (C-turn) / hybrid. Zero is RuleQuorum.
	Rule ConfirmationRule
	// Turns is required for RuleTurn and RuleHybrid.
	Turns *TurnTracker
}

type originatorEntry struct {
	maxHeight  int64
	observedAt time.Time
}

// ConfirmationIndex tracks per-originator attestations for (C-quorum) confirmation.
type ConfirmationIndex struct {
	mu sync.Mutex

	roster        map[string]struct{}
	quorum        int
	freshness     time.Duration
	windowHeights int64
	oracle        blocks.BlockOracle
	now           func() time.Time
	rule          ConfirmationRule
	turns         *TurnTracker

	byOriginator     map[string]originatorEntry
	confirmedHeights map[uint64]struct{}
	lastCompactTip   int64
}

// QuorumForRoster returns ceil(2/3 × rosterSize) for PoC defaults.
func QuorumForRoster(rosterSize int) int {
	if rosterSize <= 0 {
		return 0
	}
	return (2*rosterSize + 2) / 3
}

// NewConfirmationIndex constructs an empty confirmation index.
func NewConfirmationIndex(cfg ConfirmationConfig) *ConfirmationIndex {
	roster := make(map[string]struct{}, len(cfg.Roster))
	for _, id := range cfg.Roster {
		id = strings.TrimSpace(id)
		if id != "" {
			roster[id] = struct{}{}
		}
	}
	q := cfg.Quorum
	if q <= 0 {
		q = QuorumForRoster(len(roster))
	}
	f := cfg.Freshness
	if f <= 0 {
		f = DefaultOriginatorFreshness
	}
	w := cfg.WindowHeights
	if w <= 0 {
		w = 256
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &ConfirmationIndex{
		roster:           roster,
		quorum:           q,
		freshness:        f,
		windowHeights:    w,
		oracle:           cfg.Oracle,
		now:              now,
		rule:             cfg.Rule,
		turns:            cfg.Turns,
		byOriginator:     make(map[string]originatorEntry),
		confirmedHeights: make(map[uint64]struct{}),
	}
}

// SetQuorum overrides Q (tests).
func (idx *ConfirmationIndex) SetQuorum(q int) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.quorum = q
}

// RecordAttestation ingests one audit-ring entry into the confirmation index.
func (idx *ConfirmationIndex) RecordAttestation(a AnchorAttestation) {
	if idx == nil || !attestationCountsForConfirmation(a) {
		return
	}
	key := originatorKey(a)
	if !idx.inRoster(key) {
		return
	}
	observedAt := time.UnixMilli(a.ObservedAtUnixMs)
	if a.ObservedAtUnixMs <= 0 {
		observedAt = idx.now()
	}

	idx.mu.Lock()
	defer idx.mu.Unlock()

	tip := idx.localTipLocked()
	if tip >= 0 {
		idx.maybeCompactLocked(tip)
	}
	if !idx.indexableHeightLocked(a.MainnetHeight, tip) {
		return
	}

	ent := idx.byOriginator[key]
	if a.MainnetHeight > ent.maxHeight {
		ent.maxHeight = a.MainnetHeight
		ent.observedAt = observedAt
	} else if a.MainnetHeight == ent.maxHeight && observedAt.After(ent.observedAt) {
		ent.observedAt = observedAt
	}
	idx.byOriginator[key] = ent
}

func turnConfirmState(turns *TurnTracker, h uint64) ConfirmState {
	if turns != nil && turns.Confirms(h) {
		return ConfirmConfirmed
	}
	return ConfirmPending
}

// IsStrictlyConfirmed implements ConfirmationView.
func (idx *ConfirmationIndex) IsStrictlyConfirmed(h uint64) ConfirmState {
	if idx == nil || h == 0 {
		return ConfirmPending
	}

	idx.mu.Lock()
	defer idx.mu.Unlock()

	if idx.rule == RuleTurn {
		return turnConfirmState(idx.turns, h)
	}

	q := idx.quorumConfirmedLocked(h)
	if q == ConfirmConfirmed {
		return ConfirmConfirmed
	}
	if idx.rule == RuleHybrid {
		if t := turnConfirmState(idx.turns, h); t == ConfirmConfirmed {
			return ConfirmConfirmed
		}
	}
	return q
}

func (idx *ConfirmationIndex) quorumConfirmedLocked(h uint64) ConfirmState {
	if _, ok := idx.confirmedHeights[h]; ok {
		return ConfirmConfirmed
	}
	if idx.isStaleLocked() {
		return ConfirmStale
	}

	tip := idx.localTipLocked()
	idx.maybeCompactLocked(tip)

	if idx.quorum <= 0 {
		return ConfirmPending
	}

	var eligible int
	for origin := range idx.roster {
		ent, ok := idx.byOriginator[origin]
		if !ok {
			continue
		}
		if !idx.originatorEligibleLocked(ent, int64(h), tip) {
			continue
		}
		eligible++
	}
	if eligible >= idx.quorum {
		idx.confirmedHeights[h] = struct{}{}
		return ConfirmConfirmed
	}
	return ConfirmPending
}

func attestationCountsForConfirmation(a AnchorAttestation) bool {
	if a.MainnetHeight <= 0 || len(a.MainnetBlockHash) == 0 {
		return false
	}
	if a.Trust == TrustDisputeCarrier {
		return false
	}
	return true
}

func originatorKey(a AnchorAttestation) string {
	if id := strings.TrimSpace(a.OriginatorSenderID); id != "" {
		return id
	}
	return strings.TrimSpace(a.PeerID)
}

func (idx *ConfirmationIndex) inRoster(originator string) bool {
	_, ok := idx.roster[originator]
	return ok
}

func (idx *ConfirmationIndex) localTipLocked() int64 {
	if idx.oracle == nil {
		return -1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	hdr, err := idx.oracle.Latest(ctx)
	if err != nil || hdr == nil {
		return -1
	}
	return hdr.Height
}

func (idx *ConfirmationIndex) isStaleLocked() bool {
	if idx.oracle == nil {
		return true
	}
	if so, ok := idx.oracle.(interface{ Stale() bool }); ok && so.Stale() {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := idx.oracle.Latest(ctx)
	return err != nil
}

// indexableHeight reports whether an attested height may enter the rolling index.
// Heights ahead of local tip are allowed (late-host carry-forward).
func (idx *ConfirmationIndex) indexableHeightLocked(height, tip int64) bool {
	if tip < 0 {
		return true
	}
	minH := tip - idx.windowHeights
	if minH < 0 {
		minH = 0
	}
	return height >= minH
}

func (idx *ConfirmationIndex) originatorEligibleLocked(ent originatorEntry, h, tip int64) bool {
	now := idx.now()
	if now.Sub(ent.observedAt) > idx.freshness {
		return false
	}
	if ent.maxHeight < h {
		return false
	}
	if tip >= 0 && ent.maxHeight < tip-idx.windowHeights {
		return false
	}
	return true
}

func (idx *ConfirmationIndex) maybeCompactLocked(tip int64) {
	if tip < 0 || tip == idx.lastCompactTip {
		return
	}
	idx.lastCompactTip = tip
	now := idx.now()
	minH := tip - idx.windowHeights
	if minH < 0 {
		minH = 0
	}
	for origin, ent := range idx.byOriginator {
		if ent.maxHeight < minH || now.Sub(ent.observedAt) > idx.freshness {
			delete(idx.byOriginator, origin)
		}
	}
}
