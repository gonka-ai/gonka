package run_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/vo"
)

func TestConvergeWipesAShardPastItsExpiryThatTheChainStillLists(t *testing.T) {
	reservedAt := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name       string
		unreadable error
		state      run.RunState
	}{
		{
			name:       "the dapi cannot say whether the node is drained",
			unreadable: errors.New("dapi down"),
			state:      run.RunState{Shard: 7, ReservedAt: reservedAt, Spec: runSpec(), Start: true},
		},
		{
			name:  "its run has been broken for longer than the host waits",
			state: run.RunState{Shard: 7, ReservedAt: reservedAt, Spec: runSpec(), Start: true, Fault: &shared.Fault{Code: "PULL_FAILED"}, FaultAt: reservedAt},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// arrange
			rec := &recorder{}
			chain := &reservationsStub{reservation: run.Reservation{Shard: 7, BaseImage: baseImage}, found: true}
			runs := &runStoreStub{states: map[vo.NodeRef]run.RunState{nodeA: tc.state}}
			machine := run.Machine{
				Images:     &imagesStub{rec: rec, present: map[vo.ImageDigest]bool{baseImage: true, runImage: true}},
				Containers: &containersStub{rec: rec, info: run.ContainerInfo{State: vo.ContainerRunning, Image: runImage}, shards: []vo.ShardID{7}},
				Volumes:    &volumesStub{rec: rec, shardID: 7, present: true},
				GPU:        &gpuStub{rec: rec},
				Mesh:       &networkStub{rec: rec, shardID: 7, key: true, up: true},
				Egress:     egressStub{},
				Control:    &controlStub{rec: rec, unreadable: tc.unreadable},
				Runs:       runs,
				Clock:      clockStub{now: reservedAt.Add(10 * time.Hour)},
				StopGrace:  time.Minute,
			}
			converger := run.NewConverger(chain, runs, machine, machine.Clock, time.Hour)

			// act
			_, err := converger.Converge(context.Background(), nodeA)

			// assert
			if err != nil {
				t.Fatalf("got %v, want the run wiped", err)
			}
			want := []string{"containers.stop", "containers.remove", "mesh.remove", "volumes.wipe"}
			if !reflect.DeepEqual(rec.calls, want) {
				t.Fatalf("got %v, want %v", rec.calls, want)
			}
			if len(chain.releases) != 0 {
				t.Fatalf("got releases %v, want none for a shard that is already over", chain.releases)
			}
		})
	}
}

func TestConvergeBuildsAgainAContainerWhoseImageWentUnrecorded(t *testing.T) {
	reservedAt := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	errStore := errors.New("state disk full")

	cases := []struct {
		name      string
		container run.ContainerInfo
		wantFirst []string
	}{
		{
			name:      "first container for the run",
			container: run.ContainerInfo{State: vo.ContainerAbsent},
			wantFirst: []string{"volumes.ensure", "containers.create", "containers.remove"},
		},
		{
			name:      "container replaced for a new revision",
			container: run.ContainerInfo{State: vo.ContainerCreated, Image: runImage, Revision: 1},
			wantFirst: []string{"volumes.ensure", "containers.remove", "containers.create", "containers.remove"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// arrange
			rec := &recorder{}
			chain := &reservationsStub{reservation: run.Reservation{Shard: 7, BaseImage: baseImage, Active: true}, found: true}
			runs := &runStoreStub{
				states:         map[vo.NodeRef]run.RunState{nodeA: {Shard: 7, ReservedAt: reservedAt, Spec: runSpec(), Revision: 2}},
				imageRecordErr: errStore,
			}
			containers := &containersStub{rec: rec, info: tc.container, shards: []vo.ShardID{7}}
			clock := clockStub{now: reservedAt.Add(time.Minute)}
			machine := run.Machine{
				Images:     &imagesStub{rec: rec, present: map[vo.ImageDigest]bool{baseImage: true, runImage: true}},
				Containers: containers,
				Volumes:    &volumesStub{rec: rec, shardID: 7, present: true},
				GPU:        &gpuStub{rec: rec},
				Mesh:       &networkStub{rec: rec, shardID: 7, key: true, up: true},
				Egress:     egressStub{},
				Control:    &controlStub{rec: rec},
				Runs:       runs,
				Clock:      clock,
				StopGrace:  time.Minute,
			}
			converger := run.NewConverger(chain, runs, machine, clock, time.Hour)

			// act
			_, firstErr := converger.Converge(context.Background(), nodeA)
			firstCalls, leftBehind := rec.calls, containers.info.State
			rec.calls, runs.imageRecordErr = nil, nil
			_, secondErr := converger.Converge(context.Background(), nodeA)

			// assert
			if !errors.Is(firstErr, errStore) {
				t.Fatalf("got %v, want the failed record reported", firstErr)
			}
			if !reflect.DeepEqual(firstCalls, tc.wantFirst) {
				t.Fatalf("first pass got %v, want %v", firstCalls, tc.wantFirst)
			}
			if leftBehind.Exists() {
				t.Fatalf("got a %s container left behind, want none without its image recorded", leftBehind)
			}
			if secondErr != nil {
				t.Fatalf("got %v, want the next pass to build the container", secondErr)
			}
			if want := []string{"volumes.ensure", "containers.create"}; !reflect.DeepEqual(rec.calls, want) {
				t.Fatalf("second pass got %v, want %v", rec.calls, want)
			}
			want := []run.ImageRun{{Image: runImage, At: clock.now}}
			if got := runs.states[nodeA].Images; !reflect.DeepEqual(got, want) {
				t.Fatalf("got image history %v, want %v", got, want)
			}
		})
	}
}
