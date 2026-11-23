package api

import (
	"net/http"
	"testing"
)

const tinyLogLimit = 16

type replayTally struct {
	hits, misses, replays, live int64
}

func tallyOf(f *adoptionFixture) replayTally {
	var tally replayTally
	tally.hits, tally.misses, _, _ = f.live.server.CacheStats()
	tally.replays, tally.live = f.live.server.StreamLogStats()
	return tally
}

// Test flow:
//  1. Start a streamed request whose client leaves while its race goes on, and assert one live log and the first request's miss.
//  2. Repeat the identical request, wait until it has taken the log over, and let the race finish.
//  3. Assert the takeover is counted as one replay, no cache hit is counted, the miss count did not move, and the gauge is back to 0.
func TestAnAdoptedLiveLogIsCountedAsAReplayAndNotAsACacheHit(t *testing.T) {
	fixture := newAdoptionFixture(t, 1, succeeded())
	fixture.startAndLeave(t, "caller-a")
	if got, want := tallyOf(fixture), (replayTally{misses: 1, live: 1}); got != want {
		t.Fatalf("tally while the race goes on = %+v, want %+v", got, want)
	}

	_, served := fixture.startAdopter(t, t.Context())
	fixture.finishRace()
	<-served

	if got, want := tallyOf(fixture), (replayTally{misses: 1, replays: 1}); got != want {
		t.Fatalf("tally after the adopted replay = %+v, want %+v", got, want)
	}
}

// Test flow:
//  1. Start a streamed request whose client leaves after the first event, and let its race finish.
//  2. Repeat the identical request, which the cache serves.
//  3. Assert the repeat is counted as one cache hit and no replay, the miss count is the first request's alone, and no log is held.
func TestAReplayFromTheCacheIsCountedAsACacheHitAndNotAsAReplay(t *testing.T) {
	fixture := newAdoptionFixture(t, 1, succeeded())
	fixture.startAndLeave(t, "caller-a")
	fixture.finishRace()

	fixture.live.request(t, http.MethodPost, chatPath, streamChatBody, callerHeaders("caller-a"))

	if got, want := tallyOf(fixture), (replayTally{hits: 1, misses: 1}); got != want {
		t.Fatalf("tally after the cached replay = %+v, want %+v", got, want)
	}
}

// Test flow:
//  1. Add two logs under one key and a third under another, and read the gauge.
//  2. Drop one log for size, conclude-remove it again, then remove the others.
//  3. Assert the gauge reads 3, 2 after the drop, still 2 after the repeated removal, and 0 once all are removed.
func TestTheLiveLogGaugeFallsOnceForALogDroppedForSizeAndRemovedAgain(t *testing.T) {
	index := newStreamIndex()
	first := testLog(index, nil, tinyLogLimit)
	second := testLog(index, nil, testLogLimit)
	other := newStreamLog(cacheKey{model: "other"}, testLogLimit, nil, index, testLogGeneration)
	if _, live := index.stats(); live != 3 {
		t.Fatalf("live logs = %d, want 3", live)
	}

	first.append([]byte("data: 1\n\ndata: 2\n\n"))
	if _, live := index.stats(); live != 2 {
		t.Fatalf("live logs after a drop for size = %d, want 2", live)
	}
	index.remove(first)
	if _, live := index.stats(); live != 2 {
		t.Fatalf("live logs after removing the dropped log again = %d, want 2", live)
	}
	index.remove(second)
	index.remove(other)
	if _, live := index.stats(); live != 0 {
		t.Fatalf("live logs once all are removed = %d, want 0", live)
	}
}
