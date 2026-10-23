package chain

import (
	"math"
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

func TestReleaseRequestID_FitsTheChainLimitForTheLongestValidRefs(t *testing.T) {
	node := vo.NodeRef{
		Participant: vo.Participant("gonka1" + strings.Repeat("q", 84)),
		NodeID:      vo.NodeID(strings.Repeat("n", 64)),
	}
	if err := types.ValidateTrainingNodeId(string(node.NodeID)); err != nil {
		t.Fatalf("the longest node id the chain admits: %v", err)
	}
	reasons := []vo.ReleaseReason{
		vo.ReleaseFailedPrepare, vo.ReleaseFailedRun, vo.ReleaseUnreachable, vo.ReleaseOperatorAbort, vo.ReleaseManualKick,
	}
	for _, reason := range reasons {
		id := vo.ReleaseRequestID(vo.ShardID(math.MaxUint64), node, reason)
		if len(id) > types.MaxAutokickRequestIdLen {
			t.Fatalf("reason %s: request id of %d bytes, the chain takes at most %d", reason, len(id), types.MaxAutokickRequestIdLen)
		}
	}
}
