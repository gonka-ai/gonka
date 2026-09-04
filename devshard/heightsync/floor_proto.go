package heightsync

import (
	"errors"
	"fmt"

	"devshard/types"
)

// ToProto serializes the derived floor for the snapshot envelope. It is not
// hashed into the state root. cfg is omitted; it is retention only.
func (f *FloorIndex) ToProto() *types.FloorIndexProto {
	if f == nil {
		return nil
	}
	entries := make([]*types.FloorIndexEntryProto, len(f.entries))
	for i, e := range f.entries {
		entries[i] = &types.FloorIndexEntryProto{
			Nonce:  e.nonce,
			Height: e.height,
			Hash:   append([]byte(nil), e.hash...),
			Author: e.author,
		}
	}
	return &types.FloorIndexProto{
		Entries:   entries,
		Truncated: f.truncated,
	}
}

// ErrFloorBlobInvalid marks a snapshot floor that cannot be a fold of any diff
// sequence. Callers must discard it and rebuild from the journal (or fail
// closed) rather than serve L0 verdicts from it.
var ErrFloorBlobInvalid = errors.New("height-sync floor blob invalid")

// FloorIndexFromProto reconstructs a FloorIndex from a snapshot blob.
// A nil proto means the snapshot omitted the floor (legacy); callers must
// fall back to a journal replay. An empty proto is a valid fold (no entries).
//
// The blob is validated before it is trusted. AsOf binary-searches entries and
// reads the last one as a running maximum, so a blob whose nonces or heights are
// out of order would answer F(m) with a height the log never established — and
// unlike a GetDiffs failure, nothing downstream would notice. Restore treats a
// present blob as authoritative, so this is the only place that check can live.
func FloorIndexFromProto(cfg FloorConfig, p *types.FloorIndexProto) (*FloorIndex, error) {
	if p == nil {
		return nil, nil
	}
	f := NewFloorIndexWith(cfg)
	if len(p.GetEntries()) > 0 {
		f.entries = make([]floorEntry, 0, len(p.GetEntries()))
		for i, e := range p.GetEntries() {
			if e == nil {
				return nil, fmt.Errorf("%w: entry %d is missing", ErrFloorBlobInvalid, i)
			}
			if e.GetHeight() == 0 || !StampPresent(e.GetHash()) {
				return nil, fmt.Errorf("%w: entry %d has no reference height", ErrFloorBlobInvalid, i)
			}
			if n := len(f.entries); n > 0 {
				prev := f.entries[n-1]
				if e.GetNonce() <= prev.nonce {
					return nil, fmt.Errorf("%w: entry %d nonce %d follows %d",
						ErrFloorBlobInvalid, i, e.GetNonce(), prev.nonce)
				}
				if e.GetHeight() <= prev.height {
					return nil, fmt.Errorf("%w: entry %d height %d follows %d",
						ErrFloorBlobInvalid, i, e.GetHeight(), prev.height)
				}
			}
			f.entries = append(f.entries, floorEntry{
				nonce:  e.GetNonce(),
				height: e.GetHeight(),
				hash:   append([]byte(nil), e.GetHash()...),
				author: e.GetAuthor(),
			})
		}
	}
	f.truncated = p.GetTruncated()
	return f, nil
}
