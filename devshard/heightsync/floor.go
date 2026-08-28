package heightsync

import (
	"fmt"
	"math"
	"sort"
)

// DefaultFloorWindow bounds retained floor entries. One entry is appended per
// *increase* of the reference height, not per nonce, so this covers a long
// session: at one block per second it is over an hour of continuous advance.
const DefaultFloorWindow = 4096

// SequencerSigner is the FloorClaim.Signer of a sequencer-composed stamp
// (MsgStartInference, MsgHeartbeat). Slot ids are dense from zero, so the top of
// the range is free for the one party that owns no slot.
const SequencerSigner = ^uint32(0)

// MinFloorQuorum is the default corroboration when FloorConfig.Quorum is left
// unset (NewFloorIndex). FloorConfigFor instead uses host-only Q:
// ceil(2/3 × slots_num), clamped to [1, slots_num], so a one-slot escrow can
// still seed F and SequencerSigner is never padded in to make a fake second vote.
const MinFloorQuorum = 2

// FloorClaim is one Diff-resident reference height together with the identity
// that signed it.
//
// Attribution is what makes the raise rule resistant to a single party: without
// it, one signer echoing itself across five messages is indistinguishable from
// five parties agreeing. Every message type names its signer in the log —
// slot_id on an ack, executor_slot on a finish, the executor of record for a
// confirm, the sequencer for the legs it composes.
type FloorClaim struct {
	Signer uint32
	Height uint64
	Hash   []byte
}

// FloorConfig tunes how the floor may move.
type FloorConfig struct {
	// Quorum is how many distinct *host* signers must hold a height before the
	// floor may jump further than WindowBlocks in one step. Zero means
	// MinFloorQuorum; FloorConfigFor sets a host-only value in [1, slots_num].
	Quorum int
	// WindowBlocks is W_conf: how far one signer may raise the floor unaided.
	WindowBlocks uint64
	// Window bounds retained entries (DefaultFloorWindow when zero).
	Window int
}

// FloorConfigFor derives the raise rule from an escrow's host roster. Quorum is
// ceil(2/3 × slots_num) over host-signed log-resident claims — SequencerSigner
// never fills a seat — so a jump nobody else can vouch for does not become the
// escrow's logical time.
func FloorConfigFor(slotsNum int, cfg HeartbeatConfig) FloorConfig {
	q := QuorumForRoster(slotsNum)
	if q < 1 {
		q = 1
	}
	if slotsNum > 0 && q > slotsNum {
		q = slotsNum
	}
	return FloorConfig{
		Quorum:       q,
		WindowBlocks: cfg.withDefaults().WindowBlocks,
	}
}

func (c FloorConfig) withDefaults() FloorConfig {
	if c.Quorum <= 0 {
		c.Quorum = MinFloorQuorum
	}
	if c.WindowBlocks == 0 {
		c.WindowBlocks = DefaultConfirmWindowBlocks
	}
	if c.Window == 0 {
		c.Window = DefaultFloorWindow
	}
	return c
}

type floorEntry struct {
	nonce  uint64
	height uint64
	hash   []byte
	author uint32
}

// FloorPoint is F(m) with the identity that put it there. The author is what
// keeps blame with the originator of a bad height rather than with the honest
// parties that carried it (L6).
type FloorPoint struct {
	Height uint64
	Hash   []byte
	Author uint32
	Nonce  uint64
}

type floorSignerClaim struct {
	height uint64
	hash   []byte
}

// FloorIndex answers F(m): the reference height the log had established at
// nonces strictly below m.
//
// Every Diff-resident height feeds it, because every Diff-resident height is a
// reference height (spec §14). One party raising the floor does not put the
// others in an impossible position: the floor is itself in the log, so lifting
// to it is always available, which is what makes L0 an exact check with no
// tolerance band.
//
// How far the floor may move in one step is bounded (proposal §14), with
// sequencer stamps excluded (rule 3):
//
//   - Within W_conf of the standing floor a single *host* signer may raise it.
//     That is the ordinary path — a cadence of one turnover every Interval keeps
//     honest advances far inside the window — so a host ack or confirm at the
//     live tip establishes the turn's reference height. MsgHeartbeat and
//     MsgStartInference never do: they are user-signed carries at most.
//   - Beyond W_conf the floor moves only to a height Quorum distinct *host*
//     signers hold. SequencerSigner does not count toward Q, so sequencer + one
//     host cannot jump 1<<40. A lone host claim of 1<<40 leaves the floor where
//     it was instead of putting it past anything any chain will reach.
//   - Nothing lowers it. A reorg is followed by producers declining to carry a
//     floor beyond reach (HeartbeatConfig.FloorOutOfReach) and resuming once the
//     live branch passes it, which needs no backward motion and keeps L0 exact.
//
// Both bounds are functions of the applied log alone — no clock, no oracle, no
// admission decision — so every verifier computes the same floor and therefore
// the same L0 verdicts.
//
// Entries are appended in nonce order and hold a running maximum, so AsOf is a
// binary search and the structure is cheap to clone for trial-apply.
type FloorIndex struct {
	entries []floorEntry
	// claims is the per-signer high-water claim. Bounded by the roster, and the
	// only state the raise rule needs beyond the entries themselves.
	claims    map[uint32]floorSignerClaim
	cfg       FloorConfig
	truncated bool
}

