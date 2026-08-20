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
	// RuleTurn is withdrawn (spec §17) and behaves as RuleQuorum.
	//
	// It read ≥ Q log-resident acks with observed_height ≥ h. Once every
	// Diff-resident height became a reference height, an ack at or above h no
	// longer witnesses that its signer saw block h — a lagging host reaches it
	// by lifting to a floor another party established, legitimately and by
	// design. Counting Q of those confirms h on one originator's claim echoed Q
	// times, which is what (C-quorum)'s distinct-originator requirement exists
	// to prevent. Turn completion still certifies reachability; it cannot
	// certify a height.
	RuleTurn
	// RuleHybrid is ConfirmConfirmed if (C-quorum) or (C-strong) clears.
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
	// Rule selects the confirmation predicate. Zero is RuleQuorum.
	Rule ConfirmationRule
	// Turns is retained for turn bookkeeping; it no longer feeds confirmation.
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
		w = int64(DefaultConfirmWindowBlocks)
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
	observedAt, ok := idx.attestationTime(a)
	if !ok {
		return
	}

	tip, _ := idx.oracleView()

	idx.mu.Lock()
	defer idx.mu.Unlock()

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

// attestationTime is the clock (C-quorum) freshness uses. Originator
// observation wins when present so a carry-forward cannot reset F at receipt.
// A non-positive originator timestamp is ineligible (step 13 fail-closed),
// except when the field is absent entirely — first-party audit rows still
// carry only ObservedAtUnixMs.
func (idx *ConfirmationIndex) attestationTime(a AnchorAttestation) (time.Time, bool) {
	if a.OriginatorTimestampMs > 0 {
		return time.UnixMilli(a.OriginatorTimestampMs), true
	}
	if a.OriginatorTimestampMs < 0 {
		return time.Time{}, false
	}
	if a.ObservedAtUnixMs > 0 {
		return time.UnixMilli(a.ObservedAtUnixMs), true
	}
	if idx.now != nil {
		return idx.now(), true
	}
	return time.Now(), true
}

// IsStrictlyConfirmed implements ConfirmationView.
//
// Only (C-quorum) is available here. (C-turn) is withdrawn (see RuleTurn) and
// (C-strong) arrives with Phase F, at which point RuleHybrid gains its second
// disjunct.
func (idx *ConfirmationIndex) IsStrictlyConfirmed(h uint64) ConfirmState {
	if idx == nil || h == 0 {
		return ConfirmPending
	}

	tip, stale := idx.oracleView()

	idx.mu.Lock()
	defer idx.mu.Unlock()

	return idx.quorumConfirmedLocked(h, tip, stale)
}

func (idx *ConfirmationIndex) quorumConfirmedLocked(h uint64, tip int64, stale bool) ConfirmState {
	idx.maybeCompactLocked(tip)
	if _, ok := idx.confirmedHeights[h]; ok {
		return ConfirmConfirmed
	}
	if stale {
		return ConfirmStale
	}
	if tip >= 0 {
		minH := tip - idx.windowHeights
		if minH < 0 {
			minH = 0
		}
		if int64(h) < minH {
			return ConfirmPending
		}
	}

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

func (idx *ConfirmationIndex) oracleView() (tip int64, stale bool) {
	if idx == nil {
		return -1, true
	}
	idx.mu.Lock()
	oracle := idx.oracle
	idx.mu.Unlock()
	return fetchConfirmOracle(oracle)
}

func fetchConfirmOracle(oracle blocks.BlockOracle) (int64, bool) {
	if oracle == nil {
		return -1, true
	}
	if so, ok := oracle.(interface{ Stale() bool }); ok && so.Stale() {
		return -1, true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	hdr, err := oracle.Latest(ctx)
	if err != nil || hdr == nil {
		return -1, true
	}
	return hdr.Height, false
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
	for h := range idx.confirmedHeights {
		if int64(h) < minH {
			delete(idx.confirmedHeights, h)
		}
	}
}

// ConfirmedCount is the size of the retained confirmed-height set (H99).
func (idx *ConfirmationIndex) ConfirmedCount() int {
	if idx == nil {
		return 0
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	return len(idx.confirmedHeights)
}
