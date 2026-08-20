package heightsync

// DefaultFloorWindow bounds retained floor entries. One entry is appended per
// *increase* of the reference height, not per nonce, so this covers a long
// session: at one block per second it is over an hour of continuous advance.
const DefaultFloorWindow = 4096

type floorEntry struct {
	nonce  uint64
	height uint64
	hash   []byte
}

// FloorIndex answers F(m): the highest reference height stamped in the log at
// nonces strictly below m.
//
// Every Diff-resident height feeds it, because every Diff-resident height is a
// reference height (spec §14). One party raising the floor does not put the
// others in an impossible position: the floor is itself in the log, so lifting
// to it is always available, which is what makes L0 an exact check with no
// tolerance band.
//
// Entries are appended in nonce order and hold a running maximum, so AsOf is a
// binary search and the structure is cheap to clone for trial-apply.
type FloorIndex struct {
	entries   []floorEntry
	window    int
	truncated bool
}

// NewFloorIndex constructs an empty index with the default window.
func NewFloorIndex() *FloorIndex {
	return &FloorIndex{window: DefaultFloorWindow}
}

// Observe folds the maximum reference height of one diff into the index.
// A zero height or absent hash is not a claim and is ignored (H38 presence rule).
func (f *FloorIndex) Observe(diffNonce, height uint64, hash []byte) {
	if f == nil || height == 0 || !StampPresent(hash) {
		return
	}
	if f.window == 0 {
		f.window = DefaultFloorWindow
	}
	if n := len(f.entries); n > 0 {
		last := f.entries[n-1]
		if height <= last.height {
			return // running maximum unchanged
		}
		if diffNonce <= last.nonce {
			// Diffs apply in nonce order, so this is a re-observation of the same
			// nonce. Raise in place rather than appending out of order, which
			// would break the binary search.
			f.entries[n-1] = floorEntry{
				nonce:  last.nonce,
				height: height,
				hash:   append([]byte(nil), hash...),
			}
			return
		}
	}
	f.entries = append(f.entries, floorEntry{
		nonce:  diffNonce,
		height: height,
		hash:   append([]byte(nil), hash...),
	})
	if len(f.entries) > f.window {
		drop := len(f.entries) - f.window
		f.entries = append([]floorEntry(nil), f.entries[drop:]...)
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
	if f == nil || len(f.entries) == 0 {
		return 0, nil, true
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
			return 0, nil, false
		}
		return 0, nil, true
	}
	e := f.entries[lo-1]
	return e.height, e.hash, true
}

// Max is the highest reference height in the index.
func (f *FloorIndex) Max() (uint64, []byte) {
	if f == nil || len(f.entries) == 0 {
		return 0, nil
	}
	e := f.entries[len(f.entries)-1]
	return e.height, e.hash
}

// Len reports retained entries. Test and metrics seam for the bound.
func (f *FloorIndex) Len() int {
	if f == nil {
		return 0
	}
	return len(f.entries)
}

// Clone returns a deep copy so trial-apply cannot leak into committed state.
func (f *FloorIndex) Clone() *FloorIndex {
	if f == nil {
		return nil
	}
	return &FloorIndex{
		entries:   append([]floorEntry(nil), f.entries...),
		window:    f.window,
		truncated: f.truncated,
	}
}
