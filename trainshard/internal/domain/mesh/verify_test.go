package mesh_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"testing"

	"trainshard/internal/domain/mesh"
	"trainshard/internal/domain/shared/vo"
)

func TestNewPairIsOrderIndependent(t *testing.T) {
	// act
	forward := mesh.NewPair(nodeA, nodeB)
	backward := mesh.NewPair(nodeB, nodeA)

	// assert
	if forward != backward {
		t.Fatalf("got %v and %v, want the same pair", forward, backward)
	}
}

func TestFullyConnected(t *testing.T) {
	// arrange
	nodes := []vo.NodeRef{nodeA, nodeB, nodeC}
	outsider := vo.NodeRef{Participant: "gonka1zzz", NodeID: "node-9"}

	cases := []struct {
		name   string
		failed []mesh.Pair
		want   bool
	}{
		{name: "no failed pairs", want: true},
		{name: "one pair inside the run", failed: []mesh.Pair{mesh.NewPair(nodeA, nodeB)}},
		{
			name:   "failed pair involving a node that already left",
			failed: []mesh.Pair{mesh.NewPair(nodeA, outsider)},
			want:   true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// act
			got := mesh.FullyConnected(nodes, tc.failed)

			// assert
			if got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestWorstPicksTheNodeWithMoreFailedPairs(t *testing.T) {
	// arrange
	nodes := []vo.NodeRef{nodeA, nodeB, nodeC}
	failed := []mesh.Pair{mesh.NewPair(nodeC, nodeA), mesh.NewPair(nodeC, nodeB)}

	for id := vo.ShardID(1); id <= 16; id++ {
		// act
		worst, found := mesh.Worst(id, nodes, failed)

		// assert
		if !found || worst != nodeC {
			t.Fatalf("shard %d: got %v found=%v, want %v", id, worst, found, nodeC)
		}
	}
}

func TestWorstBreaksATieByTheShardHashWhateverTheOrder(t *testing.T) {
	// arrange
	failed := []mesh.Pair{mesh.NewPair(nodeA, nodeB)}
	want := nodeA
	if keyA, keyB := tieKeyOf(shardID, nodeA), tieKeyOf(shardID, nodeB); bytes.Compare(keyB[:], keyA[:]) < 0 {
		want = nodeB
	}

	for _, nodes := range [][]vo.NodeRef{{nodeA, nodeB, nodeC}, {nodeC, nodeB, nodeA}, {nodeB, nodeC, nodeA}} {
		for range 3 {
			// act
			worst, found := mesh.Worst(shardID, nodes, failed)

			// assert
			if !found || worst != want {
				t.Fatalf("order %v: got %v found=%v, want %v", nodes, worst, found, want)
			}
		}
	}
}

func TestWorstBreaksATieDifferentlyFromShardToShard(t *testing.T) {
	// arrange
	nodes := []vo.NodeRef{nodeA, nodeB, nodeC}
	failed := []mesh.Pair{mesh.NewPair(nodeA, nodeB)}
	picked := make(map[vo.NodeRef]bool)

	// act
	for id := vo.ShardID(1); id <= 64; id++ {
		worst, _ := mesh.Worst(id, nodes, failed)
		picked[worst] = true
	}

	// assert
	if len(picked) != 2 || !picked[nodeA] || !picked[nodeB] {
		t.Fatalf("got %v picked across shards, want each end of the broken link blamed in some shard", picked)
	}
}

func tieKeyOf(id vo.ShardID, node vo.NodeRef) [sha256.Size]byte {
	buf := binary.BigEndian.AppendUint64(nil, uint64(id))
	buf = append(buf, node.Participant...)
	buf = append(buf, 0)
	buf = append(buf, node.NodeID...)
	return sha256.Sum256(buf)
}

func TestWorstBlamesNeitherOfTwoNodes(t *testing.T) {
	// act
	_, found := mesh.Worst(shardID, []vo.NodeRef{nodeA, nodeB}, []mesh.Pair{mesh.NewPair(nodeA, nodeB)})

	// assert
	if found {
		t.Fatal("a broken link between two nodes must not autokick either of them")
	}
}

func TestWorstFindsNothingWithoutFailures(t *testing.T) {
	// act
	_, found := mesh.Worst(shardID, []vo.NodeRef{nodeA, nodeB}, nil)

	// assert
	if found {
		t.Fatal("a fully connected mesh must not autokick anyone")
	}
}
