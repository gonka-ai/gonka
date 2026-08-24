package run_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/vo"
)

var (
	nodeA     = vo.NodeRef{Participant: "gonka1host", NodeID: "node-a"}
	nodeB     = vo.NodeRef{Participant: "gonka1host", NodeID: "node-b"}
	baseImage = vo.ImageDigest("base@sha256:" + strings.Repeat("a", 64))
	runImage  = vo.ImageDigest("run@sha256:" + strings.Repeat("b", 64))

	baseLayers = vo.ImageLayers{"layer-1", "layer-2"}
	runLayers  = vo.ImageLayers{"layer-1", "layer-2", "layer-3"}

	limits = run.Limits{MaxGPUs: 8, MaxDiskBytes: 1 << 40, MaxSources: 2}
)

func runSpec() run.RunSpec {
	return run.RunSpec{
		Image:     runImage,
		Command:   []string{"train.py"},
		Env:       map[string]string{"HF_TOKEN": "secret"},
		Sources:   []vo.Source{{Host: "s3.amazonaws.com", Port: 443}},
		Resources: run.Resources{GPUs: 8, DiskBytes: 1 << 30},
	}
}

func TestCanDeploy(t *testing.T) {
	cases := []struct {
		name      string
		mutate    func(*run.RunSpec)
		container vo.ContainerState
		wantErr   error
	}{
		{
			name:      "stopped container and a digest to deploy",
			mutate:    func(*run.RunSpec) {},
			container: vo.ContainerCreated,
		},
		{
			name:      "no image digest",
			mutate:    func(s *run.RunSpec) { s.Image = "" },
			container: vo.ContainerCreated,
			wantErr:   run.ErrImageMissing,
		},
		{
			name:      "running container",
			mutate:    func(*run.RunSpec) {},
			container: vo.ContainerRunning,
			wantErr:   run.ErrContainerRunning,
		},
		{
			name:      "more gpus than the host allows",
			mutate:    func(s *run.RunSpec) { s.Resources.GPUs = 16 },
			container: vo.ContainerCreated,
			wantErr:   run.ErrGPUsExceeded,
		},
		{
			name:      "more disk than the host allows",
			mutate:    func(s *run.RunSpec) { s.Resources.DiskBytes = 1 << 50 },
			container: vo.ContainerCreated,
			wantErr:   run.ErrDiskExceeded,
		},
		{
			name: "more outside sources than the host allows",
			mutate: func(s *run.RunSpec) {
				s.Sources = []vo.Source{{Host: "a", Port: 443}, {Host: "b", Port: 443}, {Host: "c", Port: 443}}
			},
			container: vo.ContainerCreated,
			wantErr:   run.ErrSourcesExceeded,
		},
		{
			name:      "no outside sources at all",
			mutate:    func(s *run.RunSpec) { s.Sources = nil },
			container: vo.ContainerCreated,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {

			spec := runSpec()
			tc.mutate(&spec)

			err := run.CanDeploy(spec, limits, tc.container)

			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("got %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestVerifyImage(t *testing.T) {

	derived := run.VerifyImage(runLayers, baseLayers)
	unrelated := run.VerifyImage(vo.ImageLayers{"other"}, baseLayers)
	unknownBase := run.VerifyImage(runLayers, nil)

	if derived != nil {
		t.Fatalf("an image built on the base must pass, got %v", derived)
	}
	if !errors.Is(unrelated, run.ErrImageNotDerived) || !errors.Is(unknownBase, run.ErrImageNotDerived) {
		t.Fatalf("got %v and %v, want %v", unrelated, unknownBase, run.ErrImageNotDerived)
	}
}

func TestCanStartAndCanStopRequireAContainer(t *testing.T) {

	startAbsent := run.CanStart(vo.ContainerAbsent)
	startCreated := run.CanStart(vo.ContainerCreated)
	stopAbsent := run.CanStop(vo.ContainerAbsent)
	stopRunning := run.CanStop(vo.ContainerRunning)

	if !errors.Is(startAbsent, run.ErrContainerMissing) || !errors.Is(stopAbsent, run.ErrContainerMissing) {
		t.Fatalf("absent container must be refused: start=%v stop=%v", startAbsent, stopAbsent)
	}
	if startCreated != nil || stopRunning != nil {
		t.Fatalf("existing container must be allowed: start=%v stop=%v", startCreated, stopRunning)
	}
}

func TestSameImage(t *testing.T) {
	cases := []struct {
		name    string
		nodes   []run.NodeImage
		want    vo.ImageDigest
		wantErr error
	}{
		{
			name:  "every node holds the same image",
			nodes: []run.NodeImage{{Node: nodeA, Image: runImage}, {Node: nodeB, Image: runImage}},
			want:  runImage,
		},
		{
			name:    "one node is out of step",
			nodes:   []run.NodeImage{{Node: nodeA, Image: runImage}, {Node: nodeB, Image: baseImage}},
			wantErr: run.ErrImagesDiffer,
		},
		{
			name:    "a node holds no image at all",
			nodes:   []run.NodeImage{{Node: nodeA, Image: runImage}, {Node: nodeB}},
			wantErr: run.ErrImagesDiffer,
		},
		{
			name:    "no nodes",
			wantErr: run.ErrNoNodes,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {

			got, err := run.SameImage(tc.nodes)

			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("got error %v, want %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("got image %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAutokick(t *testing.T) {
	reserved := run.Desired{Reservation: run.Reservation{BaseImage: baseImage}, Reserved: true}
	ready := run.Observed{Drained: true, Images: []vo.ImageDigest{baseImage}, MeshKey: true}
	patience := time.Hour

	cases := []struct {
		name       string
		observed   run.Observed
		state      run.RunState
		waited     time.Duration
		wantReason vo.ReleaseReason
		wantKick   bool
	}{
		{
			name:     "still preparing, still inside the deadline",
			observed: run.Observed{},
			state:    run.RunState{},
			waited:   30 * time.Minute,
		},
		{
			name:       "never got ready before the deadline",
			observed:   run.Observed{},
			state:      run.RunState{},
			waited:     time.Hour,
			wantReason: vo.ReleaseFailedPrepare,
			wantKick:   true,
		},
		{
			name:     "ready and healthy",
			observed: ready,
			state:    run.RunState{},
			waited:   10 * time.Hour,
		},
		{
			name:     "broke a moment ago",
			observed: ready,
			state:    run.RunState{Fault: &shared.Fault{Code: "PULL_FAILED"}},
			waited:   time.Minute,
		},
		{
			name:       "has been broken longer than the host waits",
			observed:   ready,
			state:      run.RunState{Fault: &shared.Fault{Code: "PULL_FAILED"}},
			waited:     2 * time.Hour,
			wantReason: vo.ReleaseFailedRun,
			wantKick:   true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {

			reservedAt := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
			state := tc.state
			state.ReservedAt, state.FaultAt = reservedAt, reservedAt

			reason, kick := run.Autokick(reserved, tc.observed, state, reservedAt.Add(tc.waited), patience)

			if kick != tc.wantKick || (kick && reason != tc.wantReason) {
				t.Fatalf("got %q %v, want %q %v", reason, kick, tc.wantReason, tc.wantKick)
			}
		})
	}
}

func TestAutokickLeavesANodeTheChainNoLongerHoldsToCleanup(t *testing.T) {

	reservedAt := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	state := run.RunState{ReservedAt: reservedAt}

	_, kick := run.Autokick(run.Desired{}, run.Observed{}, state, reservedAt.Add(10*time.Hour), time.Hour)

	if kick {
		t.Fatal("an unreserved node has nothing left to release; it is on its way out already")
	}
}

func TestRunSpecKeepsEnvironmentValuesOutOfText(t *testing.T) {

	text := runSpec().String()

	if strings.Contains(text, "secret") {
		t.Fatalf("run spec text leaked an environment value: %s", text)
	}
}

func TestRunSpecWithEnvLetsThePlacementWin(t *testing.T) {
	// arrange
	spec := runSpec()
	spec.Env = map[string]string{"NODE_RANK": "9", "HF_TOKEN": "secret"}
	placement := run.PlacementEnv(vo.Placement{Rank: 2, Size: 4, Master: "10.7.0.1"})

	// act
	got := spec.WithEnv(placement)

	// assert
	if got.Env["NODE_RANK"] != "2" || got.Env["NNODES"] != "4" {
		t.Fatalf("got %v, want the rank the host gave rather than the one the run asked for", got.Env)
	}
	if got.Env["MASTER_ADDR"] != "10.7.0.1" || got.Env["MASTER_PORT"] != "29500" {
		t.Fatalf("got %v, want the rendezvous of rank 0", got.Env)
	}
	if got.Env["HF_TOKEN"] != "secret" {
		t.Fatalf("got %v, want the run's own values kept", got.Env)
	}
	if spec.Env["NODE_RANK"] != "9" {
		t.Fatalf("got %v, want the spec it was called on left alone", spec.Env)
	}
}
