package api

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"devshard/cmd/gateway/chain"
)

const (
	freshSecondEvent = "data: {\"choices\":[{\"delta\":{\"content\":\"three\"},\"finish_reason\":\"stop\"}]}\n\n"
	freshStream      = firstEvent + freshSecondEvent + doneEvent
)

func (f *adoptionFixture) requestWithHeaders(t *testing.T, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	answered := httptest.NewRecorder()
	returned := make(chan struct{})
	go func() {
		defer close(returned)
		f.live.requestIntoWithContext(t, t.Context(), answered, http.MethodPost, chatPath, streamChatBody, headers)
	}()
	awaitValue(t, returned, "the request to be answered by a race of its own")
	return answered
}

func (f *adoptionFixture) serveRepeatsFromAnEngineOfTheirOwn() {
	f.live.inference.chunks = replyChunks
	f.live.server.inference = f.live.inference
}

func (f *adoptionFixture) cacheResults(t *testing.T) []string {
	t.Helper()
	f.live.cached.mu.Lock()
	defer f.live.cached.mu.Unlock()
	return slices.Clone(f.live.cached.results)
}

// Test flow:
//  1. Start a streamed request whose client leaves after the first event while its race goes on.
//  2. Repeat the request with `Cache-Control: no-cache` and with `no-store`, each against an engine that serves it whole.
//  3. Assert each repeat ran a race of its own and received its own reply instead of taking over the stream, and the first race ran once.
func TestNoCacheAndNoStoreDoNotAdoptALiveStream(t *testing.T) {
	for _, directive := range []string{"no-cache", "no-store"} {
		t.Run(directive, func(t *testing.T) {
			fixture := newAdoptionFixture(t, 1, succeeded())
			fixture.startAndLeave(t, "caller-a")
			fixture.serveRepeatsFromAnEngineOfTheirOwn()

			repeated := fixture.requestWithHeaders(t, withCacheControl(directive))
			fixture.finishRace()

			if repeated.Body.String() != fullStream || fixture.live.inference.runs.Load() != 1 || fixture.engine.runs.Load() != 1 {
				t.Fatalf("repeat = %q after %d races of its own and %d of the first, want a race of its own", repeated.Body.String(), fixture.live.inference.runs.Load(), fixture.engine.runs.Load())
			}
		})
	}
}

// Test flow:
//  1. Send a streamed request and let its stream be stored.
//  2. Change what the engine answers and send the same request with `Cache-Control: no-cache`.
//  3. Send the request plainly and assert the cache serves the fresh stream and only two races ran.
func TestNoCacheOnAStreamReplacesTheStoredStreamWithTheFreshOne(t *testing.T) {
	live := newHarness(t)
	live.inference.chunks = replyChunks
	live.request(t, http.MethodPost, chatPath, streamChatBody, callerHeaders("caller-a"))
	live.inference.chunks = []string{firstEvent, freshSecondEvent, doneEvent}

	bypassed := live.request(t, http.MethodPost, chatPath, streamChatBody, withCacheControl("no-cache"))
	replay := live.request(t, http.MethodPost, chatPath, streamChatBody, callerHeaders("caller-a"))

	if bypassed.Body.String() != freshStream || replay.Body.String() != freshStream || live.inference.runs.Load() != 2 {
		t.Fatalf("bypass = %q, replay = %q after %d races, want the fresh stream twice from two races", bypassed.Body.String(), replay.Body.String(), live.inference.runs.Load())
	}
}

// Test flow:
//  1. Send a streamed request with `Cache-Control: no-store`, then the same request plainly.
//  2. Assert both ran a race, so the first left no log and no entry, and only the second was counted as stored.
func TestNoStoreOnAStreamStoresNothingAndLeavesNoLog(t *testing.T) {
	live := newHarness(t)
	live.inference.chunks = replyChunks

	live.request(t, http.MethodPost, chatPath, streamChatBody, withCacheControl("no-store"))
	_, logsAfterNoStore := live.server.streams.stats()
	live.request(t, http.MethodPost, chatPath, streamChatBody, callerHeaders("caller-a"))

	live.cached.mu.Lock()
	defer live.cached.mu.Unlock()
	if want := []string{"qwen/" + cacheResultStored}; !slices.Equal(live.cached.results, want) || live.inference.runs.Load() != 2 || logsAfterNoStore != 0 {
		t.Fatalf("cache results = %v after %d races with %d logs, want %v after 2 races with none", live.cached.results, live.inference.runs.Load(), logsAfterNoStore, want)
	}
}

