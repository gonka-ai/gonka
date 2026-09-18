package run_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shared/vo"
)

type requestLogStub struct {
	mu      sync.Mutex
	results map[string][]run.NodeResult
}

func (l *requestLogStub) Result(_ context.Context, ref run.RequestRef) ([]run.NodeResult, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	results, found := l.results[ref.String()]
	return results, found, nil
}

func (l *requestLogStub) Record(_ context.Context, ref run.RequestRef, results []run.NodeResult) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.results[ref.String()] = results
	return nil
}

func TestOnceAppliesTheSameRequestOnlyOnceEvenWhenItArrivesTwiceAtOnce(t *testing.T) {
	// arrange
	once := run.NewOnce(&requestLogStub{results: map[string][]run.NodeResult{}})
	ref := run.RequestRef{Op: run.OpDeploy, Shard: 7, Actor: "gonka1creator", ID: "req-1"}
	var applied atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	apply := func(context.Context) []run.NodeResult {
		applied.Add(1)
		close(entered)
		<-release
		return []run.NodeResult{{Node: nodeA, State: vo.ContainerCreated}}
	}

	// act
	answers := make(chan []run.NodeResult, 2)
	go func() {
		results, _ := once.Do(context.Background(), ref, apply)
		answers <- results
	}()
	<-entered
	go func() {
		results, _ := once.Do(context.Background(), ref, func(context.Context) []run.NodeResult {
			applied.Add(1)
			return nil
		})
		answers <- results
	}()
	close(release)
	first, second := <-answers, <-answers

	// assert
	if applied.Load() != 1 {
		t.Fatalf("applied %d times, want once", applied.Load())
	}
	if len(first) != 1 || len(second) != 1 || first[0] != second[0] {
		t.Fatalf("got %v and %v, want both callers handed the one recorded answer", first, second)
	}
}
