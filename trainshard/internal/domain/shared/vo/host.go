package vo

import (
	"slices"

	"trainshard/internal/domain/shared"
)

var ErrNodeNotServed = shared.New("NODE_NOT_SERVED", shared.ErrValidation, "node is not one of this host's nodes")

// Host is the machine a request is addressed to: the participant it answers for, where it is
// reached, and the nodes it serves, so a node id that lives on the participant's other machine is
// refused here. The endpoint is what a coordinator dials; a daemon describing itself leaves it
// zero, and a shard record with a zero one names a machine the chain holds no address for
type Host struct {
	Participant Participant
	Endpoint    Endpoint
	Nodes       []NodeRef
}

// HostOf finds the machine that serves a node among the given hosts, false if none does
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
