package hosts_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	hostdrun "trainshard/internal/application/hostd/run"
	"trainshard/internal/application/hostd/session"
	"trainshard/internal/domain/mesh"
	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/vo"
	chainfake "trainshard/internal/infrastructure/adapters/chain/fake"
	"trainshard/internal/infrastructure/adapters/clock"
	"trainshard/internal/infrastructure/adapters/hosts"
	"trainshard/internal/infrastructure/adapters/memory"
	nodemanagerfake "trainshard/internal/infrastructure/adapters/nodemanager/fake"
	"trainshard/internal/infrastructure/adapters/signing/cosmos"
	"trainshard/internal/infrastructure/repositories/localstate"
	"trainshard/internal/utils/signedhttp"
)

const (
	shardID   = vo.ShardID(7)
	baseImage = "ghcr.io/gonka/base@sha256:1111111111111111111111111111111111111111111111111111111111111111"
	runImage  = "ghcr.io/gonka/train@sha256:2222222222222222222222222222222222222222222222222222222222222222"
)

// the host and the coordinator each hold their own key, the way they do in production: the whole
// point of this test is that a request crosses the wire signed and comes back believed
var (
	hostKey        = key("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
	coordinatorKey = key("c87509a1c067bbde78beb793e6fa76530b6382a4c0241e5e4a9ec0a0f44dc0d3")

	host    = vo.Participant(hostKey.Address())
	creator = coordinatorKey.Address()
	node    = vo.NodeRef{Participant: host, NodeID: "node-a"}
)

// routePrefix stands in for the participant's proxy, which reaches one of several GPU machines
// under its own prefix and strips it before the daemon sees the path: what the coordinator signs
// is the path behind the prefix, and that is what the daemon has to verify
const routePrefix = "/trainshard-node-a"

type reached struct {
	*hosts.Client
	machine vo.Host
}

func key(raw string) *cosmos.Key {
	loaded, err := cosmos.FromHex(raw)
	if err != nil {
		panic(err)
	}
	return loaded
}

func seedFile(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "seed.json")
	seed := fmt.Sprintf(`{
	  "height": 100,
	  "hardware": [{"participant": %q, "node_id": "node-a", "model": "H100", "count": 8}],
	  "shards": [{
	    "id": 7,
	    "creator": %q,
	    "run_key": "gonka1runkey",
	    "status": "active",
	    "base_image_digest": %q,
	    "expires_at_height": 100000,
	    "nodes": [{"participant": %q, "node_id": "node-a", "model_id": "llama"}]
	  }]
	}`, host, creator, baseImage, host)

	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	return path
}

func newHost(t *testing.T) reached {
	t.Helper()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	clock := clock.System{}

	chain, err := chainfake.Load(seedFile(t))
	if err != nil {
		t.Fatalf("load chain: %v", err)
	}
	state, err := localstate.New(t.TempDir())
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	machine := memory.New(log, vo.GPUInventory{Profile: "H100 x8", Count: 8})

	module := hostdrun.New(hostdrun.Config{
		Participant: host,
		Nodes:       []vo.NodeRef{node},
		Limits:      run.Limits{MaxGPUs: 8, MaxDiskBytes: 1 << 40},
		Interval:    10 * time.Millisecond,
		Patience:    time.Hour,
	}, hostdrun.Deps{
		Chain:        chain,
		Reservations: chain,
		Watcher:      chain,
		Runs:         state.Runs(),
		Requests:     state.Requests(clock, time.Hour),
		Store:        state.Mesh(),
		Network:      machine.Mesh(),
		Machine: run.Machine{
			Images:     machine,
			Containers: machine,
			Volumes:    machine,
			GPU:        machine,
			Mesh:       mesh.Runtime{Network: machine.Mesh(), Store: state.Mesh(), Attestor: hostKey},
			Egress:     machine,
			Control:    nodemanagerfake.New(log),
			Runs:       state.Runs(),
			Clock:      clock,
			StopGrace:  time.Second,
		},
		Clock: clock,
		Log:   log,
	})
	streams := session.New(session.Config{Participant: host, Nodes: []vo.NodeRef{node}}, session.Deps{
		Chain:    chain,
		Streams:  machine,
		Sessions: state.Sessions(),
		Served:   state.Served(clock),
		Clock:    clock,
	})

	mux := http.NewServeMux()
	boundary := signedhttp.New(hostKey, clock, time.Minute, vo.Address(host)).Wrap
	module.Mount(mux, boundary)
	streams.Mount(mux, boundary)

	server := httptest.NewServer(http.StripPrefix(routePrefix, mux))
	t.Cleanup(server.Close)

	ctx, stop := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { defer close(stopped); module.Run(ctx) }()
	t.Cleanup(func() { stop(); <-stopped })

	return reached{
		Client:  hosts.New(server.Client(), coordinatorKey, clock, time.Minute),
		machine: vo.Host{Participant: host, Endpoint: vo.Endpoint(server.URL + routePrefix), Nodes: []vo.NodeRef{node}},
	}
}

func command(nodes ...vo.NodeRef) run.HostCommand {
	return run.HostCommand{
		Shard:     shardID,
		Nodes:     nodes,
		RequestID: vo.RequestID(fmt.Sprintf("req-%d", time.Now().UnixNano())),
		Deadline:  time.Now().Add(time.Minute),
	}
}

