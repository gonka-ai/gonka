package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

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
	want := []string{"HOME=/workspace", "MASTER_ADDR=10.1.0.1", "RANK=0", "WORLD_SIZE=8"}
	if !slices.Equal(got, want) {
		t.Fatalf("environment = %q, want %q", got, want)
	}
	if plain := environment(nil); !slices.Equal(plain, []string{"HOME=/workspace"}) {
		t.Fatalf("environment(nil) = %q, want the home the run is given", plain)
	}
	if named := environment(map[string]string{"HOME": "/elsewhere"}); !slices.Equal(named, []string{"HOME=/elsewhere"}) {
		t.Fatalf("environment = %q, want the run's own home to win", named)
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

func TestTheRunWritesOnlyToItsWorkspace(t *testing.T) {
	// arrange
	client := &Client{cfg: Config{VolumeRoot: t.TempDir()}}
	spec := run.ContainerSpec{
		Shard: vo.ShardID(42),
		Node:  node(),
		Hosts: []run.PinnedHost{{Name: "registry.example", IP: "10.0.0.7"}},
	}
	if err := os.MkdirAll(client.volumePath(spec.Shard, spec.Node), 0o700); err != nil {
		t.Fatal(err)
	}

	// act
	binds, err := client.binds(spec)
	if err != nil {
		t.Fatal(err)
	}

	// assert
	if binds[0] != client.volumePath(spec.Shard, spec.Node)+":"+workdir {
		t.Fatalf("the workspace is not the first bind: %v", binds)
	}
	for _, bind := range binds[1:] {
		if !strings.HasSuffix(bind, ":ro") {
			t.Fatalf("bind %q is writable", bind)
		}
	}
	for _, name := range []string{"hosts", "hostname", "resolv.conf"} {
		if !slices.ContainsFunc(binds, func(bind string) bool { return strings.Contains(bind, ":/etc/"+name+":ro") }) {
			t.Fatalf("/etc/%s is left to the engine: %v", name, binds)
		}
	}

	written, err := os.ReadFile(filepath.Join(client.mountsPath(spec.Shard, spec.Node), "hosts"))
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

func TestNothingTheRunProducesGrowsUnmetered(t *testing.T) {
	// arrange
	client := &Client{cfg: Config{}.withDefaults()}

	// act
	scratch, rolled := client.scratch(), client.rolled()

	// assert
	for _, path := range []string{tmpdir, rundir} {
		if !strings.Contains(scratch[path], "size=") {
			t.Fatalf("%s = %q, want a bounded tmpfs", path, scratch[path])
		}
	}
	if rolled.Config["max-size"] == "" || rolled.Config["max-file"] == "" {
		t.Fatalf("log config = %v, want a rolled log", rolled.Config)
	}
}

func TestOnlyTmpLetsTheRunLoadWhatItBuilt(t *testing.T) {
	// arrange
	client := &Client{cfg: Config{}.withDefaults()}

	// act
	scratch := client.scratch()

	// assert
	if !slices.Contains(strings.Split(scratch[tmpdir], ","), "exec") {
		t.Fatalf("%s = %q, want exec for what a jit compiler builds there", tmpdir, scratch[tmpdir])
	}
	if slices.Contains(strings.Split(scratch[rundir], ","), "exec") {
		t.Fatalf("%s = %q, want the engine's noexec kept", rundir, scratch[rundir])
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

func runSpec() run.ContainerSpec {
	return run.ContainerSpec{Shard: vo.ShardID(42), Node: node(), Run: run.RunSpec{Image: digest, Command: []string{"train"}}}
}

func TestCreateKeepsTheMountsOfAContainerTheEngineMayHaveMade(t *testing.T) {
	// arrange
	created := answer{status: http.StatusOK, body: container.InspectResponse{
		ID:     "abc",
		Name:   "/trainshard-42-node-7",
		State:  &container.State{Status: container.StateCreated},
		Config: &container.Config{Image: digest},
	}}
	cases := []struct {
		name    string
		create  answer
		inspect answer
		kept    bool
	}{
		{name: "the engine refused and made nothing", create: missing, inspect: missing, kept: false},
		{name: "the engine timed out after making it", create: answer{err: context.DeadlineExceeded}, inspect: created, kept: true},
		{name: "the engine cannot say", create: answer{status: http.StatusInternalServerError, body: map[string]string{"message": "down"}}, inspect: answer{status: http.StatusInternalServerError, body: map[string]string{"message": "down"}}, kept: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			engine, _ := stubbed(t, Config{VolumeRoot: t.TempDir()}, func(method, path string) answer {
				switch {
				case method == http.MethodPost && strings.HasSuffix(path, "/containers/create"):
					return tc.create
				case strings.HasSuffix(path, "/containers/trainshard-42-node-7/json"):
					return tc.inspect
				default:
					return missing
				}
			})
			spec := runSpec()

			// act
			err := engine.Create(context.Background(), spec)

			// assert
			if err == nil {
				t.Fatal("want the failed create reported")
			}
			_, statErr := os.Stat(engine.mountsPath(spec.Shard, spec.Node))
			if kept := statErr == nil; kept != tc.kept {
				t.Fatalf("mounts kept = %v, want %v", kept, tc.kept)
			}
		})
	}
}

func TestCreateHoldsTheRunToItsMemoryWithoutSwap(t *testing.T) {
	// arrange
	cases := []struct {
		name   string
		memory int64
		swap   int64
	}{
		{name: "no memory limit asks for no swap limit", memory: 0, swap: 0},
		{name: "a memory limit leaves no swap above it", memory: 8 << 30, swap: 8 << 30},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			engine, stub := stubbed(t, Config{VolumeRoot: t.TempDir(), MemoryBytes: tc.memory}, func(method, path string) answer {
				if method == http.MethodPost && strings.HasSuffix(path, "/containers/create") {
					return answer{status: http.StatusCreated, body: container.CreateResponse{ID: "abc"}}
				}
				return missing
			})

			// act
			err := engine.Create(context.Background(), runSpec())

			// assert
			if err != nil {
				t.Fatal(err)
			}
			sent, found := stub.sent(http.MethodPost, "/containers/create")
			if !found {
				t.Fatal("no create reached the engine")
			}
			var request container.CreateRequest
			if err := json.Unmarshal(sent.body, &request); err != nil {
				t.Fatal(err)
			}
			if request.HostConfig.Memory != tc.memory || request.HostConfig.MemorySwap != tc.swap {
				t.Fatalf("memory = %d, swap = %d, want %d and %d", request.HostConfig.Memory, request.HostConfig.MemorySwap, tc.memory, tc.swap)
			}
		})
	}
}

func TestCreateIsolatesTheRun(t *testing.T) {
	// arrange
	engine, stub := stubbed(t, Config{VolumeRoot: t.TempDir()}, func(method, path string) answer {
		if method == http.MethodPost && strings.HasSuffix(path, "/containers/create") {
			return answer{status: http.StatusCreated, body: container.CreateResponse{ID: "abc"}}
		}
		return missing
	})

	// act
	err := engine.Create(context.Background(), runSpec())

	// assert
	if err != nil {
		t.Fatal(err)
	}
	sent, _ := stub.sent(http.MethodPost, "/containers/create")
	var request container.CreateRequest
	if err := json.Unmarshal(sent.body, &request); err != nil {
		t.Fatal(err)
	}
	host := request.HostConfig
	switch {
	case request.User != "1000:1000":
		t.Fatalf("user = %q, want the unprivileged default", request.User)
	case host.Privileged || len(host.CapAdd) != 0 || !slices.Equal(host.CapDrop, []string{"ALL"}):
		t.Fatalf("privileged = %v, cap add = %v, cap drop = %v", host.Privileged, host.CapAdd, host.CapDrop)
	case !slices.Contains(host.SecurityOpt, "no-new-privileges"):
		t.Fatalf("security opts = %v", host.SecurityOpt)
	case !host.ReadonlyRootfs:
		t.Fatal("the root is writable")
	case host.NetworkMode != "none":
		t.Fatalf("network = %q, want none without a running sandbox", host.NetworkMode)
	case host.PidMode != "" || host.IpcMode != container.IPCModePrivate || host.PidsLimit == nil:
		t.Fatalf("pid mode = %q, ipc mode = %q, pids limit = %v", host.PidMode, host.IpcMode, host.PidsLimit)
	}
}

func TestStartOfAContainerThatIsGoneFails(t *testing.T) {
	// arrange
	cases := []struct {
		name  string
		start answer
		gone  bool
	}{
		{name: "already running", start: answer{status: http.StatusNotModified}},
		{name: "gone", start: missing, gone: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			engine, _ := stubbed(t, Config{}, func(string, string) answer { return tc.start })

			// act
			err := engine.Start(context.Background(), vo.ShardID(42), node())

			// assert
			if tc.gone != cerrdefs.IsNotFound(err) || (!tc.gone && err != nil) {
				t.Fatalf("err = %v, want gone = %v", err, tc.gone)
			}
		})
	}
}

