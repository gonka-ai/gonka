package mesh

import (
	"fmt"

	"trainshard/internal/domain/shared/vo"
)

type Member struct {
	Node      vo.NodeRef
	Address   string
	PublicKey string
}

type Peer struct {
	Rank      int
	Node      vo.NodeRef
	Address   string
	PublicKey string
}

type Config struct {
	Shard vo.ShardID
	Peers []Peer
}

func (c Config) Master() (Peer, bool) {
	for _, p := range c.Peers {
		if p.Rank == 0 {
			return p, true
		}
	}
	return Peer{}, false
}

func (c Config) Contains(node vo.NodeRef) bool {
	for _, p := range c.Peers {
		if p.Node == node {
			return true
		}
	}
	return false
}

func (c Config) PeersFor(node vo.NodeRef) []Peer {
	peers := make([]Peer, 0, len(c.Peers))
	for _, p := range c.Peers {
		if p.Node != node {
			peers = append(peers, p)
		}
	}
	return peers
}

// a mesh holds 254 ranks, the last octet of 10.<shard>.0.x; lifting it means spreading ranks into
// the third octet, here and nowhere else
const maxRank = 253

// Address is derived here and nowhere else: the coordinator hands out the rank, the host raises its
// interface with it and the run inside reads it back
func Address(shardID vo.ShardID, rank int) (string, error) {
	if rank < 0 || rank > maxRank {
		return "", ErrRankOffMesh
	}
	return fmt.Sprintf("10.%d.0.%d", uint64(shardID)%256, rank+1), nil
}

func (c Config) Placement(node vo.NodeRef) (vo.Placement, error) {
	for _, p := range c.Peers {
		if p.Node != node {
			continue
		}
		master, err := Address(c.Shard, 0)
		if err != nil {
			return vo.Placement{}, err
		}
		return vo.Placement{Rank: p.Rank, Size: len(c.Peers), Master: master}, nil
	}
	return vo.Placement{}, ErrNodeNotInMesh
}

// Rebuilds reports whether a node's place differs between two peer lists: a container is built with
// its rank, so a new place is a new container
func Rebuilds(previous, next Config, node vo.NodeRef) bool {
	before, err := previous.Placement(node)
	if err != nil {
		return true
	}
	after, err := next.Placement(node)
	return err != nil || before != after
}

func (c Config) Refs() []vo.NodeRef {
	refs := make([]vo.NodeRef, 0, len(c.Peers))
	for _, p := range c.Peers {
		refs = append(refs, p.Node)
	}
	return refs
}
