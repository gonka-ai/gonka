package run_test

import (
	"reflect"
	"testing"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shared/vo"
)

var cleaning = run.Desired{Reservation: run.Reservation{Shard: 7}}

func TestCleanupPlanKeepsTheSameOrder(t *testing.T) {

	observed := run.Observed{
		Drained:           true,
		Container:         vo.ContainerRunning,
		MeshKey:           true,
		MeshUp:            true,
		VolumesPresent:    true,
		TrainingProcesses: true,
	}

	got := run.CleanupPlan(cleaning, observed)

	want := []run.Action{
		{Kind: run.ActionStopContainer},
		{Kind: run.ActionRemoveContainer},
		{Kind: run.ActionRemoveMesh},
		{Kind: run.ActionWipeVolumes},
		{Kind: run.ActionKillGPUProcesses},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestCleanupPlanLetsGoOfTheShardOnlyWhenNothingIsLeft(t *testing.T) {
	cases := []struct {
		name     string
		desired  run.Desired
		observed run.Observed
		want     []run.Action
	}{
		{
			name:     "cleaned and drained",
			desired:  cleaning,
			observed: run.Observed{Drained: true},
			want:     []run.Action{{Kind: run.ActionReturnNode}},
		},
		{
			name:     "volumes still there",
			desired:  cleaning,
			observed: run.Observed{Drained: true, VolumesPresent: true},
			want:     []run.Action{{Kind: run.ActionWipeVolumes}},
		},
		{
			name:     "processes still on the gpus",
			desired:  cleaning,
			observed: run.Observed{Drained: true, TrainingProcesses: true},
			want:     []run.Action{{Kind: run.ActionKillGPUProcesses}},
		},
		{
			name:     "never drained, so only the shard is let go of",
			desired:  cleaning,
			observed: run.Observed{},
			want:     []run.Action{{Kind: run.ActionForgetRun}},
		},
		{
			name:     "no shard to let go of",
			desired:  run.Desired{},
			observed: run.Observed{},
			want:     nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {

			got := run.CleanupPlan(tc.desired, tc.observed)

			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCleanupPlanStopsBeforeRemovingAStoppedContainer(t *testing.T) {

	observed := run.Observed{Drained: true, Container: vo.ContainerExited}

	got := run.CleanupPlan(cleaning, observed)

	want := []run.Action{{Kind: run.ActionRemoveContainer}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
