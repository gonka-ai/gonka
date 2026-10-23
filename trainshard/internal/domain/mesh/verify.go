package mesh

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"

	"trainshard/internal/domain/shared/vo"
)

type Pair struct {
	A vo.NodeRef
	B vo.NodeRef
}

func NewPair(a, b vo.NodeRef) Pair {
	if b.Less(a) {
		a, b = b, a
	}
	return Pair{A: a, B: b}
}

func FullyConnected(nodes []vo.NodeRef, failed []Pair) bool {
	members := index(nodes)
	for _, p := range failed {
		if members[p.A] && members[p.B] {
			return false
		}
	}
	return true
}

// Worst names nothing for two nodes: a broken link between them blames both equally, and kicking
// either leaves a mesh of one that only looks connected
func Worst(shardID vo.ShardID, nodes []vo.NodeRef, failed []Pair) (vo.NodeRef, bool) {
	members := index(nodes)
	if len(members) < 3 {
		return vo.NodeRef{}, false
	}
	counts := make(map[vo.NodeRef]int, len(nodes))
	for _, p := range failed {
		if !members[p.A] || !members[p.B] {
			continue
		}
		counts[p.A]++
		counts[p.B]++
	}

	var worst vo.NodeRef
	var worstKey [sha256.Size]byte
	best := 0
	for _, node := range nodes {
		count := counts[node]
		if count == 0 || count < best {
			continue
		}
		key := tieKey(shardID, node)
		if count > best || bytes.Compare(key[:], worstKey[:]) < 0 {
			worst, worstKey, best = node, key, count
		}
	}
	return worst, best > 0
}

func tieKey(shardID vo.ShardID, node vo.NodeRef) [sha256.Size]byte {
	buf := make([]byte, 8, 8+len(node.Participant)+1+len(node.NodeID))
	binary.BigEndian.PutUint64(buf, uint64(shardID))
	buf = append(buf, node.Participant...)
	buf = append(buf, 0)
	buf = append(buf, node.NodeID...)
	return sha256.Sum256(buf)
}

func index(nodes []vo.NodeRef) map[vo.NodeRef]bool {
	members := make(map[vo.NodeRef]bool, len(nodes))
	for _, n := range nodes {
		members[n] = true
	}
	return members
}
