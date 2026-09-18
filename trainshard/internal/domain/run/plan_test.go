package run_test

import (
	"reflect"
	"testing"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shared/vo"
)

func preparedObserved() run.Observed {
	return run.Observed{
		Drained:        true,
		Images:         []vo.ImageDigest{baseImage},
		Container:      vo.ContainerAbsent,
		MeshKey:        true,
		MeshIdentity:   true,
		MeshUp:         true,
		Fenced:         true,
		VolumesPresent: true,
	}
}

func reservedDesired() run.Desired {
	return run.Desired{
		Reservation:    run.Reservation{Shard: 7, BaseImage: baseImage, Active: true},
		Reserved:       true,
		MeshConfigured: true,
	}
}

func TestPlan(t *testing.T) {
	cases := []struct {
		name    string
		desired func() run.Desired
		observe func(*run.Observed)
		want    []run.Action
	}{
		{
			name: "reservation is gone so the run is wiped",
			desired: func() run.Desired {
				d := reservedDesired()
				d.Reserved = false
				return d
			},
			observe: func(o *run.Observed) { o.Container = vo.ContainerRunning },
			want: []run.Action{
				{Kind: run.ActionStopContainer},
				{Kind: run.ActionRemoveContainer},
				{Kind: run.ActionRemoveMesh},
				{Kind: run.ActionWipeVolumes},
			},
		},
		{
			name: "closed shard is wiped even while the container runs",
			desired: func() run.Desired {
				d := reservedDesired()
				d.Active = false
				return d
			},
			observe: func(o *run.Observed) { o.Container = vo.ContainerRunning },
			want: []run.Action{
				{Kind: run.ActionStopContainer},
				{Kind: run.ActionRemoveContainer},
				{Kind: run.ActionRemoveMesh},
				{Kind: run.ActionWipeVolumes},
			},
		},
		{
			name:    "node still serving inference is drained first",
			desired: reservedDesired,
			observe: func(o *run.Observed) { o.Drained = false },
			want:    []run.Action{{Kind: run.ActionDrainNode}},
		},
		{
			name:    "drain is asked for before the base image is pulled, and both go on at once",
			desired: reservedDesired,
			observe: func(o *run.Observed) { o.Drained, o.Images = false, nil },
			want: []run.Action{
				{Kind: run.ActionDrainNode},
				{Kind: run.ActionPullImage, Image: baseImage},
			},
		},
		{
			name:    "drained node whose gpus still carry other work keeps draining",
			desired: reservedDesired,
			observe: func(o *run.Observed) { o.ForeignGPUWork = true },
			want:    []run.Action{{Kind: run.ActionDrainNode}},
		},
		{
			name:    "base image named by the proposal is pulled",
			desired: reservedDesired,
			observe: func(o *run.Observed) { o.Images = nil },
			want:    []run.Action{{Kind: run.ActionPullImage, Image: baseImage}},
		},
		{
			name: "mesh identity is created once the image is cached",
			desired: func() run.Desired {
				d := reservedDesired()
				d.MeshConfigured = false
				return d
			},
			observe: func(o *run.Observed) { o.MeshKey, o.MeshIdentity, o.MeshUp = false, false, false },
			want:    []run.Action{{Kind: run.ActionCreateMeshIdentity}},
		},
		{
			name: "a key whose signed member was never stored is finished, not left as done",
			desired: func() run.Desired {
				d := reservedDesired()
				d.MeshConfigured = false
				return d
			},
			observe: func(o *run.Observed) { o.MeshIdentity, o.MeshUp = false, false },
			want:    []run.Action{{Kind: run.ActionCreateMeshIdentity}},
		},
		{
			name:    "prepared node with nothing deployed waits",
			desired: reservedDesired,
			observe: func(*run.Observed) {},
			want:    []run.Action{},
		},
		{
			name:    "peer list is applied again after the interface went down",
			desired: reservedDesired,
			observe: func(o *run.Observed) { o.MeshUp = false },
			want:    []run.Action{{Kind: run.ActionApplyMeshConfig}},
		},
		{
			name:    "peer list already up is left alone",
			desired: reservedDesired,
			observe: func(o *run.Observed) { o.MeshUp = true },
			want:    []run.Action{},
		},
		{
			name: "run image is pulled but no container is built before the peer list lands",
			desired: func() run.Desired {
				d := reservedDesired()
				d.Run, d.Start, d.MeshConfigured = runSpec(), true, false
				return d
			},
			observe: func(o *run.Observed) { o.MeshUp = false },
			want:    []run.Action{{Kind: run.ActionPullImage, Image: runImage}},
		},
		{
			name: "run image is pulled before the container is created",
			desired: func() run.Desired {
				d := reservedDesired()
				d.Run = runSpec()
				return d
			},
			observe: func(*run.Observed) {},
			want: []run.Action{
				{Kind: run.ActionPullImage, Image: runImage},
				{Kind: run.ActionCreateContainer, Image: runImage},
			},
		},
		{
			name: "a prepared node pulls, creates and starts in one pass",
			desired: func() run.Desired {
				d := reservedDesired()
				d.Run, d.Start = runSpec(), true
				return d
			},
			observe: func(*run.Observed) {},
			want: []run.Action{
				{Kind: run.ActionPullImage, Image: runImage},
				{Kind: run.ActionCreateContainer, Image: runImage},
				{Kind: run.ActionStartContainer},
			},
		},
		{
			name: "container is created stopped when there is none",
			desired: func() run.Desired {
				d := reservedDesired()
				d.Run = runSpec()
				return d
			},
			observe: func(o *run.Observed) { o.Images = append(o.Images, runImage) },
			want:    []run.Action{{Kind: run.ActionCreateContainer, Image: runImage}},
		},
		{
			name: "stopped container holding another image is replaced",
			desired: func() run.Desired {
				d := reservedDesired()
				d.Run = runSpec()
				return d
			},
			observe: func(o *run.Observed) {
				o.Images = append(o.Images, runImage)
				o.Container = vo.ContainerCreated
				o.ContainerImage = baseImage
			},
			want: []run.Action{{Kind: run.ActionReplaceContainer, Image: runImage}},
		},
		{
			name: "running container is never replaced under the run",
			desired: func() run.Desired {
				d := reservedDesired()
				d.Run, d.Start = runSpec(), true
				return d
			},
			observe: func(o *run.Observed) {
				o.Images = append(o.Images, runImage)
				o.Container = vo.ContainerRunning
				o.ContainerImage = baseImage
			},
			want: []run.Action{},
		},
		{
			name: "stop reaches a running container that holds another image",
			desired: func() run.Desired {
				d := reservedDesired()
				d.Run = runSpec()
				return d
			},
			observe: func(o *run.Observed) {
				o.Images = append(o.Images, runImage)
				o.Container = vo.ContainerRunning
				o.ContainerImage = baseImage
			},
			want: []run.Action{{Kind: run.ActionStopContainer}},
		},
		{
			name: "start is applied to a stopped container",
			desired: func() run.Desired {
				d := reservedDesired()
				d.Run, d.Start = runSpec(), true
				return d
			},
			observe: func(o *run.Observed) {
				o.Images = append(o.Images, runImage)
				o.Container = vo.ContainerCreated
				o.ContainerImage = runImage
			},
			want: []run.Action{{Kind: run.ActionStartContainer}},
		},
		{
			name: "a container waiting to start in a box rebuilt under it is built again first",
			desired: func() run.Desired {
				d := reservedDesired()
				d.Run, d.Start = runSpec(), true
				return d
			},
			observe: func(o *run.Observed) {
				o.Images = append(o.Images, runImage)
				o.Container = vo.ContainerCreated
				o.ContainerImage = runImage
				o.Fenced = false
			},
			want: []run.Action{{Kind: run.ActionReplaceContainer, Image: runImage}, {Kind: run.ActionStartContainer}},
		},
		{
			name: "a running container is left alone when the box under it was rebuilt",
			desired: func() run.Desired {
				d := reservedDesired()
				d.Run, d.Start = runSpec(), true
				return d
			},
			observe: func(o *run.Observed) {
				o.Images = append(o.Images, runImage)
				o.Container = vo.ContainerRunning
				o.ContainerImage = runImage
				o.Fenced = false
			},
			want: []run.Action{},
		},
		{
			name: "exited container is reported and not restarted",
			desired: func() run.Desired {
				d := reservedDesired()
				d.Run, d.Start = runSpec(), true
				return d
			},
			observe: func(o *run.Observed) {
				o.Images = append(o.Images, runImage)
				o.Container = vo.ContainerExited
				o.ContainerImage = runImage
			},
			want: []run.Action{},
		},
		{
			name: "exited container whose place on the mesh moved is not rebuilt and rerun",
			desired: func() run.Desired {
				d := reservedDesired()
				d.Run, d.Start, d.Revision = runSpec(), true, 2
				return d
			},
			observe: func(o *run.Observed) {
				o.Images = append(o.Images, runImage)
				o.Container = vo.ContainerExited
				o.ContainerImage = runImage
				o.ContainerRevision = 1
			},
			want: []run.Action{},
		},
		{
			name: "exited container is rebuilt once stop cleared the start",
			desired: func() run.Desired {
				d := reservedDesired()
				d.Run, d.Revision = runSpec(), 2
				return d
			},
			observe: func(o *run.Observed) {
				o.Images = append(o.Images, runImage)
				o.Container = vo.ContainerExited
				o.ContainerImage = runImage
				o.ContainerRevision = 1
			},
			want: []run.Action{{Kind: run.ActionReplaceContainer, Image: runImage}},
		},
		{
			name: "stop is applied to a running container",
			desired: func() run.Desired {
				d := reservedDesired()
				d.Run = runSpec()
				return d
			},
			observe: func(o *run.Observed) {
				o.Images = append(o.Images, runImage)
				o.Container = vo.ContainerRunning
				o.ContainerImage = runImage
			},
			want: []run.Action{{Kind: run.ActionStopContainer}},
		},
		{
			name: "running as asked leaves nothing to do",
			desired: func() run.Desired {
				d := reservedDesired()
				d.Run, d.Start = runSpec(), true
				return d
			},
			observe: func(o *run.Observed) {
				o.Images = append(o.Images, runImage)
				o.Container = vo.ContainerRunning
				o.ContainerImage = runImage
			},
			want: []run.Action{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {

			observed := preparedObserved()
			tc.observe(&observed)

			got := run.Plan(tc.desired(), observed)

			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPlanIsIdempotent(t *testing.T) {

	desired := reservedDesired()
	desired.Run, desired.Start = runSpec(), true
	observed := preparedObserved()
	observed.Images = append(observed.Images, runImage)
	observed.Container = vo.ContainerRunning
	observed.ContainerImage = runImage

	first := run.Plan(desired, observed)
	second := run.Plan(desired, observed)

	if len(first) != 0 || len(second) != 0 {
		t.Fatalf("a settled run must plan nothing twice: %v then %v", first, second)
	}
}

func TestPrepared(t *testing.T) {
	cases := []struct {
		name    string
		observe func(*run.Observed)
		want    bool
	}{
		{name: "drained, cached and keyed", observe: func(*run.Observed) {}, want: true},
		{name: "still draining", observe: func(o *run.Observed) { o.Drained = false }},
		{name: "gpus still carry other work", observe: func(o *run.Observed) { o.ForeignGPUWork = true }},
		{name: "base image missing", observe: func(o *run.Observed) { o.Images = nil }},
		{name: "no mesh key", observe: func(o *run.Observed) { o.MeshKey = false }},
		{name: "key made but member not stored", observe: func(o *run.Observed) { o.MeshIdentity = false }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {

			observed := preparedObserved()
			tc.observe(&observed)

			got := run.Prepared(reservedDesired(), observed)

			if got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestUnprepared(t *testing.T) {
	cases := []struct {
		name string
		d    run.Desired
		o    run.Observed
		want string
	}{
		{"prepared says nothing", reservedDesired(), preparedObserved(), ""},
		{"not reserved", run.Desired{}, preparedObserved(), "not reserved"},
		{"waiting on the dapi", reservedDesired(), func() run.Observed { o := preparedObserved(); o.Drained = false; return o }(), "node not drained from inference"},
		{"cards still busy", reservedDesired(), func() run.Observed { o := preparedObserved(); o.ForeignGPUWork = true; return o }(), "foreign work on the gpus"},
		{"everything at once", reservedDesired(), run.Observed{ForeignGPUWork: true},
			"node not drained from inference, foreign work on the gpus, base image not pulled, no mesh identity"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// act
			got := run.Unprepared(tc.d, tc.o)

			// assert
			if got != tc.want {
				t.Fatalf("Unprepared = %q, want %q", got, tc.want)
			}
		})
	}
}
