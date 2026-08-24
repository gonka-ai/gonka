package readiness_test

import (
	"context"
	"strings"
	"testing"

	"trainshard/internal/domain/readiness"
	"trainshard/internal/domain/shared/vo"
)

type machine struct {
	probe *probeStub
	cards *cardsStub
	claim *claimStub
	clock *clockStub
	spec  readiness.Spec
}

func newMachine() *machine {
	return &machine{
		probe: newProbeStub(),
		cards: &cardsStub{inventory: hardware},
		claim: &claimStub{hardware: hardware},
		clock: newClockStub(),
		spec:  readiness.Spec{Version: version, MinFreeDiskBytes: diskFloor},
	}
}

func (m *machine) collect() readiness.Result {
	prover := readiness.NewProver(m.probe, m.clock)
	return readiness.Collect(context.Background(), prover, m.cards, m.claim, nodeA, m.spec)
}

func TestCollectKeepsANodeOutForEveryReasonItCanName(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*machine)
		want   string
	}{
		{
			name:   "runtime cannot give a container the gpus",
			mutate: func(m *machine) { m.probe.gpuContainer = errProbe },
			want:   "no nvidia runtime",
		},
		{
			name:   "machine has fewer gpus than the chain was told",
			mutate: func(m *machine) { m.cards.inventory = vo.GPUInventory{Model: "H100", Count: 4} },
			want:   "machine has 4 x H100, chain says 8 x H100",
		},
		{
			name:   "chain cannot be read",
			mutate: func(m *machine) { m.claim.err = errProbe },
			want:   "no nvidia runtime",
		},
		{
			name:   "not enough free disk",
			mutate: func(m *machine) { m.probe.freeDisk = diskFloor / 2 },
			want:   "needed",
		},
		{
			name:   "mesh port does not answer from outside",
			mutate: func(m *machine) { m.probe.meshPort = errProbe },
			want:   "no nvidia runtime",
		},
		{
			name:   "build the operator does not support",
			mutate: func(m *machine) { m.spec.SupportedVersion = "v0.2.0" },
			want:   "running v0.1.0, supported v0.2.0",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// arrange
			m := newMachine()
			tc.mutate(m)

			// act
			result := m.collect()

			// assert
			if result.Ready {
				t.Fatal("the node must be kept out of the pool")
			}
			if !strings.Contains(result.Reason(), tc.want) {
				t.Fatalf("got reason %q, want it to mention %q", result.Reason(), tc.want)
			}
		})
	}
}

func TestCollectLetsAHealthyNodeIn(t *testing.T) {
	// arrange
	m := newMachine()

	// act
	result := m.collect()

	// assert
	if !result.Ready {
		t.Fatalf("got %q, want a ready node", result.Reason())
	}
}
