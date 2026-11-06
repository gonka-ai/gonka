package heightsync

import (
	"bytes"
	"fmt"
	"math"
	"sort"
)

// DefaultFloorWindow bounds retained floor entries. One entry is appended per
// *increase* of the reference height, not per nonce, so this covers a long
// session: at one block per second it is over an hour of continuous advance.
const DefaultFloorWindow = 4096

// SealDrainFloorLagBlocks is how far behind the latest floor the seal and
// drain clocks read. The latest floor is one host stamp, and the strong proof
// is not shipped. Dispute rollback lands later; until then money moves only
// on a height that already has this many later floor blocks in the log, long
// enough for a mismatch to have been marked. Four is 2×D (D = 2).
const SealDrainFloorLagBlocks uint64 = 4

// SequencerSigner is the FloorClaim.Signer of a sequencer-composed stamp
// (MsgStartInference, MsgHeartbeat). Slot ids are dense from zero, so the top of
// the range is free for the one party that owns no slot.
const SequencerSigner = ^uint32(0)

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
	// TimeMs is the stamp's observed_time_ms. Zero means unset. The floor
	// keeps the first non-zero time for a given (height, hash); a later
	// disagree warns and does not rewrite.
	TimeMs int64
}

// FloorConfig tunes how much of the floor is retained. It carries no raise
// bound: see FloorIndex for why the distance a host-signed claim may move F is
// not the log plane's question.
type FloorConfig struct {
	// Window bounds retained entries (DefaultFloorWindow when zero).
	Window int
}

func (c FloorConfig) withDefaults() FloorConfig {
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
	timeMs int64
}

// FloorPoint is F(m) with the identity that put it there. The author is what
// keeps blame with the originator of a bad height rather than with the honest
// parties that carried it (L6). TimeMs is the carried header time of the stamp
// that first set this point (first non-zero wins); zero means unset.
type FloorPoint struct {
	Height uint64
	Hash   []byte
	Author uint32
	Nonce  uint64
	TimeMs int64
}

