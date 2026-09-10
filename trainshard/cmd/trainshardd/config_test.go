package main

import (
	"strings"
	"testing"
)

func onADockerMachine(t *testing.T, nodes string) {
	t.Helper()

	t.Setenv("TRAINSHARD_PARTICIPANT", "gonka1host")
	t.Setenv("TRAINSHARD_NODES", nodes)
	t.Setenv("TRAINSHARD_KEY_NAME", "host")
	t.Setenv("TRAINSHARD_CHAIN_GRPC", "node:9090")
	t.Setenv("TRAINSHARD_DAPI", "http://api:9200")
	t.Setenv("TRAINSHARD_MACHINE", "docker")
	t.Setenv("TRAINSHARD_MESH_ENDPOINT", "203.0.113.7")
	t.Setenv("TRAINSHARD_CONTAINER_MEMORY_BYTES", "1073741824")
	t.Setenv("TRAINSHARD_CONTAINER_NANO_CPUS", "4000000000")
}

func TestADockerMachineTakesOneNode(t *testing.T) {
	// arrange
	onADockerMachine(t, "node1,node2")

	// act
	_, err := load()

	// assert
	if err == nil || !strings.Contains(err.Error(), "TRAINSHARD_NODES") {
		t.Fatalf("got %v, want a host with real cards refused a second node", err)
	}
}

func TestADockerMachineWithOneNodeLoads(t *testing.T) {
	// arrange
	onADockerMachine(t, "node1")

	// act
	cfg, err := load()

	// assert
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.nodes) != 1 || cfg.nodes[0].NodeID != "node1" {
		t.Fatalf("got %+v, want the one node the host holds", cfg.nodes)
	}
}

// These used to be answered with a stand-in: no chain meant a daemon that reserved whatever it
// was asked for
func TestTheDaemonRefusesToStandInForWhatItWasNotGiven(t *testing.T) {
	cases := map[string]string{
		"TRAINSHARD_CHAIN_GRPC": "TRAINSHARD_CHAIN_GRPC",
		"TRAINSHARD_DAPI":       "TRAINSHARD_DAPI",
		"TRAINSHARD_KEY_NAME":   "TRAINSHARD_KEY_NAME",
		"TRAINSHARD_MACHINE":    "TRAINSHARD_MACHINE",
	}

	for missing, want := range cases {
		t.Run(missing, func(t *testing.T) {
			// arrange
			onADockerMachine(t, "node1")
			t.Setenv(missing, "")

			// act
			_, err := load()

			// assert
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("got %v, want the daemon to refuse to start without %s", err, missing)
			}
		})
	}
}

func TestADockerMachineReadsItsCardsRatherThanBeingToldThem(t *testing.T) {
	onADockerMachine(t, "node1")
	t.Setenv("TRAINSHARD_GPUS", "8")
	t.Setenv("TRAINSHARD_GPU_MODEL", "H100")

	_, err := load()

	if err == nil || !strings.Contains(err.Error(), "nvidia-smi") {
		t.Fatalf("got %v, want declared cards refused on a docker machine", err)
	}
}

func TestAMemoryMachineHasToBeToldItsCards(t *testing.T) {
	onADockerMachine(t, "node1")
	t.Setenv("TRAINSHARD_MACHINE", "memory")

	_, err := load()
	if err == nil || !strings.Contains(err.Error(), "TRAINSHARD_GPUS") {
		t.Fatalf("got %v, want a memory machine refused without cards", err)
	}

	t.Setenv("TRAINSHARD_GPUS", "8")
	t.Setenv("TRAINSHARD_GPU_MODEL", "H100")
	cfg, err := load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.inventory.Profile != "H100 x8" || cfg.inventory.Count != 8 {
		t.Fatalf("got %+v, want the declared cards as the chain would name them", cfg.inventory)
	}
}
