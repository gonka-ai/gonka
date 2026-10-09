package types

import (
	"bytes"
	"fmt"
)

const RefusalTailLimit = 1000

// RefusalPackage is immutable after construction. Snapshot stays opaque in transport.
type RefusalPackage struct {
	EscrowID   string
	Version    string
	N          uint64
	T          uint64
	Snapshot   []byte
	BaseRoot   []byte // Only for executor catch-up without a snapshot.
	Diffs      []Diff
	Signatures map[uint64]map[uint32][]byte
}

func (p *RefusalPackage) CheckBounds() error {
	if p == nil || p.N > p.T || p.T-p.N > RefusalTailLimit || uint64(len(p.Diffs)) != p.T-p.N {
		return fmt.Errorf("invalid refusal package bounds")
	}
	if len(p.Snapshot) == 0 {
		if len(p.BaseRoot) != 32 || len(p.Signatures) != 0 {
			return fmt.Errorf("invalid refusal catch-up base")
		}
	} else if len(p.BaseRoot) != 0 {
		return fmt.Errorf("snapshot and catch-up base are mutually exclusive")
	}
	for i, d := range p.Diffs {
		if d.Nonce != p.N+uint64(i)+1 || len(d.PostStateRoot) != 32 {
			return fmt.Errorf("invalid refusal diff at %d", i)
		}
	}
	return nil
}

var rootedSnapshotPrefix = []byte("devshard-snapshot-root-v1\x00")

// RootedSnapshot stores the checked root in the same atomic blob as the state.
func RootedSnapshot(data, root []byte) ([]byte, error) {
	if len(root) != 32 {
		return nil, fmt.Errorf("invalid snapshot root")
	}
	out := append([]byte(nil), rootedSnapshotPrefix...)
	out = append(out, root...)
	return append(out, data...), nil
}

func SnapshotData(data []byte) (payload, root []byte, err error) {
	if !bytes.HasPrefix(data, rootedSnapshotPrefix) {
		return data, nil, nil
	}
	data = data[len(rootedSnapshotPrefix):]
	if len(data) <= 32 {
		return nil, nil, fmt.Errorf("truncated rooted snapshot")
	}
	return data[32:], data[:32], nil
}
