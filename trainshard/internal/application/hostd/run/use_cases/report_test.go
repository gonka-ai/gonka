package usecases_test

import (
	"context"
	"errors"
	"testing"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shard"
)

func TestReportTellsEveryImageTheNodeRan(t *testing.T) {

	f := newFixture()
	ctx := context.Background()
	if err := f.meshed(ctx); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if _, err := f.deploy().Execute(ctx, deployCommand()); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	for range 2 {
		if _, err := f.reconcile().Execute(ctx, nodeA); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
	}

	reports, err := f.report().Execute(ctx, nodesCommand())

	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("got %d entries, want one per node", len(reports))
	}
	if len(reports[0].Images) != 1 || reports[0].Images[0].Image != runImage {
		t.Fatalf("got %+v, want the image the node was given recorded with its time", reports[0].Images)
	}
	if reports[0].Images[0].At.IsZero() {
		t.Fatalf("got no time on %+v, want when the image was run", reports[0].Images[0])
	}
}

func TestReportStillTellsTheImagesWhileTheGPUsAndTheDapiCannotBeRead(t *testing.T) {

	f := newFixture()
	ctx := context.Background()
	if err := f.meshed(ctx); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	f.runs.states[nodeA] = run.RunState{Shard: shardID, Spec: runSpec(), Images: []run.ImageRun{{Image: runImage, At: now}}}
	f.gpu.err = errors.New("nvidia-smi: driver not loaded")
	f.control.unreadable = errors.New("dapi is restarting")

	reports, err := f.report().Execute(ctx, nodesCommand())

	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if len(reports) != 1 || reports[0].Fault != nil || len(reports[0].Images) != 1 {
		t.Fatalf("got %+v, want the recorded image history despite probes that fail", reports)
	}
}

func TestReportHidesWhatTheShardBeforeLeftOnTheNode(t *testing.T) {

	f := newFixture()
	f.runs.states[nodeA] = run.RunState{Shard: shardID - 1, Images: []run.ImageRun{{Image: runImage, At: now}}, Fault: &oldFault}

	reports, err := f.report().Execute(context.Background(), nodesCommand())

	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if len(reports) != 1 || len(reports[0].Images) != 0 || reports[0].Fault != nil {
		t.Fatalf("got %+v, want nothing of the previous shard's run", reports)
	}
}

func TestReportKeepsARefusalInTheNodeEntry(t *testing.T) {

	f := newFixture()
	cmd := nodesCommand()
	cmd.Actor = shard.Actor{Address: stranger}

	reports, err := f.report().Execute(context.Background(), cmd)

	if err != nil {
		t.Fatalf("a per-node refusal must not fail the request: %v", err)
	}
	if len(reports) != 1 || reports[0].Fault == nil || reports[0].Fault.Code != "NOT_AUTHORIZED" {
		t.Fatalf("got %+v, want a single NOT_AUTHORIZED failure", reports)
	}
}
