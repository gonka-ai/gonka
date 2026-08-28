package heightsync

import (
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

// FloorIndexFromProto reconstructs a FloorIndex from a snapshot blob.
// A nil proto means the snapshot omitted the floor (legacy); callers must
// fall back to a journal replay. An empty proto is a valid fold (no claims).
func FloorIndexFromProto(cfg FloorConfig, p *types.FloorIndexProto) *FloorIndex {
	if p == nil {
		return nil
	}
	f := NewFloorIndexWith(cfg)
	if len(p.Entries) > 0 {
		f.entries = make([]floorEntry, len(p.Entries))
		for i, e := range p.Entries {
			if e == nil {
				continue
			}
			f.entries[i] = floorEntry{
				nonce:  e.Nonce,
				height: e.Height,
				hash:   append([]byte(nil), e.Hash...),
				author: e.Author,
			}
		}
	}
	if len(p.Claims) > 0 {
		f.claims = make(map[uint32]floorSignerClaim, len(p.Claims))
		for _, c := range p.Claims {
			if c == nil {
				continue
			}
			f.claims[c.Signer] = floorSignerClaim{
				height: c.Height,
				hash:   append([]byte(nil), c.Hash...),
			}
		}
	}
	f.truncated = p.Truncated
	return f
}
