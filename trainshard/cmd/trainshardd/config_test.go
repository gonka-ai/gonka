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
	t.Setenv("TRAINSHARD_ENDPOINT", "http://host.example.com:8000/")
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
	if cfg.endpoint != "http://host.example.com:8000" {
		t.Fatalf("got endpoint %q, want the trailing slash dropped so a path appends cleanly", cfg.endpoint)
	}
}

func TestAPrivateMeshIsOnlyTakenWhenAskedFor(t *testing.T) {
	cases := map[string]struct {
		raw     string
		private bool
		refused bool
	}{
		"unset":     {raw: "", private: false},
		"asked for": {raw: "true", private: true},
		"declined":  {raw: "false", private: false},
		"garbled":   {raw: "sure", refused: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// arrange
			onADockerMachine(t, "node1")
			t.Setenv("TRAINSHARD_MESH_PRIVATE", tc.raw)

			// act
			cfg, err := load()

			// assert
			if tc.refused {
				if err == nil || !strings.Contains(err.Error(), "TRAINSHARD_MESH_PRIVATE") {
					t.Fatalf("got %v, want %q refused", err, tc.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if cfg.meshPrivate != tc.private {
				t.Fatalf("got private %v, want %v", cfg.meshPrivate, tc.private)
			}
		})
	}
}

func TestEveryMachineHasToSayWhereItIsReached(t *testing.T) {
	cases := map[string]struct{ machine, endpoint string }{
		"unset":               {"docker", ""},
		"no scheme":           {"docker", "host.example.com:8000"},
		"unset on memory too": {"memory", ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// arrange
			onADockerMachine(t, "node1")
			t.Setenv("TRAINSHARD_MACHINE", tc.machine)
			if tc.machine == "memory" {
				t.Setenv("TRAINSHARD_GPUS", "8")
				t.Setenv("TRAINSHARD_GPU_MODEL", "H100")
			}
			t.Setenv("TRAINSHARD_ENDPOINT", tc.endpoint)

			// act
			_, err := load()

			// assert
			if err == nil || !strings.Contains(err.Error(), "TRAINSHARD_ENDPOINT") {
				t.Fatalf("got %v, want the endpoint refused", err)
			}
		})
	}
}

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

func TestADurationThatIsNotPositiveIsRefused(t *testing.T) {
	cases := map[string]string{
		"a zero loop interval":        "TRAINSHARD_RECONCILE_INTERVAL=0s",
		"a zero refresh interval":     "TRAINSHARD_REFRESH_INTERVAL=0s",
		"a negative signature window": "TRAINSHARD_SIGNATURE_WINDOW=-1m",
	}
	for name, setting := range cases {
		t.Run(name, func(t *testing.T) {
			// arrange
			onADockerMachine(t, "node1")
			variable, value, _ := strings.Cut(setting, "=")
			t.Setenv(variable, value)

			// act
			_, err := load()

			// assert
			if err == nil || !strings.Contains(err.Error(), variable) {
				t.Fatalf("got %v, want %s refused", err, setting)
			}
		})
	}
}

func TestTheRequestLogOutlivesEverySignatureThatStillPasses(t *testing.T) {
	cases := []struct {
		name    string
		ttl     string
		window  string
		refused bool
	}{
		{name: "the defaults"},
		{name: "exactly twice the window", ttl: "10m", window: "5m"},
		{name: "shorter than twice the window", ttl: "9m", window: "5m", refused: true},
		{name: "shorter than the default window", ttl: "1m", refused: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// arrange
			onADockerMachine(t, "node1")
			t.Setenv("TRAINSHARD_REQUEST_TTL", tc.ttl)
			t.Setenv("TRAINSHARD_SIGNATURE_WINDOW", tc.window)

			// act
			_, err := load()

			// assert
			if !tc.refused {
				if err != nil {
					t.Fatalf("load: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "TRAINSHARD_REQUEST_TTL") || !strings.Contains(err.Error(), "TRAINSHARD_SIGNATURE_WINDOW") {
				t.Fatalf("got %v, want a request ttl shorter than twice the signature window refused", err)
			}
		})
	}
}

func TestADockerMachineReadsItsCardsRatherThanBeingToldThem(t *testing.T) {
	// arrange
	onADockerMachine(t, "node1")
	t.Setenv("TRAINSHARD_GPUS", "8")
	t.Setenv("TRAINSHARD_GPU_MODEL", "H100")

	// act
	_, err := load()

	// assert
	if err == nil || !strings.Contains(err.Error(), "nvidia-smi") {
		t.Fatalf("got %v, want declared cards refused on a docker machine", err)
	}
}

func TestAMemoryMachineIsRefusedWithoutItsCards(t *testing.T) {
	// arrange
	onADockerMachine(t, "node1")
	t.Setenv("TRAINSHARD_MACHINE", "memory")

	// act
	_, err := load()

	// assert
	if err == nil || !strings.Contains(err.Error(), "TRAINSHARD_GPUS") {
		t.Fatalf("got %v, want a memory machine refused without cards", err)
	}
}

func TestAMemoryMachineTakesTheCardsItIsTold(t *testing.T) {
	// arrange
	onADockerMachine(t, "node1")
	t.Setenv("TRAINSHARD_MACHINE", "memory")
	t.Setenv("TRAINSHARD_GPUS", "8")
	t.Setenv("TRAINSHARD_GPU_MODEL", "H100")

	// act
	cfg, err := load()

	// assert
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.inventory.Profile != "H100 x8" || cfg.inventory.Count != 8 {
		t.Fatalf("got %+v, want the declared cards as the chain would name them", cfg.inventory)
	}
}
