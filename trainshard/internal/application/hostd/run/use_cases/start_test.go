package usecases_test

import (
	"context"
	"testing"
	"time"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shared/vo"
)

func TestStartRefusesANodeWithoutAContainer(t *testing.T) {

	f := newFixture()

	results, err := f.start().Execute(context.Background(), nodesCommand())

	if err != nil {
		t.Fatalf("a missing container is a per-node refusal: %v", err)
	}
	if len(results) != 1 || results[0].Fault == nil || results[0].Fault.Code != "CONTAINER_MISSING" {
		t.Fatalf("got %+v, want a single CONTAINER_MISSING failure", results)
	}
}

func TestStartAnswersWithTheContainerItActuallyStarted(t *testing.T) {

	f := newFixture()
	ctx := context.Background()
	if err := f.prepared(ctx); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	f.containers.infos[nodeA] = run.ContainerInfo{State: vo.ContainerCreated, Image: runImage}
	f.runs.states[nodeA] = run.RunState{Shard: shardID, Spec: runSpec()}
	f.images.present[runImage] = true

	results, err := f.start().Execute(ctx, nodesCommand())

	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if len(results) != 1 || results[0].State != vo.ContainerRunning {
		t.Fatalf("got %+v, want the node reported as running", results)
	}
	if !f.runs.states[nodeA].Start {
		t.Fatal("the run must be recorded as wanted running")
	}
}

func TestStartRefusesANodeWhoseContainerHasAlreadyRun(t *testing.T) {

	f := newFixture()
	ctx := context.Background()
	if err := f.prepared(ctx); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	f.containers.infos[nodeA] = run.ContainerInfo{State: vo.ContainerExited, Image: runImage}
	f.runs.states[nodeA] = run.RunState{Shard: shardID, Spec: runSpec()}
	f.images.present[runImage] = true

	results, err := f.start().Execute(ctx, nodesCommand())

	if err != nil {
		t.Fatalf("a container that has run is a per-node refusal: %v", err)
	}
	if len(results) != 1 || results[0].Fault == nil || results[0].Fault.Code != "CONTAINER_FINISHED" {
		t.Fatalf("got %+v, want start to admit it cannot restart what already ran", results)
	}
	if f.runs.states[nodeA].Start {
		t.Fatal("a refused start must not leave the run recorded as wanted running")
	}
}

func TestStopAnswersWithTheContainerItActuallyStopped(t *testing.T) {

	f := newFixture()
	ctx := context.Background()
	if err := f.prepared(ctx); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	f.containers.infos[nodeA] = run.ContainerInfo{State: vo.ContainerRunning, Image: runImage}
	f.runs.states[nodeA] = run.RunState{Shard: shardID, Spec: runSpec(), Start: true}
	f.images.present[runImage] = true
	cmd := stopCommand()
	cmd.Grace = 5 * time.Second

	results, err := f.stop().Execute(ctx, cmd)

	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if len(results) != 1 || results[0].State != vo.ContainerExited {
		t.Fatalf("got %+v, want the node reported as exited", results)
	}
	if f.containers.grace != 5*time.Second {
		t.Fatalf("the caller's grace must reach the container, got %v", f.containers.grace)
	}
	state := f.runs.states[nodeA]
	if state.Start {
		t.Fatal("the run must be recorded as wanted stopped")
	}
	if state.Spec.Image != runImage {
		t.Fatal("stopping must keep the deployed image so the node still holds the run it was given")
	}
}

func TestStopClampsAGraceLongerThanTheDaemonAllows(t *testing.T) {

	f := newFixture()
	ctx := context.Background()
	if err := f.prepared(ctx); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	f.containers.infos[nodeA] = run.ContainerInfo{State: vo.ContainerRunning, Image: runImage}
	f.runs.states[nodeA] = run.RunState{Shard: shardID, Spec: runSpec(), Start: true}
	f.images.present[runImage] = true
	cmd := stopCommand()
	cmd.Grace = time.Hour

	if _, err := f.stop().Execute(ctx, cmd); err != nil {
		t.Fatalf("stop: %v", err)
	}

	if f.containers.grace != time.Minute {
		t.Fatalf("got %v, want the daemon's own limit", f.containers.grace)
	}
}
