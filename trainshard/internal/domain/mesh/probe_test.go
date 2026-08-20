package mesh_test

import (
	"context"
	"testing"

	"trainshard/internal/domain/mesh"
)

func TestProbeReportsABrokenLinkOnlyOnce(t *testing.T) {

	hosts := newHostsStub()
	config := configOf(nodeA, nodeB, nodeC)
	hosts.failed[nodeA] = []mesh.Pair{mesh.NewPair(nodeA, nodeB)}
	hosts.failed[nodeB] = []mesh.Pair{mesh.NewPair(nodeB, nodeA)}

	failed := mesh.Probe(context.Background(), hosts, config)

	if len(failed) != 1 || failed[0] != mesh.NewPair(nodeA, nodeB) {
		t.Fatalf("got %v, want the pair reported by both ends counted once", failed)
	}
}

func TestProbeTakesAHostItCannotAskAsUnreachableByEveryone(t *testing.T) {

	hosts := newHostsStub()
	config := configOf(nodeA, nodeB, nodeC)
	hosts.probeErr[nodeC] = true

	failed := mesh.Probe(context.Background(), hosts, config)

	if mesh.FullyConnected(config.Refs(), failed) {
		t.Fatalf("got a connected mesh, want the silent node counted as broken")
	}
	worst, found := mesh.Worst(config.Refs(), failed)
	if !found || worst != nodeC {
		t.Fatalf("got %s, want the silent node to be the worst one", worst)
	}
}

func TestProbeFindsNothingWhenEveryNodeSeesTheOthers(t *testing.T) {

	hosts := newHostsStub()
	config := configOf(nodeA, nodeB, nodeC)

	failed := mesh.Probe(context.Background(), hosts, config)

	if len(failed) != 0 {
		t.Fatalf("got %v, want no broken links", failed)
	}
}
