package perf

import (
	"sync"
	"testing"
)

// Test flow:
//  1. Build an inflight gauge and acquire "alice" three times.
//  2. Release once and assert the count drops to 2.
//  3. Release a second time and assert the count drops to 1.
//  4. Release a third time and assert the count reaches 0 and the participant's key is dropped from the counts map.
func TestInflightGaugeAcquireReleaseBalance(t *testing.T) {
	g := newInflightGauge()
	g.acquire("alice")
	g.acquire("alice")
	g.acquire("alice")

	g.release("alice")
	if got := g.count("alice"); got != 2 {
		t.Fatalf("count() after 3 acquires + 1 release = %d, want 2", got)
	}

	g.release("alice")
	if got := g.count("alice"); got != 1 {
		t.Fatalf("count() after 3 acquires + 2 releases = %d, want 1", got)
	}

	g.release("alice")
	if got := g.count("alice"); got != 0 {
		t.Fatalf("count() after 3 acquires + 3 releases = %d, want 0", got)
	}
	if got := len(g.counts); got != 0 {
		t.Fatalf("counts map len = %d, want 0 (key dropped once balanced)", got)
	}
}

// Test flow:
//  1. For each case, acquire "alice" the case's acquire count and then release it the case's release count, varying combinations of extra releases: none, one, and several past zero.
//  2. Assert the count settles at 0 for every case.
//  3. Assert the counts map is empty afterward.
func TestInflightGaugeReleaseNeverGoesNegative(t *testing.T) {
	tests := []struct {
		name     string
		acquires int
		releases int
	}{
		{name: "release with no prior acquire", acquires: 0, releases: 1},
		{name: "release past an already-balanced participant", acquires: 1, releases: 2},
		{name: "many extra releases past zero", acquires: 2, releases: 5},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			g := newInflightGauge()
			for range testCase.acquires {
				g.acquire("alice")
			}
			for range testCase.releases {
				g.release("alice")
			}
			if got := g.count("alice"); got != 0 {
				t.Fatalf("count() = %d, want 0 (never negative)", got)
			}
			if got := len(g.counts); got != 0 {
				t.Fatalf("counts map len = %d, want 0", got)
			}
		})
	}
}

// Test flow:
//  1. Build an inflight gauge.
//  2. Assert the count for a participant that was never acquired is 0.
func TestInflightGaugeCountUnknownParticipantIsZero(t *testing.T) {
	g := newInflightGauge()
	if got := g.count("nobody"); got != 0 {
		t.Fatalf("count() for unknown participant = %d, want 0", got)
	}
}

// Test flow:
//  1. Build an inflight gauge, acquire and release "alice" so her count returns to 0 and her key is dropped.
//  2. Assert alice's count matches "bob", a participant never seen.
func TestInflightGaugeFreshParticipantMatchesDroppedParticipant(t *testing.T) {
	g := newInflightGauge()
	g.acquire("alice")
	g.release("alice")

	if got := g.count("alice"); got != g.count("bob") {
		t.Fatalf("count(alice, dropped) = %d, count(bob, never seen) = %d, want equal", got, g.count("bob"))
	}
}

// Test flow:
//  1. Build an inflight gauge; acquire "alice" twice and "bob" once.
//  2. Assert alice's count is 2 and bob's is 1.
//  3. Release alice once and assert bob's count is unaffected at 1.
func TestInflightGaugeTracksParticipantsIndependently(t *testing.T) {
	g := newInflightGauge()
	g.acquire("alice")
	g.acquire("alice")
	g.acquire("bob")

	if got := g.count("alice"); got != 2 {
		t.Fatalf("count(alice) = %d, want 2", got)
	}
	if got := g.count("bob"); got != 1 {
		t.Fatalf("count(bob) = %d, want 1", got)
	}

	g.release("alice")
	if got := g.count("bob"); got != 1 {
		t.Fatalf("count(bob) after releasing alice = %d, want unaffected 1", got)
	}
}

// Test flow:
//  1. Build an inflight gauge.
//  2. Run 200 goroutines that each acquire then release the same participant "shared".
//  3. Wait for all goroutines to finish.
//  4. Assert the count settles at 0 and the participant's key is dropped.
func TestInflightGaugeConcurrentAcquireReleaseSettlesAtZero(t *testing.T) {
	g := newInflightGauge()
	const goroutines = 200
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			g.acquire("shared")
			g.release("shared")
		}()
	}
	wg.Wait()

	if got := g.count("shared"); got != 0 {
		t.Fatalf("count() after %d concurrent acquire+release pairs = %d, want 0", goroutines, got)
	}
	if got := len(g.counts); got != 0 {
		t.Fatalf("counts map len = %d, want 0 (key dropped)", got)
	}
}

// Test flow:
//  1. Build an inflight gauge and start 200 goroutines that each acquire "shared", then block on a shared release channel before releasing.
//  2. Wait until all goroutines have acquired.
//  3. Assert the count is exactly 200 while none have released yet.
//  4. Close the release channel, wait for all goroutines to release, and assert the count settles back to 0.
func TestInflightGaugeConcurrentAcquireReachesExactTotalBeforeRelease(t *testing.T) {
	g := newInflightGauge()
	const goroutines = 200

	var acquired sync.WaitGroup
	acquired.Add(goroutines)
	release := make(chan struct{})

	var done sync.WaitGroup
	done.Add(goroutines)
	for range goroutines {
		go func() {
			defer done.Done()
			g.acquire("shared")
			acquired.Done()
			<-release
			g.release("shared")
		}()
	}

	acquired.Wait()
	if got := g.count("shared"); got != goroutines {
		t.Fatalf("count() after %d concurrent acquires (none released yet) = %d, want %d", goroutines, got, goroutines)
	}

	close(release)
	done.Wait()
	if got := g.count("shared"); got != 0 {
		t.Fatalf("count() after all releases settled = %d, want 0", got)
	}
}
