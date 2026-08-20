package mesh_test

import (
	"testing"

	"trainshard/internal/domain/mesh"
	"trainshard/internal/domain/shared/vo"
)

func TestNewPairIsOrderIndependent(t *testing.T) {

	forward := mesh.NewPair(nodeA, nodeB)
	backward := mesh.NewPair(nodeB, nodeA)

	if forward != backward {
		t.Fatalf("got %v and %v, want the same pair", forward, backward)
	}
}

func TestFullyConnected(t *testing.T) {
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

			got := mesh.FullyConnected(nodes, tc.failed)

			if got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestWorstPicksTheNodeWithMoreFailedPairs(t *testing.T) {

	nodes := []vo.NodeRef{nodeA, nodeB, nodeC}
	failed := []mesh.Pair{mesh.NewPair(nodeC, nodeA), mesh.NewPair(nodeC, nodeB)}

	worst, found := mesh.Worst(nodes, failed)

	if !found || worst != nodeC {
		t.Fatalf("got %v found=%v, want %v", worst, found, nodeC)
	}
}

func TestWorstBreaksTiesTheSameWayEveryTime(t *testing.T) {

	nodes := []vo.NodeRef{nodeC, nodeB, nodeA}
	failed := []mesh.Pair{mesh.NewPair(nodeA, nodeB)}

	first, _ := mesh.Worst(nodes, failed)
	second, _ := mesh.Worst([]vo.NodeRef{nodeA, nodeB, nodeC}, failed)

	if first != nodeA || second != nodeA {
		t.Fatalf("got %v and %v, want %v both times", first, second, nodeA)
	}
}

func TestWorstFindsNothingWithoutFailures(t *testing.T) {

	_, found := mesh.Worst([]vo.NodeRef{nodeA, nodeB}, nil)

	if found {
		t.Fatal("a fully connected mesh must not autokick anyone")
	}
}
