package localstate_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shared/vo"
	"trainshard/internal/infrastructure/repositories/localstate"
)

func openRuns(t *testing.T, dir string) run.RunStore {
	t.Helper()

	store, err := localstate.New(dir)
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	return store.Runs()
}

func TestADeployedRunSurvivesARestartOfTheDaemon(t *testing.T) {
	// arrange
	dir, ctx := t.TempDir(), context.Background()
	spec := run.RunSpec{
		Image:     vo.ImageDigest("run@sha256:" + strings.Repeat("b", 64)),
		Command:   []string{"train.py"},
		Env:       map[string]string{"LEARNING_RATE": "0.2"},
		Sources:   []vo.Source{{Host: "s3.amazonaws.com", Port: 443}},
		Resources: run.Resources{GPUs: 8, DiskBytes: 1 << 40},
	}
	if err := run.RecordDeploy(ctx, openRuns(t, dir), node, 7, spec); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if err := run.RecordStop(ctx, openRuns(t, dir), node, time.Minute, true); err != nil {
		t.Fatalf("stop: %v", err)
	}

	// act
	state, found, err := openRuns(t, dir).Load(ctx, node)

	// assert
	if err != nil || !found {
		t.Fatalf("got found=%v err=%v, want the run to outlive the process", found, err)
	}
	if state.Shard != 7 || state.Spec.Image != spec.Image || state.Spec.Env["LEARNING_RATE"] != "0.2" {
		t.Fatalf("got %+v, want the run as it was deployed", state)
	}
	if state.Revision != 1 {
		t.Fatalf("got revision %d, want the deploy the container was built for kept, or a restart rebuilds it", state.Revision)
	}
	if state.Start || !state.StopGraceGiven || state.StopGrace != time.Minute {
		t.Fatalf("got %+v, want the run left stopped with its grace", state)
	}
}

func TestAnExplicitZeroStopGraceSurvivesARestart(t *testing.T) {
	// arrange
	dir, ctx := t.TempDir(), context.Background()
	if err := run.RecordDeploy(ctx, openRuns(t, dir), node, 7, run.RunSpec{Image: vo.ImageDigest("run@sha256:" + strings.Repeat("b", 64))}); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if err := run.RecordStop(ctx, openRuns(t, dir), node, 0, true); err != nil {
		t.Fatalf("stop: %v", err)
	}

	// act
	state, found, err := openRuns(t, dir).Load(ctx, node)

	// assert
	if err != nil || !found {
		t.Fatalf("got found=%v err=%v, want the run to outlive the process", found, err)
	}
	if !state.StopGraceGiven || state.StopGrace != 0 {
		t.Fatalf("got %+v, want explicit zero grace preserved", state)
	}
}

func TestAnOldStateFileWithoutStopGraceLoadsAsGraceNotGiven(t *testing.T) {
	// arrange
	dir, ctx := t.TempDir(), context.Background()
	raw := []byte(`{"shard_id":7,"start":false}`)
	path := filepath.Join(dir, "gonka1host_node-1.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	// act
	state, found, err := openRuns(t, dir).Load(ctx, node)

	// assert
	if err != nil || !found {
		t.Fatalf("got found=%v err=%v, want the file loaded", found, err)
	}
	if state.StopGraceGiven || state.StopGrace != 0 {
		t.Fatalf("got %+v, want grace left not given", state)
	}
}

func TestTheClocksAHostHandsANodeBackByOutliveARestart(t *testing.T) {
	// arrange
	dir, ctx := t.TempDir(), context.Background()
	reservedAt := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	slipped := reservedAt.Add(time.Hour)

	if err := run.RecordReservation(ctx, openRuns(t, dir), node, 7, reservedAt); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	state, _, err := openRuns(t, dir).Load(ctx, node)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := run.TrackPreparedness(ctx, openRuns(t, dir), node, &state, run.Desired{Reserved: true}, run.Observed{}, slipped); err != nil {
		t.Fatalf("track: %v", err)
	}

	// act
	reopened, _, err := openRuns(t, dir).Load(ctx, node)

	// assert
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !reopened.ReservedAt.Equal(reservedAt) || !reopened.UnpreparedAt.Equal(slipped) {
		t.Fatalf("got reserved %v unready %v, want %v and %v", reopened.ReservedAt, reopened.UnpreparedAt, reservedAt, slipped)
	}
}

func TestADeployForAnotherShardStartsFromNothing(t *testing.T) {
	// arrange
	dir, ctx := t.TempDir(), context.Background()
	reservedAt := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	spec := run.RunSpec{Image: vo.ImageDigest("run@sha256:" + strings.Repeat("b", 64))}
	if err := run.RecordReservation(ctx, openRuns(t, dir), node, 7, reservedAt); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	state, _, err := openRuns(t, dir).Load(ctx, node)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := run.TrackPreparedness(ctx, openRuns(t, dir), node, &state, run.Desired{Reserved: true}, run.Observed{}, reservedAt.Add(time.Hour)); err != nil {
		t.Fatalf("track: %v", err)
	}

	// act
	if err := run.RecordDeploy(ctx, openRuns(t, dir), node, 8, spec); err != nil {
		t.Fatalf("deploy: %v", err)
	}

	// assert
	reopened, _, err := openRuns(t, dir).Load(ctx, node)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if reopened.Shard != 8 || !reopened.ReservedAt.IsZero() || !reopened.UnpreparedAt.IsZero() {
		t.Fatalf("got %+v, want the clocks of shard 7 gone with it", reopened)
	}
}
