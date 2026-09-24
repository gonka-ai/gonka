package run_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/vo"
)

var (
	alice     = vo.Participant("gonka1alice")
	bob       = vo.Participant("gonka1bob")
	first     = vo.NodeRef{Participant: alice, NodeID: "node-a"}
	second    = vo.NodeRef{Participant: bob, NodeID: "node-b"}
	third     = vo.NodeRef{Participant: alice, NodeID: "node-c"}
	aliceHost = vo.Host{Participant: alice, Nodes: []vo.NodeRef{first, third}}
	bobHost   = vo.Host{Participant: bob, Nodes: []vo.NodeRef{second}}
	errHost   = errors.New("host does not answer")
)

func answered(nodes []vo.NodeRef) []run.NodeResult {
	results := make([]run.NodeResult, 0, len(nodes))
	for _, node := range nodes {
		results = append(results, run.NodeResult{Node: node, State: vo.ContainerCreated})
	}
	return results
}

func TestPerHostFailsOnlyTheNodesOfASilentHost(t *testing.T) {

	results := run.PerHost(context.Background(), []vo.Host{aliceHost, bobHost}, run.Failed,
		func(_ context.Context, host vo.Host) ([]run.NodeResult, error) {
			if host.Participant == alice {
				return nil, errHost
			}
			return answered(host.Nodes), nil
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

	results := run.PerHost(context.Background(), []vo.Host{aliceHost, bobHost}, run.Failed,
		func(_ context.Context, host vo.Host) ([]run.NodeResult, error) {
			return answered([]vo.NodeRef{host.Nodes[0], stranger}), nil
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

	results := run.PerHost(context.Background(), []vo.Host{{Participant: alice, Nodes: []vo.NodeRef{first}}, bobHost}, run.Failed,
		func(_ context.Context, host vo.Host) ([]run.NodeResult, error) {
			if host.Participant == bob {
				return answered(host.Nodes), nil
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

func TestPerHostAnswersInTheOrderTheHostsWereGiven(t *testing.T) {

	answeredFirst := make(chan struct{})

	results := run.PerHost(context.Background(), []vo.Host{bobHost, aliceHost}, run.Failed,
		func(_ context.Context, host vo.Host) ([]run.NodeResult, error) {
			if host.Participant != bob {
				close(answeredFirst)
				return answered(host.Nodes), nil
			}
			if !waitFor(answeredFirst) {
				t.Error("the hosts were asked one after another, so the order proves nothing")
			}
			return answered(host.Nodes), nil
		})

	want := []vo.NodeRef{second, first, third}
	if len(results) != len(want) {
		t.Fatalf("got %d results, want one per node", len(results))
	}
	for index, result := range results {
		if result.Node != want[index] {
			t.Fatalf("got %+v, want the answers in the order the hosts were given: %v", results, want)
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

	run.PerHost(context.Background(), []vo.Host{aliceHost, bobHost}, run.Failed,
		func(_ context.Context, host vo.Host) ([]run.NodeResult, error) {
			arrived <- host.Participant
			if !waitFor(both) {
				t.Error("a host waited alone: one slow host still costs the command a round trip each")
			}
			return answered(host.Nodes), nil
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

func TestANodeThatAnsweredWithTheFaultOfItsRunStillAnswered(t *testing.T) {

	faulted := run.StatusOf(first, run.Desired{}, run.Observed{Container: vo.ContainerAbsent}, &shared.Fault{Code: "IMAGE_NOT_DERIVED"})
	unreached := run.FailedStatus(first, errHost)

	if faulted.Unanswered() {
		t.Fatalf("got %+v, want a node that said what it holds counted as an answer", faulted)
	}
	if !unreached.Unanswered() {
		t.Fatalf("got %+v, want a node the call never reached counted as silent", unreached)
	}
}