// FloorTimeMismatch is a later stamp that repeats an established floor
// (height, hash) with a different non-zero time. The index keeps the first
// time; callers warn and do not roll back.
type FloorTimeMismatch struct {
	Height uint64
	Hash   []byte
	Kept   int64
	Got    int64
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
// The floor is the high-water mark of *host-signed* claims, and nothing more:
//
//   - Any host-signed first-party stamp at a height above the standing floor
//     raises it, however far above. A host ack, confirm or finish at the live
//     tip establishes the turn's reference height. MsgHeartbeat and
//     MsgStartInference never do: they are user-signed carries of F at most
//     (rule 3), so SequencerSigner is skipped here.
//   - Nothing lowers it. A reorg leaves the floor where it was and producers
//     carry it until the live branch passes it, which needs no backward motion
//     and keeps L0 exact.
//
// There is deliberately no cap on the distance of a raise, and no corroboration
// quorum. Both existed once (W_conf unaided / Q) and were wrong in the same way:
// they made the floor's own bound the protection against a fabricated height,
// which the floor cannot be. A height enters the log only through an exchange
// whose envelope was admitted, and admission is where implausibility is caught —
// `|H − local_aligned| > D` demands Strong proof of the *sender* (§8/§15), so no
// party can put a height in the future without producing a chain artifact for it
// (until Strong lands, L5a marks the same condition). Capping the floor instead
// bought nothing and broke the case it was supposed to cover: one participant
// stamping H=1 into a fresh escrow pinned F at 1, and every honest host at the
// real tip was then more than W_conf away and could never move it — a poisoned
// floor no honest party could repair.
//
// The rule is a function of the applied log alone — no clock, no oracle, no
// admission decision — so every verifier computes the same floor and therefore
// the same L0 verdicts.
//
// Entries are appended in nonce order and hold a running maximum, so AsOf is a
// binary search and the structure is cheap to clone for trial-apply.
type FloorIndex struct {
	entries    []floorEntry
	cfg        FloorConfig
	truncated  bool
	mismatches []FloorTimeMismatch
}

// NewFloorIndex constructs an empty index with the default retention.
func NewFloorIndex() *FloorIndex {
	return NewFloorIndexWith(FloorConfig{})
}

// NewFloorIndexWith constructs an empty index with explicit retention.
func NewFloorIndexWith(cfg FloorConfig) *FloorIndex {
	return &FloorIndex{cfg: cfg.withDefaults()}
}

// Observe folds one diff's reference-height claims into the index. Claims are
// attributed, so the entry names the signer that established the height and L6
// can keep blame with it rather than with the parties that carried it.
//
// A zero height or absent hash is not a claim and is ignored (spec §14).
//
// TimeMs on a new raise is taken from the raising claim. A later host stamp
// that repeats the same (height, hash) applies first-non-zero-wins: fill when
// the entry still has 0, warn (via DrainTimeMismatches) and keep the first
// when a later non-zero disagrees.
func (f *FloorIndex) Observe(diffNonce uint64, claims []FloorClaim) {
	if f == nil || len(claims) == 0 {
		return
	}
	f.cfg = f.cfg.withDefaults()
	f.mismatches = f.mismatches[:0]

	present := make([]FloorClaim, 0, len(claims))
	for _, c := range claims {
		if c.Height == 0 || !StampPresent(c.Hash) {
			continue
		}
		if c.Signer == SequencerSigner {
			continue // rule 3: user-signed stamps never raise F
		}
		present = append(present, c)
	}
	if len(present) == 0 {
		return
	}
	// Sorted so that two claims tying at the same height always attribute to the
	// same signer, whatever order they sat in the diff.
	sort.Slice(present, func(i, j int) bool {
		if present[i].Signer != present[j].Signer {
			return present[i].Signer < present[j].Signer
		}
		return present[i].Height < present[j].Height
	})

	cur, _, _ := f.tip()
	best := floorEntry{nonce: diffNonce, height: cur}
	for _, c := range present {
		if c.Height > best.height {
			best = floorEntry{
				nonce:  diffNonce,
				height: c.Height,
				hash:   c.Hash,
				author: c.Signer,
				timeMs: c.TimeMs,
			}
		}
	}
	if best.height > cur {
		f.appendEntry(best)
	}
	for _, c := range present {
		f.noteFloorTime(c)
	}
}

// noteFloorTime applies first-non-zero-wins for an established (height, hash).
// A raise that just appended already has TimeMs; this covers later confirms of
// the same point and fills a zero left by a legacy stamp.
func (f *FloorIndex) noteFloorTime(c FloorClaim) {
	if f == nil || c.TimeMs <= 0 {
		return
	}
	for i := range f.entries {
		e := &f.entries[i]
		if e.height != c.Height || !bytes.Equal(e.hash, c.Hash) {
			continue
		}
		if e.timeMs == 0 {
			e.timeMs = c.TimeMs
			return
		}
		if e.timeMs != c.TimeMs {
			f.mismatches = append(f.mismatches, FloorTimeMismatch{
				Height: c.Height,
				Hash:   append([]byte(nil), c.Hash...),
				Kept:   e.timeMs,
				Got:    c.TimeMs,
			})
		}
		return
	}
}

// DrainTimeMismatches returns and clears mismatches recorded by the last
// Observe. Callers warn; the floor time is unchanged.
func (f *FloorIndex) DrainTimeMismatches() []FloorTimeMismatch {
	if f == nil || len(f.mismatches) == 0 {
		return nil
	}
	out := append([]FloorTimeMismatch(nil), f.mismatches...)
	f.mismatches = nil
	return out
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

// PointBehind is the newest retained floor point at nonces below m whose
// height is at least lag below the current floor. known is false when that
// point may have been pruned. behind is false when the log has not yet moved
// lag blocks past a retained point: the latest raise is not a settlement clock.
// The returned time is the carried TimeMs of that older point.
func (f *FloorIndex) PointBehind(m, lag uint64) (point FloorPoint, known, behind bool) {
	cur, known := f.PointAsOf(m)
	if !known {
		return FloorPoint{}, false, false
	}
	if lag == 0 {
		if cur.Height == 0 {
			return FloorPoint{}, true, false
		}
		return cur, true, true
	}
	if cur.Height <= lag || len(f.entries) == 0 {
		return FloorPoint{}, true, false
	}
	maxH := cur.Height - lag
	lo, hi := 0, len(f.entries)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if f.entries[mid].height <= maxH {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo == 0 {
		if f.truncated {
			return FloorPoint{}, false, false
		}
		return FloorPoint{}, true, false
	}
	e := f.entries[lo-1]
	if e.nonce >= m || e.height > maxH {
		return FloorPoint{}, true, false
	}
	return FloorPoint{
		Height: e.height,
		Hash:   append([]byte(nil), e.hash...),
		Author: e.author,
		Nonce:  e.nonce,
		TimeMs: e.timeMs,
	}, true, true
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
	return FloorPoint{Height: e.height, Hash: e.hash, Author: e.author, Nonce: e.nonce, TimeMs: e.timeMs}, true
}

// PointAtHeight returns the floor entry whose height is exactly height.
// Heights in the index are the running maximum, so each height appears once.
// A pruned entry is not returned.
func (f *FloorIndex) PointAtHeight(height uint64) (FloorPoint, bool) {
	if f == nil || height == 0 || len(f.entries) == 0 {
		return FloorPoint{}, false
	}
	lo, hi := 0, len(f.entries)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if f.entries[mid].height < height {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo >= len(f.entries) || f.entries[lo].height != height {
		return FloorPoint{}, false
	}
	e := f.entries[lo]
	return FloorPoint{Height: e.height, Hash: append([]byte(nil), e.hash...), Author: e.author, Nonce: e.nonce, TimeMs: e.timeMs}, true
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

// Clone returns a copy so trial-apply cannot leak into committed state. The
// apply journal calls it on diffs that carry a floor claim, so the hashes are
// shared rather than copied: appendEntry stores a fresh slice and nothing
// rewrites one in place afterwards.
func (f *FloorIndex) Clone() *FloorIndex {
	if f == nil {
		return nil
	}
	return &FloorIndex{
		entries:   append([]floorEntry(nil), f.entries...),
		cfg:       f.cfg,
		truncated: f.truncated,
		// mismatches are ephemeral Observe output; not cloned into trial state.
	}
}

// ApplyConfig replaces the retention bound. Snapshot blobs omit cfg.
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

func addSat(a, b uint64) uint64 {
	if a > math.MaxUint64-b {
		return math.MaxUint64
	}
	return a + b
}
