package api

import (
	"net/http"
	"slices"
	"testing"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
)

func withCacheControl(value string) map[string]string {
	headers := callerHeaders("caller-a")
	headers[cacheControlHeader] = value
	return headers
}

// Test flow:
//  1. Store a reply, change what the engine answers, and send the same chat with `Cache-Control: no-cache`.
//  2. Assert a second race ran and the fresh answer came back.
//  3. Send the chat again without the header and assert the fresh answer is now what the cache serves.
func TestNoCacheSkipsTheLookupAndReplacesTheEntryWithTheFreshAnswer(t *testing.T) {
	live := newHarness(t)
	live.inference.outcome = winnerOutcome()
	live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, callerHeaders("caller-a"))
	live.inference.reply = `{"id":"fresh","choices":[]}`

	bypassed := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, withCacheControl("no-cache"))
	replay := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, callerHeaders("caller-a"))

	if got := live.inference.runs.Load(); got != 2 {
		t.Fatalf("races = %d, want 2", got)
	}
	if replay.Body.String() != bypassed.Body.String() {
		t.Fatalf("replay = %q, want the fresh answer %q served from the cache", replay.Body.String(), bypassed.Body.String())
	}
}

// Test flow:
//  1. Send a chat with `Cache-Control: no-store`, then the same chat plainly.
//  2. Assert both ran a race, so the first stored nothing, and only the second was counted as stored.
func TestNoStoreSkipsTheLookupAndStoresNothing(t *testing.T) {
	live := newHarness(t)

	live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, withCacheControl("no-store"))
	live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, callerHeaders("caller-a"))

	if got := live.inference.runs.Load(); got != 2 {
		t.Fatalf("races = %d, want 2", got)
	}
	live.cached.mu.Lock()
	defer live.cached.mu.Unlock()
	if want := []string{"qwen/" + cacheResultStored}; !slices.Equal(live.cached.results, want) {
		t.Fatalf("cache results = %v, want %v", live.cached.results, want)
	}
}

// Test flow:
//  1. Store a reply, then send the same chat with each header value in turn.
//  2. Assert the values that name no-store or no-cache bypass the cache in either case, spelling or position.
//  3. Assert every other directive is ignored and the reply is served from the cache.
func TestOnlyNoCacheAndNoStoreAreActedOn(t *testing.T) {
	cases := []struct {
		header string
		bypass bool
	}{
		{"no-cache", true},
		{"NO-CACHE", true},
		{"max-age=0, no-cache", true},
		{`no-cache="Set-Cookie"`, true},
		{"no-store", true},
		{"private, No-Store", true},
		{"no-cache, no-store", true},
		{"max-age=0", false},
		{"private", false},
		{"only-if-cached", false},
		{"no-transform", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.header, func(t *testing.T) {
			live := newHarness(t)
			live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, callerHeaders("caller-a"))

			live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, withCacheControl(tc.header))

			wantRaces := int64(1)
			if tc.bypass {
				wantRaces = 2
			}
			if got := live.inference.runs.Load(); got != wantRaces {
				t.Fatalf("Cache-Control %q: races = %d, want %d", tc.header, got, wantRaces)
			}
		})
	}
}

// Test flow:
//  1. Store a reply, then block the chain for PoC generation.
//  2. Assert a request that bypasses the cache is refused with 503 like a miss and starts no race.
//  3. Assert the plain request is still served from the cache.
func TestABypassDuringPoCIsRefusedLikeAMiss(t *testing.T) {
	live := newHarness(t)
	live.swapConfig(func(next *config.Config) { next.Modes.PoCMode = config.PoCModeOff })
	live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, callerHeaders("caller-a"))
	live.snapshots.snapshot = chain.PhaseSnapshot{RequestsBlocked: true, BlockReason: chain.BlockReasonPoC, EpochPhase: chain.EpochPhasePoCGenerate}

	bypassed := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, withCacheControl("no-cache"))
	replay := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, callerHeaders("caller-a"))

	if bypassed.Code != http.StatusServiceUnavailable {
		t.Fatalf("bypass during PoC: got %d, want 503", bypassed.Code)
	}
	if replay.Code != http.StatusOK {
		t.Fatalf("plain replay during PoC: got %d, want 200", replay.Code)
	}
	if got := live.inference.runs.Load(); got != 1 {
		t.Fatalf("races = %d, want 1", got)
	}
}
