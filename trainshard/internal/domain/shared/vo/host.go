package vo

import (
	"slices"

	"trainshard/internal/domain/shared"
)

var ErrNodeNotServed = shared.New("NODE_NOT_SERVED", shared.ErrValidation, "node is not one of this host's nodes")

// Host carries the nodes it serves, so a node id that lives on the participant's other machine is
// refused here. A daemon describing itself leaves Endpoint zero; in a shard record a zero one names
// a machine the chain holds no address for
type Host struct {
	Participant Participant
	Endpoint    Endpoint
	Nodes       []NodeRef
}

func HostOf(hosts []Host, ref NodeRef) (Host, bool) {
	for _, host := range hosts {
		if slices.Contains(host.Nodes, ref) {
			return host, true
		}
	}
	return Host{}, false
}

func (h Host) Node(id string) (NodeRef, error) {
	ref, err := ParseNodeRef(string(h.Participant), id)
	if err != nil {
		return NodeRef{}, err
	}
	if !slices.Contains(h.Nodes, ref) {
		return NodeRef{}, ErrNodeNotServed
	}
	return ref, nil
}
