package mesh_test

import (
	"context"
	"errors"
	"strings"

	"trainshard/internal/domain/mesh"
	"trainshard/internal/domain/shared/vo"
)

const (
	shardID = vo.ShardID(7)
	hostA   = vo.Participant("gonka1aaa")
	hostB   = vo.Participant("gonka1bbb")
)

var errHost = errors.New("host does not answer")

func identityOf(node vo.NodeRef) mesh.Identity {
	return mesh.Identity{
		Member: mesh.Member{
			Node:      node,
			Address:   "10.0.0.1",
			PublicKey: "key-" + string(node.NodeID),
		},

		Signature: []byte(node.Participant),
	}
}

type hostsStub struct {
	identities map[vo.Participant][]mesh.Identity
	failed     map[vo.NodeRef][]mesh.Pair
	silent     map[vo.Participant]bool
	probeErr   map[vo.NodeRef]bool
}

func newHostsStub() *hostsStub {
	return &hostsStub{
		identities: map[vo.Participant][]mesh.Identity{
			hostA: {identityOf(nodeA)},
			hostB: {identityOf(nodeB), identityOf(nodeC)},
		},
		failed:   map[vo.NodeRef][]mesh.Pair{},
		silent:   map[vo.Participant]bool{},
		probeErr: map[vo.NodeRef]bool{},
	}
}

func (h *hostsStub) Identities(_ context.Context, _ vo.ShardID, participant vo.Participant) ([]mesh.Identity, error) {
	if h.silent[participant] {
		return nil, errHost
	}
	return h.identities[participant], nil
}

func (h *hostsStub) Apply(context.Context, mesh.Config, vo.NodeRef) error { return nil }

func (h *hostsStub) Probe(_ context.Context, _ mesh.Config, node vo.NodeRef) ([]mesh.Pair, error) {
	if h.probeErr[node] {
		return nil, errHost
	}
	return h.failed[node], nil
}

type verifierStub struct {
	err error
}

func (v *verifierStub) Recover(_, signature []byte) (vo.Address, error) {
	if v.err != nil {
		return "", v.err
	}
	return vo.Address(strings.TrimSpace(string(signature))), nil
}

func configOf(nodes ...vo.NodeRef) mesh.Config {
	peers := make([]mesh.Peer, 0, len(nodes))
	for rank, node := range nodes {
		peers = append(peers, mesh.Peer{Rank: rank, Node: node, Address: "10.0.0.1", PublicKey: "key"})
	}
	return mesh.Config{Shard: shardID, Peers: peers}
}

// emptyStore holds nothing, so every read answers "not here"
type emptyStore struct{ mesh.Store }

func (emptyStore) Config(context.Context, vo.ShardID, vo.NodeRef) (mesh.Config, bool, error) {
	return mesh.Config{}, false, nil
}
