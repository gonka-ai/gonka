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
	Ref     vo.NodeRef
	ModelID string
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

func (s Shard) Participants() []vo.Participant {
	seen := make(map[vo.Participant]struct{}, len(s.Nodes))
	participants := make([]vo.Participant, 0, len(s.Nodes))
	for _, n := range s.Nodes {
		if _, ok := seen[n.Ref.Participant]; ok {
			continue
		}
		seen[n.Ref.Participant] = struct{}{}
		participants = append(participants, n.Ref.Participant)
	}
	return participants
}
