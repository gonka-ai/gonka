package usecases_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"trainshard/internal/domain/mesh"
	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/vo"
)

const (
	shardID = vo.ShardID(7)
	hostA   = vo.Participant("gonka1alice")
	hostB   = vo.Participant("gonka1bob")
)

var (
	nodeA   = vo.NodeRef{Participant: hostA, NodeID: "node-a"}
	nodeB   = vo.NodeRef{Participant: hostB, NodeID: "node-b"}
	nodeC   = vo.NodeRef{Participant: hostB, NodeID: "node-c"}
	errHost = errors.New("host does not answer")
)

func shardOf(nodes ...vo.NodeRef) shard.Shard {
	reserved := make([]shard.ReservedNode, 0, len(nodes))
	for _, node := range nodes {
		reserved = append(reserved, shard.ReservedNode{Ref: node})
	}
	return shard.Shard{
		ID:              shardID,
		Creator:         "gonka1creator",
		Status:          shard.StatusActive,
		ExpiresAtHeight: 1000,
		Nodes:           reserved,
	}
}

func identityOf(node vo.NodeRef) mesh.Identity {
	return mesh.Identity{
		Member: mesh.Member{
			Node:      node,
			Address:   "10.0.0." + string(node.NodeID[len(node.NodeID)-1]),
			PublicKey: "key-" + string(node.NodeID),
		},

		Signature: []byte(node.Participant),
	}
}

type release struct {
	node   vo.NodeRef
	reason vo.ReleaseReason
}

type chainStub struct {
	record   shard.Shard
	found    bool
	height   vo.Height
	releases []release
	applies  bool
	err      error
}

func newChainStub() *chainStub {
	return &chainStub{record: shardOf(nodeA, nodeB, nodeC), found: true, height: 500, applies: true}
}

func (c *chainStub) Height(context.Context) (vo.Height, error) { return c.height, c.err }

func (c *chainStub) Shard(context.Context, vo.ShardID) (shard.Shard, bool, error) {
	return c.record, c.found, c.err
}

func (c *chainStub) Reservation(context.Context, vo.NodeRef) (vo.ShardID, bool, error) {
	return shardID, true, nil
}

func (c *chainStub) ActiveShards(context.Context) ([]shard.Shard, error) { return nil, nil }

func (c *chainStub) Hardware(context.Context, vo.NodeRef) (vo.GPUInventory, error) {
	return vo.GPUInventory{}, nil
}

func (c *chainStub) OptIn(context.Context, vo.NodeRef, time.Duration) error { return nil }

func (c *chainStub) Release(_ context.Context, _ vo.ShardID, node vo.NodeRef, reason vo.ReleaseReason) error {
	if c.err != nil {
		return c.err
	}
	c.releases = append(c.releases, release{node: node, reason: reason})
	if !c.applies {
		return nil
	}

	kept := make([]shard.ReservedNode, 0, len(c.record.Nodes))
	for _, reserved := range c.record.Nodes {
		if reserved.Ref != node {
			kept = append(kept, reserved)
		}
	}
	c.record.Nodes = kept
	return nil
}

type hostsStub struct {
	mu         sync.Mutex
	identities map[vo.Participant][]mesh.Identity
	applied    []vo.NodeRef
	failed     map[vo.NodeRef][]mesh.Pair
	silent     map[vo.Participant]bool
}

func newHostsStub() *hostsStub {
	return &hostsStub{
		identities: map[vo.Participant][]mesh.Identity{
			hostA: {identityOf(nodeA)},
			hostB: {identityOf(nodeB), identityOf(nodeC)},
		},
		failed: map[vo.NodeRef][]mesh.Pair{},
		silent: map[vo.Participant]bool{},
	}
}

func (h *hostsStub) Identities(_ context.Context, _ vo.ShardID, participant vo.Participant) ([]mesh.Identity, error) {
	if h.silent[participant] {
		return nil, errHost
	}
	return h.identities[participant], nil
}

func (h *hostsStub) Apply(_ context.Context, _ mesh.Config, node vo.NodeRef) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.silent[node.Participant] {
		return errHost
	}
	h.applied = append(h.applied, node)
	return nil
}

func (h *hostsStub) Probe(_ context.Context, cfg mesh.Config, node vo.NodeRef) ([]mesh.Pair, error) {
	pairs := make([]mesh.Pair, 0)
	for _, pair := range h.failed[node] {
		if cfg.Contains(pair.A) && cfg.Contains(pair.B) {
			pairs = append(pairs, pair)
		}
	}
	return pairs, nil
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
