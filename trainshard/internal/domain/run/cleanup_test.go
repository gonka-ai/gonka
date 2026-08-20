package run_test

import (
	"reflect"
	"testing"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shared/vo"
)

func TestCleanupPlanKeepsTheSameOrder(t *testing.T) {

	observed := run.Observed{
		Drained:           true,
		Container:         vo.ContainerRunning,
		MeshKey:           true,
		MeshUp:            true,
		VolumesPresent:    true,
		TrainingProcesses: true,
	}

	got := run.CleanupPlan(observed)

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

func TestCleanupPlanHandsTheNodeBackOnlyWhenNothingIsLeft(t *testing.T) {
	cases := []struct {
		name     string
		observed run.Observed
		want     []run.Action
	}{
		{
			name:     "cleaned and drained",
			observed: run.Observed{Drained: true},
			want:     []run.Action{{Kind: run.ActionReturnNode}},
		},
		{
			name:     "volumes still there",
			observed: run.Observed{Drained: true, VolumesPresent: true},
			want:     []run.Action{{Kind: run.ActionWipeVolumes}},
		},
		{
			name:     "processes still on the gpus",
			observed: run.Observed{Drained: true, TrainingProcesses: true},
			want:     []run.Action{{Kind: run.ActionKillGPUProcesses}},
		},
		{
			name:     "node was never drained so there is nothing to hand back",
			observed: run.Observed{},
			want:     []run.Action{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {

			got := run.CleanupPlan(tc.observed)

			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCleanupPlanStopsBeforeRemovingAStoppedContainer(t *testing.T) {

	observed := run.Observed{Drained: true, Container: vo.ContainerExited}

	got := run.CleanupPlan(observed)

	want := []run.Action{{Kind: run.ActionRemoveContainer}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
