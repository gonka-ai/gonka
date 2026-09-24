package mesh

import "trainshard/internal/domain/shared/vo"

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

// Worst names nothing for two nodes: a broken link between them blames both
// equally, and kicking either leaves a mesh of one that only looks connected.
func Worst(nodes []vo.NodeRef, failed []Pair) (vo.NodeRef, bool) {
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
	best := 0
	for _, node := range nodes {
		count := counts[node]
		if count > best || (count == best && count > 0 && node.Less(worst)) {
			worst, best = node, count
		}
	}
	return worst, best > 0
}

func index(nodes []vo.NodeRef) map[vo.NodeRef]bool {
	members := make(map[vo.NodeRef]bool, len(nodes))
	for _, n := range nodes {
		members[n] = true
	}
	return members
}
