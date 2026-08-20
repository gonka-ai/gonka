package usecases_test

import (
	"context"
	"errors"
	"testing"
	"time"

	usecases "trainshard/internal/application/coord/assembly/use_cases"
	"trainshard/internal/domain/mesh"
	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/vo"
	"trainshard/internal/utils/timex"
)

var (
	now     = time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	expired = now.Add(-time.Minute)
	forever = now.Add(time.Hour)
)

func prepare(chain *chainStub, hosts *hostsStub, verifier *verifierStub) *usecases.PrepareMeshUseCase {
	return prepareWithin(chain, hosts, verifier, time.Minute)
}

func prepareWithin(chain *chainStub, hosts *hostsStub, verifier *verifierStub, settle time.Duration) *usecases.PrepareMeshUseCase {
	return usecases.NewPrepareMeshUseCase(chain, hosts, verifier, chain, timex.NewFrozen(now), time.Millisecond, settle)
}

func TestPrepareHandsEveryNodeItsPeerList(t *testing.T) {

	chain, hosts := newChainStub(), newHostsStub()

	result, err := prepare(chain, hosts, &verifierStub{}).Execute(context.Background(), shardID, forever)

	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if len(result.Config.Peers) != 3 || len(result.Released) != 0 {
		t.Fatalf("got %+v, want a mesh of all three nodes and nothing released", result)
	}
	if len(hosts.applied) != 3 {
		t.Fatalf("got %v, want the peer list applied on every node", hosts.applied)
	}
	if master, found := result.Config.Master(); !found || master.Rank != 0 {
		t.Fatalf("got %+v, want a master at rank zero", master)
	}
}

func TestPrepareReleasesTheWorstNodeAndBuildsTheMeshAgain(t *testing.T) {

	chain, hosts := newChainStub(), newHostsStub()
	hosts.failed[nodeA] = []mesh.Pair{mesh.NewPair(nodeA, nodeB), mesh.NewPair(nodeA, nodeC)}
	hosts.failed[nodeB] = []mesh.Pair{mesh.NewPair(nodeB, nodeA)}
	hosts.failed[nodeC] = []mesh.Pair{mesh.NewPair(nodeC, nodeA)}

	result, err := prepare(chain, hosts, &verifierStub{}).Execute(context.Background(), shardID, expired)

	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if len(result.Released) != 1 || result.Released[0].Node != nodeA {
		t.Fatalf("got %v, want only the node in the most broken pairs released", result.Released)
	}
	if len(chain.releases) != 1 || chain.releases[0].reason != vo.ReleaseUnreachable {
		t.Fatalf("got %+v, want one release recorded as unreachable", chain.releases)
	}
	if len(result.Config.Peers) != 2 || result.Config.Contains(nodeA) {
		t.Fatalf("got %+v, want the mesh rebuilt without the released node", result.Config.Peers)
	}
}

func TestPrepareGivesTheTunnelsTimeToComeUpBeforeCuttingAnyone(t *testing.T) {

	chain, hosts := newChainStub(), newHostsStub()
	hosts.heals = true
	hosts.failed[nodeA] = []mesh.Pair{mesh.NewPair(nodeA, nodeB), mesh.NewPair(nodeA, nodeC)}
	hosts.failed[nodeB] = []mesh.Pair{mesh.NewPair(nodeB, nodeA)}
	hosts.failed[nodeC] = []mesh.Pair{mesh.NewPair(nodeC, nodeA)}

	result, err := prepare(chain, hosts, &verifierStub{}).Execute(context.Background(), shardID, forever)

	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if len(chain.releases) != 0 {
		t.Fatalf("got %+v, want a handshake that needed a second try to cost nobody the run", chain.releases)
	}
	if len(result.Config.Peers) != 3 {
		t.Fatalf("got %+v, want the whole mesh kept", result.Config.Peers)
	}
}

func TestPrepareDropsAHostThatWillNotTakeThePeerList(t *testing.T) {

	chain, hosts := newChainStub(), newHostsStub()
	hosts.refuses[nodeA] = true

	result, err := prepare(chain, hosts, &verifierStub{}).Execute(context.Background(), shardID, forever)

	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if len(chain.releases) != 1 || chain.releases[0].node != nodeA || chain.releases[0].reason != vo.ReleaseFailedPrepare {
		t.Fatalf("got %+v, want the refusing node released as a failed prepare", chain.releases)
	}
	if len(result.Config.Peers) != 2 || result.Config.Contains(nodeA) {
		t.Fatalf("got %+v, want the mesh built from the hosts that took it", result.Config.Peers)
	}
}

