package vo

import (
	"slices"

	"trainshard/internal/domain/shared"
)

var ErrNodeNotServed = shared.New("NODE_NOT_SERVED", shared.ErrValidation, "node is not one of this host's nodes")

// Host is the machine a request is addressed to: the participant it answers for and the nodes
// it serves, so a node id that lives on the participant's other machine is refused here
type Host struct {
	Participant Participant
	Nodes       []NodeRef
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