func TestSandboxPullsOnlyAnImageTheEngineLacks(t *testing.T) {
	// arrange
	running := answer{status: http.StatusOK, body: container.InspectResponse{
		ID:     "net",
		State:  &container.State{Status: container.StateRunning, Running: true, Pid: 4242},
		Config: &container.Config{Image: "registry.k8s.io/pause:3.9"},
	}}
	cases := []struct {
		name   string
		cached bool
	}{
		{name: "cached", cached: true},
		{name: "not cached", cached: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			creates, inspects := 0, 0
			engine, stub := stubbed(t, Config{}, func(method, path string) answer {
				switch {
				case strings.HasSuffix(path, "/containers/create"):
					creates++
					if !tc.cached && creates == 1 {
						return missing
					}
					return answer{status: http.StatusCreated, body: container.CreateResponse{ID: "net"}}
				case strings.HasSuffix(path, "/images/create"):
					return answer{status: http.StatusOK, body: map[string]string{"status": "pulled"}}
				case strings.HasSuffix(path, "/start"):
					return answer{status: http.StatusNoContent}
				case strings.HasSuffix(path, "/json"):
					inspects++
					if inspects == 1 {
						return missing
					}
					return running
				default:
					return missing
				}
			})

			// act
			pid, err := engine.Sandbox(context.Background(), vo.ShardID(42), node())

			// assert
			if err != nil {
				t.Fatal(err)
			}
			if pid != 4242 {
				t.Fatalf("pid = %d, want the sandbox's", pid)
			}
			if _, pulled := stub.sent(http.MethodPost, "/images/create"); pulled == tc.cached {
				t.Fatalf("pulled = %v with the image cached = %v", pulled, tc.cached)
			}
		})
	}
}

func TestStopWaitsOutTheGrace(t *testing.T) {
	// arrange
	engine, stub := stubbed(t, Config{Timeout: time.Second}, func(string, string) answer {
		return answer{status: http.StatusNoContent}
	})
	grace := 10 * time.Second

	// act
	err := engine.Stop(context.Background(), vo.ShardID(42), node(), grace)

	// assert
	if err != nil {
		t.Fatal(err)
	}
	sent, found := stub.sent(http.MethodPost, "/stop")
	if !found {
		t.Fatal("no stop reached the engine")
	}
	if sent.remaining <= grace {
		t.Fatalf("the stop had %s left, want more than the %s grace", sent.remaining, grace)
	}
}
