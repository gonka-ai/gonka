package localstate_test

import (
	"context"
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
	if err := run.RecordStop(ctx, openRuns(t, dir), node, time.Minute); err != nil {
		t.Fatalf("stop: %v", err)
	}

	state, found, err := openRuns(t, dir).Load(ctx, node)

	if err != nil || !found {
		t.Fatalf("got found=%v err=%v, want the run to outlive the process", found, err)
	}
	if state.Shard != 7 || state.Spec.Image != spec.Image || state.Spec.Env["LEARNING_RATE"] != "0.2" {
		t.Fatalf("got %+v, want the run as it was deployed", state)
	}
	if state.Revision != 1 {
		t.Fatalf("got revision %d, want the deploy the container was built for kept, or a restart rebuilds it", state.Revision)
	}
	if state.Start || state.StopGrace != time.Minute {
		t.Fatalf("got %+v, want the run left stopped with its grace", state)
	}
}
