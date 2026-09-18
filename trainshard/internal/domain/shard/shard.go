package shard

import "trainshard/internal/domain/shared/vo"

type Status string

const (
	StatusUnknown Status = "unknown"
	StatusActive  Status = "active"
	StatusSettled Status = "settled"
	StatusExpired Status = "expired"
)

type ReservedNode struct {
	Ref      vo.NodeRef
	ModelID  string
	Endpoint vo.Endpoint
}

type Shard struct {
	ID              vo.ShardID
	Creator         vo.Address
	RunKey          vo.Address
	Status          Status
	BaseImage       vo.ImageDigest
	ExpiresAtHeight vo.Height
	Nodes           []ReservedNode
}

func (s Shard) IsActive(height vo.Height) bool {
	return s.Status == StatusActive && height < s.ExpiresAtHeight
}

func (s Shard) Reserves(ref vo.NodeRef) bool {
	for _, n := range s.Nodes {
		if n.Ref == ref {
			return true
		}
	}
	return false
}

func (s Shard) ReservesAny(refs []vo.NodeRef) bool {
	for _, ref := range refs {
		if s.Reserves(ref) {
			return true
		}
	}
	return false
}

func (s Shard) Refs() []vo.NodeRef {
	refs := make([]vo.NodeRef, 0, len(s.Nodes))
	for _, n := range s.Nodes {
		refs = append(refs, n.Ref)
	}
	return refs
}

type machine struct {
	participant vo.Participant
	endpoint    vo.Endpoint
}

// Hosts groups the reserved nodes by the machine that serves them: nodes of one participant
// living behind different endpoints are different hosts, and nodes that published no endpoint
// form a host of their own that no address reaches. Hosts come in the order their first node
// was named
func (s Shard) Hosts() []vo.Host {
	order := make([]machine, 0, len(s.Nodes))
	nodes := make(map[machine][]vo.NodeRef, len(s.Nodes))
	for _, n := range s.Nodes {
		key := machine{participant: n.Ref.Participant, endpoint: n.Endpoint}
		if _, seen := nodes[key]; !seen {
			order = append(order, key)
		}
		nodes[key] = append(nodes[key], n.Ref)
	}

	hosts := make([]vo.Host, 0, len(order))
	for _, key := range order {
		hosts = append(hosts, vo.Host{Participant: key.participant, Endpoint: key.endpoint, Nodes: nodes[key]})
	}
	return hosts
}

func (s Shard) HostOf(ref vo.NodeRef) (vo.Host, bool) {
	return vo.HostOf(s.Hosts(), ref)
}

// Unaddressed names the reserved nodes whose host published no endpoint. Nothing a coordinator
// does reaches them, and nothing within the shard changes that: the address is copied in at
// assemble
func (s Shard) Unaddressed() []vo.NodeRef {
	refs := make([]vo.NodeRef, 0)
	for _, n := range s.Nodes {
		if n.Endpoint.IsZero() {
			refs = append(refs, n.Ref)
		}
	}
	return refs
}
