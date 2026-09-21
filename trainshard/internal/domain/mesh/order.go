package mesh

import (
	"slices"

	"trainshard/internal/domain/shared/vo"
)

func Order(shardID vo.ShardID, members []Member) (Config, error) {
	if len(members) == 0 {
		return Config{}, ErrNoMembers
	}
	ordered := slices.Clone(members)
	slices.SortFunc(ordered, func(a, b Member) int {
		switch {
		case a.Node.Less(b.Node):
			return -1
		case b.Node.Less(a.Node):
			return 1
		default:
			return 0
		}
	})

	peers := make([]Peer, 0, len(ordered))
	for i, m := range ordered {
		if m.Node.IsZero() || m.Address == "" || m.PublicKey == "" {
			return Config{}, ErrIncompleteMember
		}
		if i > 0 && ordered[i-1].Node == m.Node {
			return Config{}, ErrDuplicateNode
		}
		peers = append(peers, Peer{Rank: i, Node: m.Node, Address: m.Address, PublicKey: m.PublicKey})
	}
	return Config{Shard: shardID, Peers: peers}, nil
}
