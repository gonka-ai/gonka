package usecases_test

import (
	"context"
	"testing"
	"time"

	usecases "trainshard/internal/application/hostd/node/use_cases"
	"trainshard/internal/domain/readiness"
)

const ttl = 10 * time.Minute

type fixture struct {
	probe     *probeStub
	cards     *cardsStub
	claim     *claimStub
	submitter *submitterStub
}

func newFixture() *fixture {
	return &fixture{
		probe:     newProbeStub(),
		cards:     &cardsStub{inventory: hardware},
		claim:     &claimStub{hardware: hardware},
		submitter: &submitterStub{},
	}
}

func (f *fixture) refresh() *usecases.RefreshOptInUseCase {
	return usecases.NewRefreshOptInUseCase(f.probe, f.cards, f.claim, f.submitter,
		readiness.Spec{Version: version, MinFreeDiskBytes: diskFloor}, ttl)
}

func TestRefreshOptInCarriesTheTTLWhileTheNodeStaysReady(t *testing.T) {
	// arrange
	f := newFixture()
	refresh := f.refresh()
	ctx := context.Background()

	// act
	first, firstErr := refresh.Execute(ctx, nodeA)
	second, secondErr := refresh.Execute(ctx, nodeA)

	// assert
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
	// arrange
	f := newFixture()
	f.probe.gpuContainer = errProbe

	// act
	result, err := f.refresh().Execute(context.Background(), nodeA)

	// assert
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