// Test flow:
//  1. Start a streamed request whose client leaves after the first event, then clear the cache by an epoch change or a host mark.
//  2. Repeat the identical request against an engine that serves it whole, then let the first race finish.
//  3. Assert the repeat ran a race of its own instead of adopting the stream, and the first stream was refused as invalidated.
func TestAStreamThatBeganBeforeAClearIsNeitherAdoptedNorStored(t *testing.T) {
	clears := map[string]func(*harness, *testing.T){
		"epoch change": func(live *harness, _ *testing.T) {
			live.server.ObservePhase(chain.PhaseSnapshot{EffectiveEpochIndex: 11})
		},
		"host mark": func(live *harness, t *testing.T) {
			live.request(t, http.MethodPost, "/v1/admin/suspicious-hosts", `{"participant_key":"`+winningParticipant+`"}`, adminHeaders())
		},
	}
	for name, clear := range clears {
		t.Run(name, func(t *testing.T) {
			fixture := newAdoptionFixture(t, 1, winnerOutcome())
			fixture.live.server.ObservePhase(chain.PhaseSnapshot{EffectiveEpochIndex: 10})
			fixture.startAndLeave(t, "caller-a")
			clear(fixture.live, t)
			fixture.serveRepeatsFromAnEngineOfTheirOwn()

			repeated := fixture.requestWithHeaders(t, callerHeaders("caller-a"))
			fixture.finishRace()

			if repeated.Body.String() != fullStream || fixture.live.inference.runs.Load() != 1 {
				t.Fatalf("repeat = %q after %d races of its own, want a race of its own", repeated.Body.String(), fixture.live.inference.runs.Load())
			}
			if want := []string{"qwen/" + cacheResultStored, "qwen/" + cacheRefusedInvalidated}; !slices.Equal(fixture.cacheResults(t), want) {
				t.Fatalf("cache results = %v, want %v", fixture.cacheResults(t), want)
			}
		})
	}
}

// Test flow:
//  1. Start a streamed request whose client leaves after the first event, vote its winning nonce invalid, and let the race finish.
//  2. Repeat the identical request against an engine that serves it whole.
//  3. Assert the finished stream was refused as invalidated, so the repeat ran a race of its own.
func TestAStreamInvalidatedBeforeItIsStoredIsNotStored(t *testing.T) {
	fixture := newAdoptionFixture(t, 1, winnerOutcome())
	fixture.startAndLeave(t, "caller-a")
	fixture.live.server.InvalidateCachedReply(invalidationEscrow, winningNonce)
	fixture.finishRace()
	fixture.serveRepeatsFromAnEngineOfTheirOwn()

	repeated := fixture.requestWithHeaders(t, callerHeaders("caller-a"))

	if want := []string{"qwen/" + cacheRefusedInvalidated, "qwen/" + cacheResultStored}; repeated.Body.String() != fullStream || fixture.live.inference.runs.Load() != 1 || !slices.Equal(fixture.cacheResults(t), want) {
		t.Fatalf("repeat = %q after %d races of its own with cache results %v, want a race of its own and %v", repeated.Body.String(), fixture.live.inference.runs.Load(), fixture.cacheResults(t), want)
	}
}

// Test flow:
//  1. Start a streamed request whose client stays, won by a participant on the never-trust list.
//  2. Repeat the identical request.
//  3. Assert the stream was refused as invalidated and the repeat ran a race of its own.
func TestAStreamWonByASuspiciousHostIsNotStored(t *testing.T) {
	live := newHarness(t)
	suspicious := winnerOutcome()
	suspicious.Attempts[0].Suspicious = true
	live.inference.outcome = suspicious
	live.inference.chunks = replyChunks

	live.request(t, http.MethodPost, chatPath, streamChatBody, callerHeaders("caller-a"))
	live.request(t, http.MethodPost, chatPath, streamChatBody, callerHeaders("caller-a"))

	live.cached.mu.Lock()
	defer live.cached.mu.Unlock()
	if want := []string{"qwen/" + cacheRefusedInvalidated, "qwen/" + cacheRefusedInvalidated}; !slices.Equal(live.cached.results, want) || live.inference.runs.Load() != 2 {
		t.Fatalf("cache results = %v after %d races, want %v after 2", live.cached.results, live.inference.runs.Load(), want)
	}
}

// Test flow:
//  1. Send a streamed request whose client stays and let its stream be stored under a winning nonce.
//  2. Vote that nonce invalid and repeat the request against an engine that serves a fresh stream.
//  3. Assert the repeat is not served from the cache, and what it stores is served to the next request.
func TestAStoredStreamWhoseNonceWasVotedInvalidIsRacedAgain(t *testing.T) {
	live := newHarness(t)
	live.inference.outcome = winnerOutcome()
	live.inference.chunks = replyChunks
	live.request(t, http.MethodPost, chatPath, streamChatBody, callerHeaders("caller-a"))
	live.server.InvalidateCachedReply(invalidationEscrow, winningNonce)
	live.inference.chunks = []string{firstEvent, freshSecondEvent, doneEvent}

	raced := live.request(t, http.MethodPost, chatPath, streamChatBody, callerHeaders("caller-a"))
	replay := live.request(t, http.MethodPost, chatPath, streamChatBody, callerHeaders("caller-a"))

	if !strings.Contains(raced.Body.String(), "three") || replay.Body.String() != raced.Body.String() || live.inference.runs.Load() != 2 {
		t.Fatalf("raced = %q, replay = %q after %d races, want the fresh stream raced once and replayed", raced.Body.String(), replay.Body.String(), live.inference.runs.Load())
	}
}