func TestPrepareWaitsForTheReleaseToLandAndCarriesOn(t *testing.T) {

	chain, hosts := newChainStub(), newHostsStub()
	chain.lag = 2
	hosts.failed[nodeA] = []mesh.Pair{mesh.NewPair(nodeA, nodeB), mesh.NewPair(nodeA, nodeC)}

	result, err := prepare(chain, hosts, &verifierStub{}).Execute(context.Background(), shardID, expired)

	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if len(chain.releases) != 1 {
		t.Fatalf("got %+v, want the node kicked once rather than on every poll", chain.releases)
	}
	if len(result.Config.Peers) != 2 || result.Config.Contains(nodeA) {
		t.Fatalf("got %+v, want the mesh rebuilt without the released node", result.Config.Peers)
	}
}

func TestPrepareStopsWhenTheChainNeverGivesTheReleasedNodeBack(t *testing.T) {

	chain, hosts := newChainStub(), newHostsStub()
	chain.applies = false
	hosts.failed[nodeA] = []mesh.Pair{mesh.NewPair(nodeA, nodeB), mesh.NewPair(nodeA, nodeC)}

	_, err := prepareWithin(chain, hosts, &verifierStub{}, 0).Execute(context.Background(), shardID, expired)

	if !errors.Is(err, shard.ErrReleasePending) {
		t.Fatalf("got %v, want the run to stop rather than release the node twice", err)
	}
	if len(chain.releases) != 1 {
		t.Fatalf("got %+v, want exactly one release attempt", chain.releases)
	}
}

func TestPrepareRefusesWhatItCannotBuildAMeshFrom(t *testing.T) {
	cases := map[string]struct {
		arrange func(*chainStub, *hostsStub, *verifierStub)
		want    error
	}{
		"a shard the chain does not know": {
			arrange: func(c *chainStub, _ *hostsStub, _ *verifierStub) { c.found = false },
			want:    shard.ErrShardUnknown,
		},
		"a settled shard": {
			arrange: func(c *chainStub, _ *hostsStub, _ *verifierStub) { c.record.Status = shard.StatusSettled },
			want:    shard.ErrShardClosed,
		},
		"an expired shard": {
			arrange: func(c *chainStub, _ *hostsStub, _ *verifierStub) { c.height = c.record.ExpiresAtHeight },
			want:    shard.ErrShardClosed,
		},
		"no host answers at all": {
			arrange: func(_ *chainStub, h *hostsStub, _ *verifierStub) {
				h.silent[hostA], h.silent[hostB] = true, true
			},
			want: errHost,
		},
		"an identity signed by another host": {
			arrange: func(_ *chainStub, h *hostsStub, _ *verifierStub) {
				stolen := identityOf(nodeA)
				stolen.Signature = []byte(hostB)
				h.identities[hostA] = []mesh.Identity{stolen}
			},
			want: mesh.ErrForeignIdentity,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {

			chain, hosts, verifier := newChainStub(), newHostsStub(), &verifierStub{}
			tc.arrange(chain, hosts, verifier)

			_, err := prepare(chain, hosts, verifier).Execute(context.Background(), shardID, forever)

			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestPrepareWaitsForANodeThatHasNotReportedAnIdentityYet(t *testing.T) {

	chain, hosts := newChainStub(), newHostsStub()
	hosts.identities[hostA] = nil
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := prepare(chain, hosts, &verifierStub{}).Execute(ctx, shardID, forever)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want prepare still waiting on the quiet node", err)
	}
	if len(chain.releases) != 0 {
		t.Fatalf("got %+v, want nothing released before the prepare deadline", chain.releases)
	}
}

func TestPrepareGoesOnWithoutANodeThatStaysQuietPastTheDeadline(t *testing.T) {

	chain, hosts := newChainStub(), newHostsStub()
	hosts.identities[hostA] = nil

	result, err := prepare(chain, hosts, &verifierStub{}).Execute(context.Background(), shardID, expired)

	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if len(chain.releases) != 1 || chain.releases[0].reason != vo.ReleaseFailedPrepare {
		t.Fatalf("got %+v, want the quiet node released as a failed prepare", chain.releases)
	}
	if len(result.Config.Peers) != 2 || result.Config.Contains(nodeA) {
		t.Fatalf("got %+v, want the run to go on with the nodes that are ready", result.Config.Peers)
	}
}

func TestPrepareKeepsReleasingUntilWhatIsLeftIsConnected(t *testing.T) {

	chain, hosts := newChainStub(), newHostsStub()
	hosts.failed[nodeA] = []mesh.Pair{mesh.NewPair(nodeA, nodeB), mesh.NewPair(nodeA, nodeC)}
	hosts.failed[nodeB] = []mesh.Pair{mesh.NewPair(nodeB, nodeA), mesh.NewPair(nodeB, nodeC)}
	hosts.failed[nodeC] = []mesh.Pair{mesh.NewPair(nodeC, nodeA), mesh.NewPair(nodeC, nodeB)}

	result, err := prepare(chain, hosts, &verifierStub{}).Execute(context.Background(), shardID, expired)

	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if len(result.Released) != 2 || len(result.Config.Peers) != 1 {
		t.Fatalf("got %+v released and a mesh of %d, want the run cut down to what still works", result.Released, len(result.Config.Peers))
	}
}
