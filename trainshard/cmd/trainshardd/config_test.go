package main

import (
	"strings"
	"testing"
)

func onADockerMachine(t *testing.T, nodes string) {
	t.Helper()

	t.Setenv("TRAINSHARD_PARTICIPANT", "gonka1host")
	t.Setenv("TRAINSHARD_NODES", nodes)
	t.Setenv("TRAINSHARD_SHARED_SECRET", "secret")
	t.Setenv("TRAINSHARD_MACHINE", "docker")
	t.Setenv("TRAINSHARD_MESH_ENDPOINT", "203.0.113.7")
	t.Setenv("TRAINSHARD_CONTAINER_MEMORY_BYTES", "1073741824")
	t.Setenv("TRAINSHARD_CONTAINER_NANO_CPUS", "4000000000")
}

func TestADockerMachineTakesOneNode(t *testing.T) {

	onADockerMachine(t, "node1,node2")

	_, err := load()

	if err == nil || !strings.Contains(err.Error(), "TRAINSHARD_NODES") {
		t.Fatalf("got %v, want a host with real cards refused a second node", err)
	}
}

func TestADockerMachineWithOneNodeLoads(t *testing.T) {

	onADockerMachine(t, "node1")

	cfg, err := load()

	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.nodes) != 1 || cfg.nodes[0].NodeID != "node1" {
		t.Fatalf("got %+v, want the one node the host holds", cfg.nodes)
	}
}
