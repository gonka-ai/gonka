package api

import (
	"slices"
	"testing"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/engine"
)

func (f *adoptionFixture) cacheResults() []string {
	f.live.cached.mu.Lock()
	defer f.live.cached.mu.Unlock()
	return slices.Clone(f.live.cached.results)
}

// Test flow:
//  1. Start a streamed request whose client leaves, with a suspicious host as the race's winner, and let the race finish.
//  2. Read the cache's result counter.
//  3. Assert the stream was refused as invalidated and not stored.
func TestAStreamWonBySuspiciousHostIsNotCached(t *testing.T) {
	suspicious := succeeded()
	suspicious.WinnerNonce = 5
	suspicious.Attempts = []engine.AttemptOutcome{{Participant: "host-a", Nonce: 5, Suspicious: true}}
	fixture := newAdoptionFixture(t, 1, suspicious)
	fixture.startAndLeave(t, "caller-a")
	fixture.finishRace()

	if got, want := fixture.cacheResults(), []string{"qwen/" + cacheRefusedInvalidated}; !slices.Equal(got, want) {
		t.Fatalf("cache results = %v, want %v", got, want)
	}
}

// Test flow:
//  1. Start a streamed request whose client leaves, clear the cache by an epoch change while its race goes on, and let the race finish.
//  2. Read the cache's result counter.
//  3. Assert the stream was refused as invalidated.
func TestAStreamFromARaceThatBeganBeforeAClearIsNotStored(t *testing.T) {
	fixture := newAdoptionFixture(t, 1, succeeded())
	fixture.live.server.ObservePhase(chain.PhaseSnapshot{EffectiveEpochIndex: 10})
	fixture.startAndLeave(t, "caller-a")
	fixture.live.server.ObservePhase(chain.PhaseSnapshot{EffectiveEpochIndex: 11})
	fixture.finishRace()

	if got, want := fixture.cacheResults(), []string{"qwen/" + cacheRefusedInvalidated}; !slices.Equal(got, want) {
		t.Fatalf("cache results = %v, want %v", got, want)
	}
}

// Test flow:
//  1. Clear the cache by an epoch change, then start a streamed request whose client leaves, and let the race finish.
//  2. Read the cache's result counter.
//  3. Assert the stream was stored: a race begun after a clear carries the new generation.
func TestAStreamFromARaceThatBeganAfterAClearIsStored(t *testing.T) {
	fixture := newAdoptionFixture(t, 1, succeeded())
	fixture.live.server.ObservePhase(chain.PhaseSnapshot{EffectiveEpochIndex: 10})
	fixture.live.server.ObservePhase(chain.PhaseSnapshot{EffectiveEpochIndex: 11})
	fixture.startAndLeave(t, "caller-a")
	fixture.finishRace()

	if got, want := fixture.cacheResults(), []string{"qwen/" + cacheResultStored}; !slices.Equal(got, want) {
		t.Fatalf("cache results = %v, want %v", got, want)
	}
}

// Test flow:
//  1. Detach the client of a log begun in generation 0.
//  2. Try to adopt it as a request that sees generation 1, then as one that sees generation 0.
//  3. Assert the first is refused, as the cache refuses the log's reply, and the second adopts it.
func TestALogFromBeforeAClearIsNotAdopted(t *testing.T) {
	index := newStreamIndex()
	log := testLog(index, nil, testLogLimit)
	log.append([]byte("data: 1\n\n"))
	log.detachSocket()

	if _, adopted := index.adopt(log.key, 1); adopted {
		t.Fatal("a log begun before the cache was cleared was adopted")
	}
	if _, adopted := index.adopt(log.key, 0); !adopted {
		t.Fatal("a log of the current generation was not adopted")
	}
}
