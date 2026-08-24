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

// Address is the mesh address a rank answers on. The coordinator hands out the rank, the host
// raises its interface with it and the run inside reads it back, so all three derive it here
func Address(shardID vo.ShardID, rank int) (string, error) {
	if rank < 0 || rank > 253 {
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

func (c Config) Refs() []vo.NodeRef {
	refs := make([]vo.NodeRef, 0, len(c.Peers))
	for _, p := range c.Peers {
		refs = append(refs, p.Node)
	}
	return refs
}
