package docker

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/vo"
)

const digest = "registry.example/trainer@sha256:1111111111111111111111111111111111111111111111111111111111111111"

func node() vo.NodeRef {
	return vo.NodeRef{Participant: "gonka1abc", NodeID: "node-7"}
}

func TestSettled(t *testing.T) {
	// arrange
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nothing to do", err: nil, want: true},
		{name: "already gone", err: cerrdefs.ErrNotFound, want: true},
		{name: "already in that state", err: cerrdefs.ErrNotModified, want: true},
		{name: "wrapped not found", err: fmt.Errorf("remove: %w", cerrdefs.ErrNotFound), want: true},
		{name: "conflict is a real failure", err: cerrdefs.ErrConflict, want: false},
		{name: "unknown failure", err: errors.New("engine is down"), want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// act
			got := settled(tc.err)

			// assert
			if got != tc.want {
				t.Fatalf("settled(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestPinned(t *testing.T) {
	// arrange
	cases := []struct {
		name    string
		image   vo.ImageDigest
		refused bool
	}{
		{name: "digest reference", image: digest},
		{name: "bare digest without a repository", image: "sha256:1111", refused: true},
		{name: "mutable tag", image: "registry.example/trainer:latest", refused: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// act
			err := pinned(tc.image)

			// assert
			if tc.refused && !errors.Is(err, shared.ErrValidation) {
				t.Fatalf("pinned(%q) = %v, want a validation error", tc.image, err)
			}
			if !tc.refused && err != nil {
				t.Fatalf("pinned(%q) = %v, want nil", tc.image, err)
			}
		})
	}
}

func TestToContainerState(t *testing.T) {
	// arrange
	cases := []struct {
		state container.ContainerState
		want  vo.ContainerState
	}{
		{state: container.StateCreated, want: vo.ContainerCreated},
		{state: container.StateRunning, want: vo.ContainerRunning},
		{state: container.StatePaused, want: vo.ContainerRunning},
		{state: container.StateRestarting, want: vo.ContainerRunning},
		{state: container.StateExited, want: vo.ContainerExited},
		{state: container.StateDead, want: vo.ContainerExited},
		{state: container.StateRemoving, want: vo.ContainerExited},
		{state: "something the engine grew later", want: vo.ContainerExited},
	}

	for _, tc := range cases {
		t.Run(string(tc.state), func(t *testing.T) {
			// act
			got := toContainerState(tc.state)

			// assert
			if got != tc.want {
				t.Fatalf("toContainerState(%q) = %q, want %q", tc.state, got, tc.want)
			}
		})
	}
}

func TestToContainerInfo(t *testing.T) {
	t.Run("a running container reports no exit code", func(t *testing.T) {
		// arrange
		found := container.InspectResponse{
			State:  &container.State{Status: container.StateRunning, ExitCode: 0},
			Config: &container.Config{Image: digest},
		}

		// act
		info, err := toContainerInfo(found)

		// assert
		if err != nil {
			t.Fatal(err)
		}
		if info.State != vo.ContainerRunning || info.Image != digest {
			t.Fatalf("got %+v", info)
		}
		if info.ExitCode != nil {
			t.Fatalf("exit code = %d, want none while running", *info.ExitCode)
		}
	})

	t.Run("an exited container carries its exit code", func(t *testing.T) {
		// arrange
		found := container.InspectResponse{
			State:  &container.State{Status: container.StateExited, ExitCode: 137},
			Config: &container.Config{Image: digest},
		}

		// act
		info, err := toContainerInfo(found)

		// assert
		if err != nil {
			t.Fatal(err)
		}
		if info.ExitCode == nil || *info.ExitCode != 137 {
			t.Fatalf("exit code = %v, want 137", info.ExitCode)
		}
	})

	t.Run("an unpinned image is refused", func(t *testing.T) {
		// arrange
		found := container.InspectResponse{
			State:  &container.State{Status: container.StateRunning},
			Config: &container.Config{Image: "trainer:latest"},
		}

		// act
		_, err := toContainerInfo(found)

		// assert
		if !errors.Is(err, shared.ErrValidation) {
			t.Fatalf("err = %v, want a validation error", err)
		}
	})
}

func TestEnvironmentIsOrdered(t *testing.T) {
	// arrange
	values := map[string]string{"WORLD_SIZE": "8", "RANK": "0", "MASTER_ADDR": "10.1.0.1"}

	// act
	got := environment(values)

	// assert
	want := []string{"MASTER_ADDR=10.1.0.1", "RANK=0", "WORLD_SIZE=8"}
	if !slices.Equal(got, want) {
		t.Fatalf("environment = %q, want %q", got, want)
	}
	if empty := environment(nil); len(empty) != 0 {
		t.Fatalf("environment(nil) = %q, want empty", empty)
	}
}

func TestGPURequests(t *testing.T) {
	// arrange
	cases := []struct {
		name  string
		count int
		want  int
	}{
		{name: "no gpus asked", count: 0, want: 0},
		{name: "negative is not a request", count: -1, want: 0},
		{name: "two gpus", count: 2, want: 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// act
			got := (&Client{}).gpuRequests(tc.count)

			// assert
			if len(got) != tc.want {
				t.Fatalf("gpuRequests(%d) = %+v", tc.count, got)
			}
			if tc.want == 0 {
				return
			}
			if got[0].Driver != "" || got[0].Count != tc.count {
				t.Fatalf("request = %+v", got[0])
			}
			if len(got[0].Capabilities) != 1 || !slices.Equal(got[0].Capabilities[0], []string{"gpu"}) {
				t.Fatalf("capabilities = %+v", got[0].Capabilities)
			}
		})
	}
}

// An engine that only knows a card by name is asked for it by name, one device per card
func TestGPURequestsNameTheDevicesWhenTheEngineNeedsThem(t *testing.T) {
	// act
	got := (&Client{cfg: Config{GPUKind: "nvidia.com/gpu"}}).gpuRequests(2)

	// assert
	if len(got) != 1 || got[0].Driver != "cdi" {
		t.Fatalf("request = %+v", got)
	}
	if !slices.Equal(got[0].DeviceIDs, []string{"nvidia.com/gpu=0", "nvidia.com/gpu=1"}) {
		t.Fatalf("devices = %v", got[0].DeviceIDs)
	}
}

// Cleanup finds a run's leftovers by name and label, so both are a contract and not cosmetics
func TestNamesAndLabelsIdentifyTheRun(t *testing.T) {
	// arrange
	shardID := vo.ShardID(42)

	// act
	name := containerName(shardID, node())
	sandbox := sandboxName(shardID, node())
	tags := labels(shardID, node(), "run")

	// assert
	if name != "trainshard-42-node-7" {
		t.Fatalf("container name = %q", name)
	}
	if sandbox != name+"-net" {
		t.Fatalf("sandbox name = %q, want the container name plus -net", sandbox)
	}
	if tags[labelShard] != "42" || tags[labelNode] != "node-7" || tags[labelRole] != "run" {
		t.Fatalf("labels = %v", tags)
	}
}

// A run that named no source keeps the engine's own hosts file, and one that named a source gets it
// as a file, since the engine refuses host entries to a container living in another one's network
func TestSourcesReachTheRunAsAHostsFile(t *testing.T) {
	// arrange
	client := &Client{cfg: Config{VolumeRoot: t.TempDir()}}
	spec := run.ContainerSpec{Shard: vo.ShardID(42), Node: node()}
	if err := os.MkdirAll(client.volumePath(spec.Shard, spec.Node), 0o700); err != nil {
		t.Fatal(err)
	}

	// act
	plain, err := client.binds(spec)
	if err != nil {
		t.Fatal(err)
	}

	// assert
	if len(plain) != 1 {
		t.Fatalf("binds = %v", plain)
	}

	// act
	spec.Hosts = []run.PinnedHost{{Name: "registry.example", IP: "10.0.0.7"}}
	pinned, err := client.binds(spec)
	if err != nil {
		t.Fatal(err)
	}

	// assert
	if len(pinned) != 2 || !strings.HasSuffix(pinned[1], ":/etc/hosts:ro") {
		t.Fatalf("binds = %v", pinned)
	}
	written, err := os.ReadFile(client.hostsPath(spec.Shard, spec.Node))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(written), "10.0.0.7\tregistry.example") {
		t.Fatalf("hosts file = %q", written)
	}
	if !strings.Contains(string(written), "localhost") {
		t.Fatal("the run still has to resolve localhost")
	}
}

func TestConfigDefaults(t *testing.T) {
	// act
	cfg := Config{}.withDefaults()

	// assert
	if cfg.Socket != "/var/run/docker.sock" || cfg.User != "1000:1000" {
		t.Fatalf("got %+v", cfg)
	}
	if cfg.PidsLimit != 4096 || cfg.ShmBytes != 1<<30 {
		t.Fatalf("limits = %d pids, %d shm", cfg.PidsLimit, cfg.ShmBytes)
	}
	if cfg.APIVersion != "" {
		t.Fatal("api version must stay empty so the client negotiates with whatever engine is there")
	}

	// act
	kept := Config{Socket: "/run/docker.sock", PidsLimit: 16}.withDefaults()

	// assert
	if kept.Socket != "/run/docker.sock" || kept.PidsLimit != 16 {
		t.Fatalf("defaults overwrote a set value: %+v", kept)
	}
}
