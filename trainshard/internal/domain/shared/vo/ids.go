package vo

import (
	"crypto/rand"
	"fmt"
	"strconv"
	"strings"

	"trainshard/internal/domain/shared"
)

const (
	addressPrefix   = "gonka1"
	maxNodeIDLen    = 64
	maxRequestIDLen = 128
	maxAddressLen   = 128
)

type Height int64

type ShardID uint64

func ParseShardID(s string) (ShardID, error) {
	id, err := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	if err != nil || id == 0 {
		return 0, fmt.Errorf("shard_id %q: %w", s, shared.ErrValidation)
	}
	return ShardID(id), nil
}

func (id ShardID) String() string { return strconv.FormatUint(uint64(id), 10) }

func (id ShardID) IsZero() bool { return id == 0 }

type Participant string

type NodeID string

type NodeRef struct {
	Participant Participant
	NodeID      NodeID
}

func ParseNodeRef(participant, nodeID string) (NodeRef, error) {
	addr, err := ParseAddress(participant)
	if err != nil {
		return NodeRef{}, fmt.Errorf("participant: %w", err)
	}
	id := strings.TrimSpace(nodeID)
	if id == "" || len(id) > maxNodeIDLen {
		return NodeRef{}, fmt.Errorf("node_id %q: %w", nodeID, shared.ErrValidation)
	}
	return NodeRef{Participant: Participant(addr), NodeID: NodeID(id)}, nil
}

func (r NodeRef) String() string { return string(r.Participant) + "/" + string(r.NodeID) }

func (r NodeRef) IsZero() bool { return r.Participant == "" && r.NodeID == "" }

func (r NodeRef) Less(other NodeRef) bool {
	if r.Participant != other.Participant {
		return r.Participant < other.Participant
	}
	return r.NodeID < other.NodeID
}

type RequestID string

func NewRequestID() RequestID { return RequestID(rand.Text()) }

func ParseRequestID(s string) (RequestID, error) {
	id := strings.TrimSpace(s)
	if id == "" || len(id) > maxRequestIDLen {
		return "", fmt.Errorf("request_id %q: %w", s, shared.ErrValidation)
	}
	return RequestID(id), nil
}

type Address string

func ParseAddress(s string) (Address, error) {
	addr := strings.TrimSpace(s)
	if !strings.HasPrefix(addr, addressPrefix) || len(addr) > maxAddressLen {
		return "", fmt.Errorf("address %q: %w", s, shared.ErrValidation)
	}
	return Address(addr), nil
}