func waitFor(t *testing.T, why string, ready func(run.NodeStatus) bool, client reached) run.NodeStatus {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for {
		statuses, err := client.Status(context.Background(), client.machine, command(node))
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		if len(statuses) != 1 {
			t.Fatalf("got %d statuses, want one", len(statuses))
		}
		if ready(statuses[0]) {
			return statuses[0]
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s, last status %+v", why, statuses[0])
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// meshed takes the node as far as a coordinator would before it deploys: a container is built
// with the rank its peer list gives it, so there is no run without one
func meshed(t *testing.T, client reached) {
	t.Helper()

	ctx := context.Background()
	identities, err := client.Identities(ctx, shardID, client.machine)
	if err != nil {
		t.Fatalf("identities: %v", err)
	}
	config, err := mesh.Order(shardID, []mesh.Member{identities[0].Member})
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	if err := client.Apply(ctx, config, client.machine, node); err != nil {
		t.Fatalf("apply mesh: %v", err)
	}
	waitFor(t, "the mesh to come up", func(s run.NodeStatus) bool { return s.MeshUp }, client)
}

func TestARunIsDrivenOverHTTPFromEndToEnd(t *testing.T) {

	client := newHost(t)
	ctx := context.Background()

	waitFor(t, "the node to be prepared", func(s run.NodeStatus) bool { return s.Prepared }, client)
	meshed(t, client)

	deployed, err := client.Deploy(ctx, client.machine, run.DeployCall{
		HostCommand: command(node),
		Run: run.RunSpec{
			Image:     runImage,
			Command:   []string{"train.py"},
			Env:       map[string]string{"DATA": "s3://bucket"},
			Resources: run.Resources{GPUs: 8, DiskBytes: 1 << 30},
		},
	})
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if len(deployed) != 1 || !deployed[0].OK() {
		t.Fatalf("got %+v, want the deploy accepted", deployed)
	}
	created := waitFor(t, "the container to be created", func(s run.NodeStatus) bool {
		return s.State.Exists()
	}, client)
	if created.Image != runImage {
		t.Fatalf("got %q, want the container built from the run image", created.Image)
	}

	started, err := client.Start(ctx, client.machine, command(node))

	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if !started[0].OK() {
		t.Fatalf("got %+v, want the start accepted", started[0])
	}
	waitFor(t, "the container to be running", func(s run.NodeStatus) bool { return s.State.Running() }, client)

	stopped, err := client.Stop(ctx, client.machine, run.StopCall{HostCommand: command(node), Grace: time.Second})

	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !stopped[0].OK() {
		t.Fatalf("got %+v, want the stop accepted", stopped[0])
	}
	waitFor(t, "the container to be stopped", func(s run.NodeStatus) bool { return !s.State.Running() }, client)
}

func TestTheResultIsCollectedOverHTTPBeforeTheShardCloses(t *testing.T) {

	client := newHost(t)
	ctx := context.Background()
	waitFor(t, "the node to be prepared", func(s run.NodeStatus) bool { return s.Prepared }, client)
	meshed(t, client)
	if _, err := client.Deploy(ctx, client.machine, run.DeployCall{
		HostCommand: command(node),
		Run:         run.RunSpec{Image: runImage, Resources: run.Resources{GPUs: 8, DiskBytes: 1 << 30}},
	}); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	waitFor(t, "the container to be created", func(s run.NodeStatus) bool { return s.State.Exists() }, client)

	reports, err := client.Report(ctx, client.machine, shardID, []vo.NodeRef{node})

	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if len(reports) != 1 || reports[0].Node != node {
		t.Fatalf("got %+v, want a report for the run's only node", reports)
	}
	if len(reports[0].Images) != 1 || reports[0].Images[0].Image != runImage {
		t.Fatalf("got %+v, want the image the node ran with its time", reports[0].Images)
	}
	if reports[0].Images[0].At.IsZero() {
		t.Fatalf("got %+v, want the time to survive the wire", reports[0].Images[0])
	}
}

func TestTheMeshIsBuiltAndProbedOverHTTP(t *testing.T) {

	client := newHost(t)
	ctx := context.Background()
	waitFor(t, "the node to be prepared", func(s run.NodeStatus) bool { return s.Prepared }, client)

	identities, err := client.Identities(ctx, shardID, client.machine)
	if err != nil {
		t.Fatalf("identities: %v", err)
	}
	config, err := mesh.Order(shardID, []mesh.Member{identities[0].Member})
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	err = client.Apply(ctx, config, client.machine, node)

	if len(identities) != 1 || identities[0].Member.Node != node || len(identities[0].Signature) == 0 {
		t.Fatalf("got %+v, want one signed member", identities)
	}
	if err != nil {
		t.Fatalf("apply mesh: %v", err)
	}
	waitFor(t, "the mesh to come up", func(s run.NodeStatus) bool { return s.MeshUp }, client)

	failed, err := client.Probe(ctx, config, client.machine, node)

	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if len(failed) != 0 {
		t.Fatalf("got %v, want a mesh of one node to have no broken links", failed)
	}
}

func TestLogsAndRefusalsCrossTheWireAsThemselves(t *testing.T) {

	client := newHost(t)
	ctx := context.Background()
	var out strings.Builder

	err := client.Logs(ctx, client.machine, run.LogRequest{Shard: shardID, Node: node, Tail: 10}, &out)

	if err != nil {
		t.Fatalf("logs: %v", err)
	}
	if !strings.Contains(out.String(), "no logs") {
		t.Fatalf("got %q, want what the machine had to say", out.String())
	}

	_, err = client.Start(ctx, client.machine, command(vo.NodeRef{Participant: host, NodeID: "node-x"}))

	if shared.CodeOf(err) != "NODE_NOT_SERVED" {
		t.Fatalf("got %v, want the host's own refusal of a node it does not serve", err)
	}
}