// NewFloorIndex constructs an empty index with the default raise rule.
func NewFloorIndex() *FloorIndex {
	return NewFloorIndexWith(FloorConfig{})
}

// NewFloorIndexWith constructs an empty index with an explicit raise rule.
func NewFloorIndexWith(cfg FloorConfig) *FloorIndex {
	return &FloorIndex{
		claims: make(map[uint32]floorSignerClaim),
		cfg:    cfg.withDefaults(),
	}
}

// Observe folds one diff's reference-height claims into the index and returns
// the marks the raise rule produced. Claims are attributed, so a signer that
// strikes out on its own is named at the moment of the damage rather than after
// a forensic replay.
//
// A zero height or absent hash is not a claim and is ignored (spec §14).
func (f *FloorIndex) Observe(diffNonce uint64, claims []FloorClaim) []AttributableMark {
	if f == nil || len(claims) == 0 {
		return nil
	}
	f.cfg = f.cfg.withDefaults()
	if f.claims == nil {
		f.claims = make(map[uint32]floorSignerClaim)
	}

	present := make([]FloorClaim, 0, len(claims))
	for _, c := range claims {
		if c.Height == 0 || !StampPresent(c.Hash) {
			continue
		}
		present = append(present, c)
	}
	if len(present) == 0 {
		return nil
	}
	sort.Slice(present, func(i, j int) bool {
		if present[i].Signer != present[j].Signer {
			return present[i].Signer < present[j].Signer
		}
		return present[i].Height < present[j].Height
	})
	for _, c := range present {
		if c.Signer == SequencerSigner {
			continue // rule 3: user-signed stamps never raise and never vote
		}
		if held := f.claims[c.Signer]; c.Height > held.height {
			f.claims[c.Signer] = floorSignerClaim{
				height: c.Height,
				hash:   append([]byte(nil), c.Hash...),
			}
		}
	}

	cur, _, _ := f.tip()
	best := floorEntry{nonce: diffNonce, height: cur}
	unaided := addSat(cur, f.cfg.WindowBlocks)
	for _, c := range present {
		if c.Signer == SequencerSigner {
			continue
		}
		if c.Height <= unaided && c.Height > best.height {
			best = floorEntry{nonce: diffNonce, height: c.Height, hash: c.Hash, author: c.Signer}
		}
	}
	if corr, ok := f.corroborated(); ok && corr.height > best.height {
		best = floorEntry{nonce: diffNonce, height: corr.height, hash: corr.hash, author: corr.author}
	}
	if best.height > cur {
		f.appendEntry(best)
	}
	return f.outOfBandMarks(diffNonce, present)
}

// outOfBandMarks names a signer whose claim is nowhere near any other party's.
//
// The test is deliberately not "beyond the raise bound": an escrow bootstrapping
// on mainnet heights, or one whose whole roster follows a chain that jumped, has
// every honest claim far above a floor that has not caught up yet, and marking
// those would bury the signal. What is anomalous is a height no peer is within
// W_conf of — a party alone on a limb. The first claimant of a genuine jump is
// briefly in that position too, so this is evidence, not a verdict.
func (f *FloorIndex) outOfBandMarks(diffNonce uint64, present []FloorClaim) []AttributableMark {
	cur, _, _ := f.tip()
	if cur == 0 {
		return nil // no logical time yet: nothing to be out of band with
	}
	var marks []AttributableMark
	for _, c := range present {
		peer := f.peerMax(c.Signer)
		if peer == 0 || c.Height <= addSat(peer, f.cfg.WindowBlocks) {
			continue
		}
		marks = append(marks, AttributableMark{
			Kind:  MarkFloorOutOfBand,
			Slot:  markSlot(c.Signer),
			Nonce: diffNonce,
			Detail: fmt.Sprintf("%s claimed height %d; nearest other signer is at %d, floor %d, W_conf %d",
				FloorAuthorLabel(c.Signer), c.Height, peer, cur, f.cfg.WindowBlocks),
		})
	}
	return marks
}

// corroborated is the highest height Quorum distinct signers hold, with the
// claim of the signer that completes it.
//
// Carried claims cannot launder a poisoned height through this. A carry equals
// the standing floor, and the floor is already at or below the Quorum-th
// ranked value, so echoing it can never lift that value.
func (f *FloorIndex) corroborated() (floorEntry, bool) {
	ranked := make([]floorEntry, 0, len(f.claims))
	for signer, held := range f.claims {
		if signer == SequencerSigner {
			continue
		}
		ranked = append(ranked, floorEntry{height: held.height, hash: held.hash, author: signer})
	}
	if len(ranked) < f.cfg.Quorum {
		return floorEntry{}, false
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].height != ranked[j].height {
			return ranked[i].height > ranked[j].height
		}
		return ranked[i].author < ranked[j].author
	})
	q := ranked[f.cfg.Quorum-1]
	return q, q.height > 0
}

