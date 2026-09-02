package heightsync

import (
	"errors"
	"fmt"
	"sort"

	"devshard/types"
)

// ToProto serializes the derived floor for the snapshot envelope. It is not
// hashed into the state root. cfg is omitted and recomputed from the roster
// on restore.
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
	signers := make([]uint32, 0, len(f.claims))
	for signer := range f.claims {
		signers = append(signers, signer)
	}
	sort.Slice(signers, func(i, j int) bool { return signers[i] < signers[j] })
	claims := make([]*types.FloorSignerClaimProto, 0, len(signers))
	for _, signer := range signers {
		held := f.claims[signer]
		claims = append(claims, &types.FloorSignerClaimProto{
			Signer: signer,
			Height: held.height,
			Hash:   append([]byte(nil), held.hash...),
		})
	}
	return &types.FloorIndexProto{
		Entries:   entries,
		Claims:    claims,
		Truncated: f.truncated,
	}
}

// ErrFloorBlobInvalid marks a snapshot floor that cannot be a fold of any diff
// sequence. Callers must discard it and rebuild from the journal (or fail
// closed) rather than serve L0 verdicts from it.
var ErrFloorBlobInvalid = errors.New("height-sync floor blob invalid")

// FloorIndexFromProto reconstructs a FloorIndex from a snapshot blob.
// A nil proto means the snapshot omitted the floor (legacy); callers must
// fall back to a journal replay. An empty proto is a valid fold (no claims).
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
	if len(p.Entries) > 0 {
		f.entries = make([]floorEntry, 0, len(p.Entries))
		for i, e := range p.Entries {
			if e == nil {
				return nil, fmt.Errorf("%w: entry %d is missing", ErrFloorBlobInvalid, i)
			}
			if e.Height == 0 || !StampPresent(e.Hash) {
				return nil, fmt.Errorf("%w: entry %d has no reference height", ErrFloorBlobInvalid, i)
			}
			if n := len(f.entries); n > 0 {
				prev := f.entries[n-1]
				if e.Nonce <= prev.nonce {
					return nil, fmt.Errorf("%w: entry %d nonce %d follows %d",
						ErrFloorBlobInvalid, i, e.Nonce, prev.nonce)
				}
				if e.Height <= prev.height {
					return nil, fmt.Errorf("%w: entry %d height %d follows %d",
						ErrFloorBlobInvalid, i, e.Height, prev.height)
				}
			}
			f.entries = append(f.entries, floorEntry{
				nonce:  e.Nonce,
				height: e.Height,
				hash:   append([]byte(nil), e.Hash...),
				author: e.Author,
			})
		}
	}
	if len(p.Claims) > 0 {
		f.claims = make(map[uint32]floorSignerClaim, len(p.Claims))
		for i, c := range p.Claims {
			if c == nil {
				return nil, fmt.Errorf("%w: claim %d is missing", ErrFloorBlobInvalid, i)
			}
			if c.Height == 0 || !StampPresent(c.Hash) {
				return nil, fmt.Errorf("%w: claim %d has no reference height", ErrFloorBlobInvalid, i)
			}
			if _, dup := f.claims[c.Signer]; dup {
				return nil, fmt.Errorf("%w: claim %d repeats signer %d", ErrFloorBlobInvalid, i, c.Signer)
			}
			f.claims[c.Signer] = floorSignerClaim{
				height: c.Height,
				hash:   append([]byte(nil), c.Hash...),
			}
		}
	}
	f.truncated = p.Truncated
	return f, nil
}
