package mesh_test

import (
	"context"
	"errors"
	"testing"

	"trainshard/internal/domain/mesh"
	"trainshard/internal/domain/shared/vo"
)

func TestApplyingAPeerListThatIsNotThereIsNotSuccess(t *testing.T) {

	runtime := mesh.Runtime{Store: emptyStore{}}

	err := runtime.Apply(context.Background(), shardID, vo.NodeRef{Participant: hostA, NodeID: "node-a"})

	if !errors.Is(err, mesh.ErrMissingConfig) {
		t.Fatalf("got %v, want a node left without a peer list to say so instead of reporting the mesh up", err)
	}
}