func (f *FloorIndex) peerMax(signer uint32) uint64 {
	var best uint64
	for other, held := range f.claims {
		if other == signer {
			continue
		}
		if held.height > best {
			best = held.height
		}
	}
	return best
}

func (f *FloorIndex) tip() (uint64, []byte, uint32) {
	if len(f.entries) == 0 {
		return 0, nil, 0
	}
	e := f.entries[len(f.entries)-1]
	return e.height, e.hash, e.author
}

func (f *FloorIndex) appendEntry(e floorEntry) {
	e.hash = append([]byte(nil), e.hash...)
	if n := len(f.entries); n > 0 && e.nonce <= f.entries[n-1].nonce {
		// Diffs apply in nonce order, so this is a re-observation of the same
		// nonce. Raise in place rather than appending out of order, which would
		// break the binary search.
		e.nonce = f.entries[n-1].nonce
		f.entries[n-1] = e
		return
	}
	f.entries = append(f.entries, e)
	if len(f.entries) > f.cfg.Window {
		drop := len(f.entries) - f.cfg.Window
		// Header shift: the window is bounded, so copying 4096 entries on every
		// overflow is wasted. The backing array is released on the next grow.
		f.entries = f.entries[drop:]
		f.truncated = true
	}
}

// AsOf returns F(m) and the hash of the block that set it.
//
// known is false only when m falls at or before a pruned entry, where the exact
// floor is no longer recoverable. Callers must skip the check in that case
// rather than substitute a higher floor, since inventing a floor would reject
// honest stamps. Reaching this requires a stamp older than DefaultFloorWindow
// reference-height increases.
func (f *FloorIndex) AsOf(m uint64) (height uint64, hash []byte, known bool) {
	p, known := f.PointAsOf(m)
	return p.Height, p.Hash, known
}

// PointAsOf is AsOf plus the identity that established the floor.
func (f *FloorIndex) PointAsOf(m uint64) (FloorPoint, bool) {
	if f == nil || len(f.entries) == 0 {
		return FloorPoint{}, true
	}
	lo, hi := 0, len(f.entries)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if f.entries[mid].nonce < m {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo == 0 {
		if f.truncated {
			return FloorPoint{}, false
		}
		return FloorPoint{}, true
	}
	e := f.entries[lo-1]
	return FloorPoint{Height: e.height, Hash: e.hash, Author: e.author, Nonce: e.nonce}, true
}

// Max is the highest reference height in the index.
func (f *FloorIndex) Max() (uint64, []byte) {
	if f == nil {
		return 0, nil
	}
	h, hash, _ := f.tip()
	return h, hash
}

// Len reports retained entries. Test and metrics seam for the bound.
func (f *FloorIndex) Len() int {
	if f == nil {
		return 0
	}
	return len(f.entries)
}

// Clone returns a deep copy so trial-apply and snapshot restore cannot leak
// into the live index.
func (f *FloorIndex) Clone() *FloorIndex {
	if f == nil {
		return nil
	}
	entries := make([]floorEntry, len(f.entries))
	for i, e := range f.entries {
		entries[i] = floorEntry{
			nonce:  e.nonce,
			height: e.height,
			hash:   append([]byte(nil), e.hash...),
			author: e.author,
		}
	}
	claims := make(map[uint32]floorSignerClaim, len(f.claims))
	for signer, held := range f.claims {
		claims[signer] = floorSignerClaim{
			height: held.height,
			hash:   append([]byte(nil), held.hash...),
		}
	}
	return &FloorIndex{
		entries:   entries,
		claims:    claims,
		cfg:       f.cfg,
		truncated: f.truncated,
	}
}

// ApplyConfig replaces the raise-rule parameters. Snapshot blobs omit cfg;
// restore recomputes it from the roster.
func (f *FloorIndex) ApplyConfig(cfg FloorConfig) {
	if f == nil {
		return
	}
	f.cfg = cfg.withDefaults()
}

// FloorAuthorLabel names a claim signer for marks and log lines.
func FloorAuthorLabel(signer uint32) string {
	if signer == SequencerSigner {
		return "sequencer"
	}
	return fmt.Sprintf("slot %d", signer)
}

// markSlot keeps the sequencer out of the slot field, which is slot-indexed
// everywhere else; FloorAuthorLabel carries the identity in Detail instead.
func markSlot(signer uint32) uint32 {
	if signer == SequencerSigner {
		return 0
	}
	return signer
}

func addSat(a, b uint64) uint64 {
	if a > math.MaxUint64-b {
		return math.MaxUint64
	}
	return a + b
}
