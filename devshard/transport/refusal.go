package transport

import "devshard/types"

type RefusalPackageJSON struct {
	EscrowID   string                       `json:"escrow_id"`
	Version    string                       `json:"version"`
	N          uint64                       `json:"n"`
	T          uint64                       `json:"t"`
	Snapshot   []byte                       `json:"snapshot,omitempty"`
	BaseRoot   []byte                       `json:"base_root,omitempty"`
	Diffs      []DiffJSON                   `json:"diffs"`
	Signatures map[uint64]map[uint32][]byte `json:"signatures,omitempty"`
}

func refusalToJSON(p *types.RefusalPackage) (*RefusalPackageJSON, error) {
	if p == nil {
		return nil, nil
	}
	out := &RefusalPackageJSON{EscrowID: p.EscrowID, Version: p.Version, N: p.N, T: p.T, Snapshot: p.Snapshot, BaseRoot: p.BaseRoot, Signatures: p.Signatures}
	for _, d := range p.Diffs {
		dj, err := DiffToJSON(d)
		if err != nil {
			return nil, err
		}
		out.Diffs = append(out.Diffs, dj)
	}
	return out, nil
}
func refusalFromJSON(p *RefusalPackageJSON) (*types.RefusalPackage, error) {
	if p == nil {
		return nil, nil
	}
	out := &types.RefusalPackage{EscrowID: p.EscrowID, Version: p.Version, N: p.N, T: p.T, Snapshot: p.Snapshot, BaseRoot: p.BaseRoot, Signatures: p.Signatures}
	for _, d := range p.Diffs {
		diff, err := DiffFromJSON(d)
		if err != nil {
			return nil, err
		}
		out.Diffs = append(out.Diffs, diff)
	}
	return out, out.CheckBounds()
}
