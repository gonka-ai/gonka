package run_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shared/vo"
)

var (
	alice   = vo.Participant("gonka1alice")
	bob     = vo.Participant("gonka1bob")
	first   = vo.NodeRef{Participant: alice, NodeID: "node-a"}
	second  = vo.NodeRef{Participant: bob, NodeID: "node-b"}
	third   = vo.NodeRef{Participant: alice, NodeID: "node-c"}
	errHost = errors.New("host does not answer")
)

func answered(nodes []vo.NodeRef) []run.NodeResult {
	results := make([]run.NodeResult, 0, len(nodes))
	for _, node := range nodes {
		results = append(results, run.NodeResult{Node: node, State: vo.ContainerCreated})
	}
	return results
}

func TestPerHostAsksEachHostOnceForAllOfItsNodes(t *testing.T) {

	var mu sync.Mutex
	asked := map[vo.Participant][]vo.NodeRef{}

	results := run.PerHost(context.Background(), []vo.NodeRef{first, second, third}, run.Failed,
		func(_ context.Context, participant vo.Participant, nodes []vo.NodeRef) ([]run.NodeResult, error) {
			mu.Lock()
			defer mu.Unlock()

			asked[participant] = append(asked[participant], nodes...)
			return answered(nodes), nil
		})

	if len(asked) != 2 {
		t.Fatalf("got %d hosts asked, want one call per host", len(asked))
	}
	if len(asked[alice]) != 2 || asked[alice][0] != first || asked[alice][1] != third {
		t.Fatalf("got %v, want both of alice's nodes in one call", asked[alice])
	}
	if len(results) != 3 {
		t.Fatalf("got %d results, want one per node", len(results))
	}
}

func TestPerHostFailsOnlyTheNodesOfASilentHost(t *testing.T) {

	nodes := []vo.NodeRef{first, second, third}

	results := run.PerHost(context.Background(), nodes, run.Failed,
		func(_ context.Context, participant vo.Participant, held []vo.NodeRef) ([]run.NodeResult, error) {
			if participant == alice {
				return nil, errHost
			}
			return answered(held), nil
		})

	if len(results) != 3 {
		t.Fatalf("got %d results, want the silent host's nodes reported too", len(results))
	}
	for _, result := range results {
		if result.Node.Participant == alice && result.OK() {
			t.Fatalf("got %+v, want alice's nodes reported as failed", result)
		}
		if result.Node.Participant == bob && !result.OK() {
			t.Fatalf("got %+v, want bob's nodes answered", result)
		}
	}
}

func TestPerHostAnswersForTheNodesItAskedAboutAndNoOthers(t *testing.T) {

	stranger := vo.NodeRef{Participant: bob, NodeID: "node-x"}

	results := run.PerHost(context.Background(), []vo.NodeRef{first, second, third}, run.Failed,
		func(_ context.Context, _ vo.Participant, held []vo.NodeRef) ([]run.NodeResult, error) {
			return answered([]vo.NodeRef{held[0], stranger}), nil
		})

	if len(results) != 3 {
		t.Fatalf("got %d results, want one per node asked about", len(results))
	}
	for _, result := range results {
		if result.Node == stranger {
			t.Fatalf("got %+v, want a node nobody asked about left out", result)
		}
		if result.Node == third && result.OK() {
			t.Fatalf("got %+v, want the node the host said nothing about reported as failed", result)
		}
	}
}

func TestPerHostFailsANodeItsHostAnsweredForTwice(t *testing.T) {

	results := run.PerHost(context.Background(), []vo.NodeRef{first, second}, run.Failed,
		func(_ context.Context, participant vo.Participant, held []vo.NodeRef) ([]run.NodeResult, error) {
			if participant == bob {
				return answered(held), nil
			}
			return answered([]vo.NodeRef{first, first}), nil
		})

	if len(results) != 2 {
		t.Fatalf("got %d results, want one per node asked about", len(results))
	}
	for _, result := range results {
		if result.Node == first && result.OK() {
			t.Fatalf("got %+v, want a node its host could not answer for once reported as failed", result)
		}
		if result.Node == second && !result.OK() {
			t.Fatalf("got %+v, want the other host's node left alone", result)
		}
	}
}

func TestPerHostAnswersInTheOrderTheNodesWereNamed(t *testing.T) {

	answeredFirst := make(chan struct{})

	results := run.PerHost(context.Background(), []vo.NodeRef{second, first, third}, run.Failed,
		func(_ context.Context, participant vo.Participant, nodes []vo.NodeRef) ([]run.NodeResult, error) {
			if participant != bob {
				close(answeredFirst)
				return answered(nodes), nil
			}
			if !waitFor(answeredFirst) {
				t.Error("the hosts were asked one after another, so the order proves nothing")
			}
			return answered(nodes), nil
		})

	want := []vo.NodeRef{second, first, third}
	if len(results) != len(want) {
		t.Fatalf("got %d results, want one per node", len(results))
	}
	for index, result := range results {
		if result.Node != want[index] {
			t.Fatalf("got %+v, want the answers in the order the nodes were named: %v", results, want)
		}
	}
}

func TestPerHostAsksEveryHostAtTheSameTime(t *testing.T) {

	arrived := make(chan vo.Participant, 2)
	both := make(chan struct{})
	go func() {
		<-arrived
		<-arrived
		close(both)
	}()

	run.PerHost(context.Background(), []vo.NodeRef{first, second}, run.Failed,
		func(_ context.Context, participant vo.Participant, nodes []vo.NodeRef) ([]run.NodeResult, error) {
			arrived <- participant
			if !waitFor(both) {
				t.Error("a host waited alone: one slow host still costs the command a round trip each")
			}
			return answered(nodes), nil
		})
}

func waitFor(done chan struct{}) bool {
	select {
	case <-done:
		return true
	case <-time.After(2 * time.Second):
		return false
	}
}
