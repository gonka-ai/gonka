package mesh

import "trainshard/internal/domain/shared/vo"

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

func (c Config) Refs() []vo.NodeRef {
	refs := make([]vo.NodeRef, 0, len(c.Peers))
	for _, p := range c.Peers {
		refs = append(refs, p.Node)
	}
	return refs
}
