package usecases_test

import (
	"context"
	"strings"
	"testing"
	"time"

	usecases "trainshard/internal/application/hostd/node/use_cases"
	"trainshard/internal/domain/shared/vo"
)

const ttl = 10 * time.Minute

type fixture struct {
	probe     *probeStub
	gpu       *gpuStub
	chain     *chainStub
	submitter *submitterStub
	supported string
}

func newFixture() *fixture {
	return &fixture{
		probe:     newProbeStub(),
		gpu:       &gpuStub{inventory: hardware},
		chain:     &chainStub{hardware: hardware},
		submitter: &submitterStub{},
	}
}

func (f *fixture) readiness() *usecases.EvaluateReadinessUseCase {
	return usecases.NewEvaluateReadinessUseCase(f.probe, f.gpu, f.chain, version, f.supported, diskFloor)
}

func (f *fixture) refresh() *usecases.RefreshOptInUseCase {
	return usecases.NewRefreshOptInUseCase(f.readiness(), f.submitter, ttl)
}

func TestReadinessKeepsANodeOutForEveryReasonItCanName(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*fixture)
		want   string
	}{
		{
			name:   "runtime cannot give a container the gpus",
			mutate: func(f *fixture) { f.probe.gpuContainer = errProbe },
			want:   "no nvidia runtime",
		},
		{
			name:   "machine has fewer gpus than the chain was told",
			mutate: func(f *fixture) { f.gpu.inventory = vo.GPUInventory{Model: "H100", Count: 4} },
			want:   "machine has 4 x H100, chain says 8 x H100",
		},
		{
			name:   "chain cannot be read",
			mutate: func(f *fixture) { f.chain.err = errProbe },
			want:   "no nvidia runtime",
		},
		{
			name:   "not enough free disk",
			mutate: func(f *fixture) { f.probe.freeDisk = diskFloor / 2 },
			want:   "needed",
		},
		{
			name:   "mesh port does not answer from outside",
			mutate: func(f *fixture) { f.probe.meshPort = errProbe },
			want:   "no nvidia runtime",
		},
		{
			name:   "build the operator does not support",
			mutate: func(f *fixture) { f.supported = "v0.2.0" },
			want:   "running v0.1.0, supported v0.2.0",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {

			f := newFixture()
			tc.mutate(f)

			result := f.readiness().Execute(context.Background(), nodeA)

			if result.Ready {
				t.Fatal("the node must be kept out of the pool")
			}
			if !strings.Contains(result.Reason(), tc.want) {
				t.Fatalf("got reason %q, want it to mention %q", result.Reason(), tc.want)
			}
		})
	}
}

func TestReadinessLetsAHealthyNodeIn(t *testing.T) {

	f := newFixture()

	result := f.readiness().Execute(context.Background(), nodeA)

	if !result.Ready {
		t.Fatalf("got %q, want a ready node", result.Reason())
	}
}

func TestRefreshOptInCarriesTheTTLWhileTheNodeStaysReady(t *testing.T) {

	f := newFixture()
	ctx := context.Background()

	first, firstErr := f.refresh().Execute(ctx, nodeA)
	second, secondErr := f.refresh().Execute(ctx, nodeA)

	if firstErr != nil || secondErr != nil {
		t.Fatalf("refresh must not fail: %v then %v", firstErr, secondErr)
	}
	if !first.Ready || !second.Ready {
		t.Fatal("a healthy node must stay in the pool")
	}
	if len(f.submitter.ttls) != 2 || f.submitter.ttls[0] != ttl {
		t.Fatalf("got %v, want the ttl submitted on every refresh", f.submitter.ttls)
	}
}

func TestRefreshOptInLetsAnUnhealthyNodeLapse(t *testing.T) {

	f := newFixture()
	f.probe.gpuContainer = errProbe

	result, err := f.refresh().Execute(context.Background(), nodeA)

	if err != nil {
		t.Fatalf("a failed check is not an error: %v", err)
	}
	if result.Ready {
		t.Fatal("the node must not be reported ready")
	}
	if len(f.submitter.ttls) != 0 {
		t.Fatal("nothing may be submitted for a node that failed a check")
	}
}
