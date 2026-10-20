package chain

import (
	"strings"
	"testing"

	"github.com/productscience/inference/x/inference/types"

	"trainshard/internal/domain/shared/vo"
)

func TestNodeIdGrammar_ChainAndDaemonAgree(t *testing.T) {
	const participant = "gonka1host"
	vectors := []string{
		"node-1.a_b", "A", "0", "...", "a.b.c", strings.Repeat("n", 64),
		"", ".", "..", "node/1", `node\1`, "node 1", " node-1", "node-1 ", "\tnode", "nöde", "node:1", "node@1",
		strings.Repeat("n", 65),
	}
	for _, id := range vectors {
		chainErr := types.ValidateTrainingNodeId(id)
		ref, daemonErr := vo.ParseNodeRef(participant, id)
		if chainErr == nil {
			if daemonErr != nil {
				t.Fatalf("node id %q: the chain accepts it and the daemon rejects it: %v", id, daemonErr)
			}
			if string(ref.NodeID) != id {
				t.Fatalf("node id %q: the daemon reads it as %q", id, ref.NodeID)
			}
		}
		if daemonErr == nil && string(ref.NodeID) == id && chainErr != nil {
			t.Fatalf("node id %q: the daemon accepts it and the chain rejects it: %v", id, chainErr)
		}
	}
}
