package api

import (
	"bytes"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/filters"
)

const streamChatBody = `{"model":"qwen","messages":[{"role":"user","content":"hi"}],"stream":true}`

func callerHeaders(key string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + key}
}

// chunkRecorder keeps every write separate, which httptest.ResponseRecorder concatenates away.
type chunkRecorder struct {
	header  http.Header
	status  int
	chunks  []string
	flushes int
}

func newChunkRecorder() *chunkRecorder { return &chunkRecorder{header: http.Header{}} }

func (w *chunkRecorder) Header() http.Header { return w.header }

func (w *chunkRecorder) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *chunkRecorder) Write(chunk []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.chunks = append(w.chunks, string(chunk))
	return len(chunk), nil
}

func (w *chunkRecorder) Flush() { w.flushes++ }

// Test flow:
//  1. Send the same chat completion twice from the same caller.
//  2. Assert both responses carry identical bodies.
//  3. Assert only one race ran, so the second request was a cache hit.
//  4. Assert the replay still carries the escrow's X-Devshard-ID header.
func TestASecondIdenticalRequestFromTheSameCallerIsServedFromTheCache(t *testing.T) {
	live := newHarness(t)

	first := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, callerHeaders("caller-a"))
	second := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, callerHeaders("caller-a"))

	if first.Body.String() != second.Body.String() {
		t.Fatalf("replay differs:\n first %q\nsecond %q", first.Body.String(), second.Body.String())
	}
	if got := live.inference.runs.Load(); got != 1 {
		t.Fatalf("races: got %d, want 1 (the second request must be a cache hit)", got)
	}
	if got := second.Header().Get("X-Devshard-ID"); got != "7" {
		t.Fatalf("X-Devshard-ID on the replay: got %q, want the escrow that produced the body", got)
	}
}

// Test flow:
//  1. Send the same chat completion from two different callers.
//  2. Assert two races ran, so a second caller is never served the first caller's reply.
func TestTheSameRequestFromADifferentCallerIsAMiss(t *testing.T) {
	live := newHarness(t)

	live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, callerHeaders("caller-a"))
	live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, callerHeaders("caller-b"))

	if got := live.inference.runs.Load(); got != 2 {
		t.Fatalf("races: got %d, want 2 (a second caller must not be served the first caller's reply)", got)
	}
}

// Test flow:
//  1. Send the same chat completion once from an authenticated caller and once with no Authorization header.
//  2. Assert two races ran.
func TestAnUnauthenticatedCallerIsNotServedAnAuthenticatedCallersReply(t *testing.T) {
	live := newHarness(t)

	live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, callerHeaders("caller-a"))
	live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, nil)

	if got := live.inference.runs.Load(); got != 2 {
		t.Fatalf("races: got %d, want 2", got)
	}
}

// Test flow:
//  1. Build a cache key for the same caller and body pinned to different escrow IDs.
//  2. Store one entry under escrow "7".
//  3. Assert a lookup pinned to escrow "7" hits, a lookup pinned to escrow "9" misses, and an unpinned lookup misses.
func TestTheSameCallerOnADifferentEscrowIsAMiss(t *testing.T) {
	pinned := func(escrowID string) cacheKey {
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		request.Header.Set("Authorization", "Bearer caller-a")
		request.SetPathValue("id", escrowID)
		return cacheKeyFor(request, filters.Result{Model: "qwen", Body: []byte(chatBody), Logprobs: filters.LogprobIntent{}, ClientStream: false, ClientUsage: false})
	}
	cache := newResponseCache(1 << 20)
	now := time.Unix(1700000000, 0)
	entry := cachedResponse{escrowID: "7", status: http.StatusOK, body: []byte(`{"id":"a"}`), bounds: []int{10}}

	cache.put(pinned("7"), entry, now)

	if _, hit := cache.get(pinned("7"), now); !hit {
		t.Fatal("the escrow that stored the entry must hit")
	}
	if _, hit := cache.get(pinned("9"), now); hit {
		t.Fatal("a request pinned to another devshard was served from this one's entry")
	}
	if _, hit := cache.get(pinned(""), now); hit {
		t.Fatal("an unpinned request was served from a pinned entry")
	}
}

// Test flow:
//  1. Make inference stream three separate chunks.
//  2. Send the same streaming chat completion twice from the same caller, recording chunks with a `chunkRecorder`.
//  3. Assert only one race ran.
//  4. Assert the live recording matches the inference chunks exactly.
//  5. Assert the replay reproduces the same chunk boundaries, with at least one flush per chunk and the same Content-Type.
func TestAStreamedReplyReplaysChunkForChunk(t *testing.T) {
	live := newHarness(t)
	live.inference.chunks = []string{
		"data: {\"choices\":[{\"delta\":{\"content\":\"one\"}}]}\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\"two\"},\"finish_reason\":\"stop\"}]}\n\n",
		"data: [DONE]\n\n",
	}

	produced := newChunkRecorder()
	live.requestInto(t, produced, http.MethodPost, "/v1/chat/completions", streamChatBody, callerHeaders("caller-a"))
	replayed := newChunkRecorder()
	live.requestInto(t, replayed, http.MethodPost, "/v1/chat/completions", streamChatBody, callerHeaders("caller-a"))

	if got := live.inference.runs.Load(); got != 1 {
		t.Fatalf("races: got %d, want 1", got)
	}
	if !slices.Equal(produced.chunks, live.inference.chunks) {
		t.Fatalf("live stream chunks: got %q, want %q", produced.chunks, live.inference.chunks)
	}
	if !slices.Equal(replayed.chunks, produced.chunks) {
		t.Fatalf("replay collapsed the stream: got %d chunks %q, want %d %q",
			len(replayed.chunks), replayed.chunks, len(produced.chunks), produced.chunks)
	}
	if replayed.flushes < len(replayed.chunks) {
		t.Fatalf("replay flushes: got %d, want at least one per chunk (%d)", replayed.flushes, len(replayed.chunks))
	}
	if got := replayed.header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("replayed Content-Type: got %q", got)
	}
}

// Test flow:
//  1. Send a chat completion once to fill the cache, and record the limiter's acquire and token counts.
//  2. Send the identical completion again.
//  3. Assert the replay is 200 and neither the limiter's acquire count nor its token budget moved.
func TestACacheHitTakesNoLimiterSlotAndNoTokenBudget(t *testing.T) {
	live := newHarness(t)

	live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, callerHeaders("caller-a"))
	acquiresAfterMiss := live.limiter.acquires.Load()
	tokensAfterMiss := live.limiter.tokens.Load()

	hit := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, callerHeaders("caller-a"))

	if hit.Code != http.StatusOK {
		t.Fatalf("cache hit: got %d (%s)", hit.Code, hit.Body.String())
	}
	if acquiresAfterMiss != 1 {
		t.Fatalf("the first request must take one slot: got %d", acquiresAfterMiss)
	}
	if got := live.limiter.acquires.Load(); got != acquiresAfterMiss {
		t.Fatalf("a cache hit took a limiter slot: acquires %d, want %d", got, acquiresAfterMiss)
	}
	if got := live.limiter.tokens.Load(); got != tokensAfterMiss {
		t.Fatalf("a cache hit charged the token budget: in flight %d, want %d", got, tokensAfterMiss)
	}
}

// Test flow:
//  1. Make inference fail with `engine.ErrStopped`.
//  2. Send the same chat completion twice.
//  3. Assert two races ran, so a failure is never replayed.
func TestAFailedRaceIsNotCached(t *testing.T) {
	live := newHarness(t)
	live.inference.reply = ""
	live.inference.err = engine.ErrStopped
	live.inference.outcome = engine.RaceOutcome{EscrowID: "7"}

	live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, callerHeaders("caller-a"))
	live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, callerHeaders("caller-a"))

	if got := live.inference.runs.Load(); got != 2 {
		t.Fatalf("races: got %d, want 2 (a failure must not be replayed)", got)
	}
}

// Test flow:
//  1. Make inference stream an SSE error event under what would otherwise be a 200.
//  2. Send the same chat completion twice.
//  3. Assert two races ran, so an error folded into a success is not treated as an answer to replay.
func TestAnErrorFoldedIntoASuccessIsNotCached(t *testing.T) {
	live := newHarness(t)
	live.inference.reply = ""
	live.inference.chunks = []string{"data: {\"error\":{\"message\":\"empty content stream\"}}\n\n", "data: [DONE]\n\n"}
	live.inference.outcome = engine.RaceOutcome{EscrowID: "7"}

	live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, callerHeaders("caller-a"))
	live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, callerHeaders("caller-a"))

	if got := live.inference.runs.Load(); got != 2 {
		t.Fatalf("races: got %d, want 2 (an error folded into a 200 is no answer to replay)", got)
	}
}

// Test flow:
//  1. Make inference emit one chunk and then fail with `engine.ErrAllAttemptsFailed`.
//  2. Send the same streaming chat completion twice, recording the first with a `chunkRecorder`.
//  3. Assert the first response keeps its already-committed 200 status.
//  4. Assert two races ran, so a stream that failed after starting is not replayed.
func TestAStreamThatFailsAfterItStartedIsNotCached(t *testing.T) {
	live := newHarness(t)
	live.inference.reply = ""
	live.inference.chunks = []string{"data: {\"choices\":[{\"delta\":{\"content\":\"one\"}}]}\n\n"}
	live.inference.err = engine.ErrAllAttemptsFailed
	live.inference.outcome = engine.RaceOutcome{EscrowID: "7"}

	first := newChunkRecorder()
	live.requestInto(t, first, http.MethodPost, "/v1/chat/completions", streamChatBody, callerHeaders("caller-a"))
	live.requestInto(t, newChunkRecorder(), http.MethodPost, "/v1/chat/completions", streamChatBody, callerHeaders("caller-a"))

	if first.status != http.StatusOK {
		t.Fatalf("a started stream must keep its status: got %d, want 200", first.status)
	}
	if got := live.inference.runs.Load(); got != 2 {
		t.Fatalf("races: got %d, want 2 (a failed stream must not be replayed)", got)
	}
}

// Test flow:
//  1. Configure the chat cache's max bytes to zero.
//  2. Send the same chat completion twice.
//  3. Assert two races ran, so the cache is effectively off.
func TestZeroMaxBytesDisablesTheCache(t *testing.T) {
	live := newHarness(t, func(next *config.Config) { next.Cache.ChatCacheMaxBytes = 0 })

	live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, callerHeaders("caller-a"))
	live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, callerHeaders("caller-a"))

	if got := live.inference.runs.Load(); got != 2 {
		t.Fatalf("races: got %d, want 2 (the cache is off)", got)
	}
}

// Test flow:
//  1. Seed a cache entry directly with a body carrying an upstream-failure SSE error.
//  2. Read it back.
//  3. Assert the read is a miss, and the poisoned entry is removed from the cache afterward.
func TestAnEntryPoisonedAfterItWasStoredDropsItselfOnRead(t *testing.T) {
	cache := newResponseCache(1 << 20)
	now := time.Unix(1700000000, 0)
	key := cacheKey{caller: sha256.Sum256([]byte("a")), model: "qwen", body: sha256.Sum256([]byte("b"))}
	poisoned := []byte(`data: {"error":{"message":"upstream request timeout","type":"server_error"}}` + "\n\n")

	cache.entries[key] = cachedResponse{escrowID: "7", status: http.StatusOK, body: poisoned, bounds: []int{len(poisoned)}, expiresAt: now.Add(time.Hour)}

	if _, hit := cache.get(key, now); hit {
		t.Fatal("a transient upstream failure was replayed from the cache")
	}
	if len(cache.entries) != 0 {
		t.Fatalf("the poisoned entry survived the read: %d entries left", len(cache.entries))
	}
}

// Test flow:
//  1. Store an entry, then read it back at a time past `cacheEntryTTL`.
//  2. Assert that read is a miss.
//  3. Store the same entry into a cache sized smaller than the entry.
//  4. Assert `put` refuses it as `cacheRefusedTooLarge` and nothing is stored.
func TestAnExpiredEntryIsAMissAndAnOversizedOneIsNeverStored(t *testing.T) {
	now := time.Unix(1700000000, 0)
	key := cacheKey{caller: sha256.Sum256([]byte("a")), model: "qwen", body: sha256.Sum256([]byte("b"))}
	body := []byte(`{"id":"a"}`)
	entry := cachedResponse{escrowID: "7", status: http.StatusOK, body: body, bounds: []int{len(body)}}

	expiring := newResponseCache(1 << 20)
	expiring.put(key, entry, now)
	if _, hit := expiring.get(key, now.Add(cacheEntryTTL)); hit {
		t.Fatal("an entry at its expiry was still served")
	}

	tiny := newResponseCache(1)
	if refusal := tiny.put(key, entry, now); refusal != cacheRefusedTooLarge {
		t.Fatalf("put refused with %q, want %q", refusal, cacheRefusedTooLarge)
	}
	if len(tiny.entries) != 0 {
		t.Fatalf("an entry larger than the whole cap was stored: %d entries", len(tiny.entries))
	}
}

// Test flow:
//  1. Build a storable body of a known size, so the cache only ever accounts for replies it agreed to keep.
//  2. Store 10 distinct entries into a cache sized to hold only 4.
//  3. Assert the cache's total bytes never exceed its cap.
//  4. Assert eviction left at least one entry rather than emptying the cache.
func TestTheCapEvictsUntilTheCacheFits(t *testing.T) {
	now := time.Unix(1700000000, 0)
	answer := strings.Repeat("x", 512-len(`{"choices":[{"index":0,"message":{"content":""},"finish_reason":"stop"}]}`))
	body := []byte(`{"choices":[{"index":0,"message":{"content":"` + answer + `"},"finish_reason":"stop"}]}`)
	cache := newResponseCache(4 * (int64(len(body)) + cacheEntryOverhead))

	for index := range 10 {
		key := cacheKey{caller: sha256.Sum256([]byte("a")), model: "qwen", body: sha256.Sum256([]byte{byte('a' + index)})}
		cache.put(key, cachedResponse{escrowID: "7", status: http.StatusOK, body: body, bounds: []int{len(body)}}, now)
	}

	if cache.totalBytes > cache.maxBytes {
		t.Fatalf("retained %d bytes over a %d cap", cache.totalBytes, cache.maxBytes)
	}
	if len(cache.entries) == 0 {
		t.Fatal("eviction emptied the cache instead of making room")
	}
}

// Test flow:
//  1. For both a non-streaming and a streaming reply, begin a live `clientStream` and set its headers.
//  2. Serve a cached entry with the same escrow, content type and streaming flag through `serveCached`.
//  3. Assert the live and replayed responses carry identical headers.
func TestCachedReplayAndLiveStreamAgreeOnHeaders(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		name := "non_streaming"
		contentType := "application/json"
		if streaming {
			name, contentType = "streaming", "text/event-stream"
		}
		t.Run(name, func(t *testing.T) {
			live := httptest.NewRecorder()
			stream := newClientStream(live, "req-1", streaming, true, filters.LogprobIntent{}, nil)
			stream.beginLocked(contentType)
			live.Header().Set(EscrowHeader, "escrow-1")

			replay := httptest.NewRecorder()
			serveCached(replay, "req-1", cachedResponse{
				escrowID:    "escrow-1",
				contentType: contentType,
				stream:      streaming,
				status:      http.StatusOK,
			})

			if !reflect.DeepEqual(live.Header(), replay.Header()) {
				t.Fatalf("live headers %v != replayed headers %v", live.Header(), replay.Header())
			}
		})
	}
}

// Test flow:
//  1. Write past a `cacheRecorder`'s limit in repeated chunks.
//  2. Assert the recorder marks itself overflowed and releases its buffer.
//  3. Assert an overflowed recording is never offered to the cache as storable.
func TestCacheRecorderStopsBufferingPastWhatTheCacheCouldStore(t *testing.T) {
	const limit = 4096
	recorder := &cacheRecorder{ResponseWriter: httptest.NewRecorder(), limit: limit}
	chunk := bytes.Repeat([]byte("x"), 1024)

	for range 12 {
		if _, err := recorder.Write(chunk); err != nil {
			t.Fatalf("Write(): %v", err)
		}
	}

	if !recorder.overflowed {
		t.Fatal("recorder kept buffering past the cache's own limit")
	}
	if buffered := recorder.body.Len(); buffered != 0 {
		t.Fatalf("buffered %d bytes after overflow, want the buffer released", buffered)
	}
	if _, storable := recorder.entry("escrow-1", true, nil); storable {
		t.Fatal("an overflowed recording was offered to the cache")
	}
}

// Test flow:
//  1. Write one chunk within a `cacheRecorder`'s limit.
//  2. Assert the recording is offered to the cache as storable.
func TestCacheRecorderKeepsARecordingInsideTheLimit(t *testing.T) {
	recorder := &cacheRecorder{ResponseWriter: httptest.NewRecorder(), limit: 1 << 20}
	recorder.WriteHeader(http.StatusOK)

	if _, err := recorder.Write([]byte(`data: {"choices":[]}` + "\n\n")); err != nil {
		t.Fatalf("Write(): %v", err)
	}

	if _, storable := recorder.entry("escrow-1", true, nil); !storable {
		t.Fatal("a recording inside the limit was refused")
	}
}

// Test flow:
//  1. Build cache keys for the same request and body, varying the logprob intent across keep, keep-with-alternatives, and neither.
//  2. Assert each pair of intents produces a distinct key, since the normalized body alone cannot tell them apart.
//  3. Build a further key for the same "keep" intent but with a usage chunk requested.
//  4. Assert it too differs from the plain "keep" key.
func TestTheCacheKeySeparatesRequestsByWhatTheyAskedFor(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	request.Header.Set("Authorization", "Bearer one-caller")
	body := []byte(`{"model":"qwen","logprobs":true}`)

	asked := cacheKeyFor(request, filters.Result{Model: "qwen", Body: body, Logprobs: filters.LogprobIntent{Keep: true}, ClientStream: false, ClientUsage: false})
	askedForAlternatives := cacheKeyFor(request, filters.Result{Model: "qwen", Body: body, Logprobs: filters.LogprobIntent{Keep: true, KeepTop: true}})
	askedForNeither := cacheKeyFor(request, filters.Result{Model: "qwen", Body: body, Logprobs: filters.LogprobIntent{}, ClientStream: false, ClientUsage: false})

	if asked == askedForNeither {
		t.Fatal("a request that asked for logprobs shares an entry with one that did not")
	}
	if asked == askedForAlternatives {
		t.Fatal("a request that asked for alternatives shares an entry with one that did not")
	}
	askedForUsage := cacheKeyFor(request, filters.Result{Model: "qwen", Body: body, Logprobs: filters.LogprobIntent{Keep: true}, ClientStream: false, ClientUsage: true})
	if asked == askedForUsage {
		t.Fatal("a request that asked for a usage chunk shares an entry with one that did not")
	}
}

// Test flow:
//  1. Build cache keys for the same request and body, once buffered and once forced to stream.
//  2. Assert the two keys differ, so a buffered and a streamed caller never share an entry.
func TestTheCacheSeparatesReplyShapes(t *testing.T) {
	t.Parallel()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	body := []byte(chatBody)

	buffered := cacheKeyFor(request, filters.Result{Model: "qwen", Body: body, Logprobs: filters.LogprobIntent{}, ClientStream: false, ClientUsage: false})
	streamed := cacheKeyFor(request, filters.Result{Model: "qwen", Body: body, Logprobs: filters.LogprobIntent{}, ClientStream: true, ClientUsage: false})

	if buffered == streamed {
		t.Fatal("a buffered and a streamed caller share one cache key")
	}
}

// Test flow:
//  1. Make inference stream one complete chunk followed by a chunk cut off mid-event.
//  2. Send the same streaming chat completion twice.
//  3. Assert two races ran, so a stream that stopped mid-event is never replayed.
func TestAStreamThatEndedMidEventIsNotCached(t *testing.T) {
	live := newHarness(t)
	live.inference.reply = ""
	live.inference.chunks = []string{
		"data: {\"choices\":[{\"delta\":{\"content\":\"one\"}}]}\n\n",
		"data: {\"choices\":[{\"delta\":{\"cont",
	}
	live.inference.outcome = engine.RaceOutcome{EscrowID: "7"}

	live.requestInto(t, newChunkRecorder(), http.MethodPost, "/v1/chat/completions", streamChatBody, callerHeaders("caller-a"))
	live.requestInto(t, newChunkRecorder(), http.MethodPost, "/v1/chat/completions", streamChatBody, callerHeaders("caller-a"))

	if got := live.inference.runs.Load(); got != 2 {
		t.Fatalf("races: got %d, want 2 (a reply that stopped mid-answer must not be replayed from cache)", got)
	}
}

// Test flow:
//  1. Read `entryLimit()` off a cache sized at 1 GiB.
//  2. Assert it caps at `maxBufferedResponseBytes`, not the cache's own size.
//  3. Read `entryLimit()` off a cache sized at 1 KiB.
//  4. Assert it caps at the cache's own ceiling instead.
func TestOneRecordedEntryIsBoundedByOneReplyNotByTheWholeCache(t *testing.T) {
	if limit := newResponseCache(1 << 30).entryLimit(); limit != maxBufferedResponseBytes {
		t.Errorf("entryLimit() on a 1 GiB cache = %d, want %d", limit, int64(maxBufferedResponseBytes))
	}
	if limit := newResponseCache(1 << 10).entryLimit(); limit != 1<<10 {
		t.Errorf("entryLimit() on a 1 KiB cache = %d, want the cache's own ceiling", limit)
	}
}

// Test flow:
//  1. Write 40 chunks through a `cacheRecorder` and take its stored entry.
//  2. Assert the entry was accepted for storage.
//  3. Assert the entry's body and bounds slices carry no spare capacity beyond their length.
func TestAStoredEntryHoldsExactlyWhatItIsChargedFor(t *testing.T) {
	recorder := newCacheRecorder(httptest.NewRecorder(), 1<<20, true, nil)
	for range 40 {
		if _, err := recorder.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"word \"}}]}\n\n")); err != nil {
			t.Fatalf("Write(): %v", err)
		}
	}

	entry, stored := recorder.entry("escrow-1", true, nil)
	if !stored {
		t.Fatal("the recorder refused to store a reply it accepted")
	}
	if cap(entry.body) != len(entry.body) {
		t.Errorf("entry body holds %d bytes but is charged for %d", cap(entry.body), len(entry.body))
	}
	if cap(entry.bounds) != len(entry.bounds) {
		t.Errorf("entry bounds hold %d, charged for %d", cap(entry.bounds), len(entry.bounds))
	}
}

// Test flow:
//  1. Make inference stream one reasoning chunk and stop without a finish reason.
//  2. Send the same streaming chat completion twice.
//  3. Assert the first response still delivers what arrived, terminator included.
//  4. Assert two races ran, so an unfinished answer is never replayed.
func TestAStreamThatStoppedMidAnswerIsNotCached(t *testing.T) {
	live := newHarness(t)
	live.inference.chunks = []string{`data: {"choices":[{"index":0,"delta":{"reasoning":"still working"}}]}` + "\n\n"}

	first := live.request(t, http.MethodPost, "/v1/chat/completions", streamChatBody, callerHeaders("caller-a"))
	live.request(t, http.MethodPost, "/v1/chat/completions", streamChatBody, callerHeaders("caller-a"))

	if delivered := first.Body.String(); !strings.Contains(delivered, "still working") || !strings.Contains(delivered, "[DONE]") {
		t.Fatalf("the client is still served what arrived, terminator included: %q", delivered)
	}
	if got := live.inference.runs.Load(); got != 2 {
		t.Fatalf("races: got %d, want 2 (an unfinished answer must not be replayed)", got)
	}
}

// Test flow:
//  1. Make inference stream content followed by a chunk carrying `finish_reason: "stop"`.
//  2. Send the same streaming chat completion twice.
//  3. Assert only one race ran, since a finished answer is what the cache is for.
func TestAStreamThatFinishedItsAnswerIsCached(t *testing.T) {
	live := newHarness(t)
	live.inference.chunks = []string{
		`data: {"choices":[{"index":0,"delta":{"content":"ok"}}]}` + "\n\n",
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n",
	}

	live.request(t, http.MethodPost, "/v1/chat/completions", streamChatBody, callerHeaders("caller-a"))
	live.request(t, http.MethodPost, "/v1/chat/completions", streamChatBody, callerHeaders("caller-a"))

	if got := live.inference.runs.Load(); got != 1 {
		t.Fatalf("races: got %d, want 1 (a finished answer is what the cache is for)", got)
	}
}

// Test flow:
//  1. Make inference stream one chunk with no finish reason.
//  2. Send the same non-streaming chat completion twice.
//  3. Assert two races ran, so a folded, unfinished answer is never replayed either.
func TestAFoldedAnswerThatStoppedMidAnswerIsNotCached(t *testing.T) {
	live := newHarness(t)
	live.inference.chunks = []string{`data: {"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"still w"}}]}` + "\n\n"}

	live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, callerHeaders("caller-a"))
	live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, callerHeaders("caller-a"))

	if got := live.inference.runs.Load(); got != 2 {
		t.Fatalf("races: got %d, want 2 (a folded unfinished answer must not be replayed either)", got)
	}
}

// Test flow:
//  1. Build a streaming entry whose body carries content but no finish reason.
//  2. Call `cache.put` with it.
//  3. Assert the refusal is `filters.CacheRefusedUnfinished` and nothing was stored.
func TestPutRefusesAnUnfinishedAnswerAndNamesIt(t *testing.T) {
	cache := newResponseCache(1 << 20)
	now := time.Unix(1700000000, 0)
	key := cacheKey{caller: sha256.Sum256([]byte("a")), model: "qwen", body: sha256.Sum256([]byte("b"))}
	unfinished := []byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"half\"}}]}\n\ndata: [DONE]\n\n")

	refusal := cache.put(key, cachedResponse{escrowID: "7", stream: true, status: http.StatusOK, body: unfinished, bounds: []int{len(unfinished)}}, now)

	if refusal != filters.CacheRefusedUnfinished {
		t.Fatalf("put refused with %q, want %q", refusal, filters.CacheRefusedUnfinished)
	}
	if len(cache.entries) != 0 {
		t.Fatalf("an unfinished answer was stored: %d entries", len(cache.entries))
	}
}

// Test flow:
//  1. Make inference stream one JSON object split across two `data:` lines with a finish reason on the second.
//  2. Send the same streaming chat completion twice.
//  3. Assert only one race ran, so a finished answer split across data lines still reads as finished.
func TestAStreamSplitAcrossDataLinesIsJudgedWhole(t *testing.T) {
	live := newHarness(t)
	live.inference.chunks = []string{"data: {\"choices\":[{\"index\":0,\ndata: \"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"}

	live.request(t, http.MethodPost, "/v1/chat/completions", streamChatBody, callerHeaders("caller-a"))
	live.request(t, http.MethodPost, "/v1/chat/completions", streamChatBody, callerHeaders("caller-a"))

	if got := live.inference.runs.Load(); got != 1 {
		t.Fatalf("races: got %d, want 1 (a finished answer split across data lines is still finished)", got)
	}
}

// Test flow:
//  1. Make inference stream one finished event with no trailing blank line.
//  2. Send the same streaming chat completion twice.
//  3. Assert the first response's terminator is written on its own line, not glued onto the host's event.
//  4. Assert only one race ran, so the finished answer still earned its cache entry.
func TestATerminatorIsNotGluedOntoAnUnterminatedEvent(t *testing.T) {
	live := newHarness(t)
	live.inference.chunks = []string{`data: {"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`}

	first := live.request(t, http.MethodPost, "/v1/chat/completions", streamChatBody, callerHeaders("caller-a"))
	live.request(t, http.MethodPost, "/v1/chat/completions", streamChatBody, callerHeaders("caller-a"))

	if delivered := first.Body.String(); !strings.Contains(delivered, "}\n\ndata: [DONE]") {
		t.Fatalf("the terminator was glued onto the host's last event: %q", delivered)
	}
	if got := live.inference.runs.Load(); got != 1 {
		t.Fatalf("races: got %d, want 1 (a finished answer must still earn its entry)", got)
	}
}

// Test flow:
//  1. Make inference stream one finished event already closed with a CRLF blank line.
//  2. Send one streaming chat completion.
//  3. Assert no extra separator was written after the event that already ended.
func TestACrlfFramedEventIsNotSeparatedTwice(t *testing.T) {
	live := newHarness(t)
	live.inference.chunks = []string{"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\r\n\r\n"}

	first := live.request(t, http.MethodPost, "/v1/chat/completions", streamChatBody, callerHeaders("caller-a"))

	if delivered := first.Body.String(); strings.Contains(delivered, "\r\n\r\n\n\n") {
		t.Fatalf("a separator was written after an event that already ended: %q", delivered)
	}
}

// Test flow:
//  1. Fill the cache with one chat completion, then flip the chain snapshot to blocked for PoC generation.
//  2. Send the identical completion again, having recorded the limiter's acquire count beforehand.
//  3. Assert the replay is 200 with the same cached body.
//  4. Assert no race ran and no limiter slot was taken for the replay.
func TestACachedReplyIsServedWhileTheChainIsInPoC(t *testing.T) {
	live := newHarness(t)
	live.swapConfig(func(next *config.Config) { next.Modes.PoCMode = config.PoCModeOff })
	first := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, callerHeaders("caller-a"))
	live.snapshots.snapshot = chain.PhaseSnapshot{RequestsBlocked: true, BlockReason: chain.BlockReasonPoC, EpochPhase: chain.EpochPhasePoCGenerate}
	acquiresBefore := live.limiter.acquires.Load()

	replay := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, callerHeaders("caller-a"))

	if replay.Code != http.StatusOK || replay.Body.String() != first.Body.String() {
		t.Fatalf("replay during PoC: got %d %q, want 200 with the cached body %q", replay.Code, replay.Body.String(), first.Body.String())
	}
	if got := live.inference.runs.Load(); got != 1 {
		t.Fatalf("races: got %d, want 1 (the replay must not reach a host)", got)
	}
	if got := live.limiter.acquires.Load(); got != acquiresBefore {
		t.Fatalf("the replay took %d limiter slots, want none", got-acquiresBefore)
	}
}

// Test flow:
//  1. Set the chain snapshot blocked for confirmation-PoC generation.
//  2. Send a chat completion that has never been cached.
//  3. Assert the response is 503 and no race started.
func TestACacheMissWhileTheChainIsInPoCIsStillRefused(t *testing.T) {
	live := newHarness(t)
	live.swapConfig(func(next *config.Config) { next.Modes.PoCMode = config.PoCModeOff })
	live.snapshots.snapshot = chain.PhaseSnapshot{RequestsBlocked: true, BlockReason: chain.BlockReasonConfirmationPoC, ConfirmationPoCPhase: chain.ConfirmationPoCGeneration}

	refused := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, callerHeaders("caller-a"))

	if refused.Code != http.StatusServiceUnavailable {
		t.Fatalf("a miss during PoC: got %d, want 503", refused.Code)
	}
	if got := live.inference.runs.Load(); got != 0 {
		t.Fatalf("a miss during PoC started %d races", got)
	}
}

// Test flow:
//  1. Fill the cache with one chat completion while the chain snapshot is healthy.
//  2. Age the snapshot's last-healthy time past the configured staleness bound.
//  3. Send the identical completion again.
//  4. Assert the replay is refused with 503 and only the original race ever ran.
func TestAStaleChainServesNoCachedReply(t *testing.T) {
	live := newHarness(t, func(configuration *config.Config) { configuration.Chain.SnapshotMaxAgeSeconds = 30 })
	live.snapshots.snapshot = chain.PhaseSnapshot{LastHealthyAt: harnessClock}
	live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, callerHeaders("caller-a"))
	live.snapshots.snapshot = chain.PhaseSnapshot{LastHealthyAt: harnessClock.Add(-time.Hour)}

	refused := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, callerHeaders("caller-a"))

	if refused.Code != http.StatusServiceUnavailable {
		t.Fatalf("a replay behind a stale chain: got %d, want 503", refused.Code)
	}
	if got := live.inference.runs.Load(); got != 1 {
		t.Fatalf("races: got %d, want only the one that filled the cache", got)
	}
}

// Test flow:
//  1. Fill the cache with one chat completion while the chain snapshot is healthy.
//  2. Flip the snapshot to blocked for PoC generation with a last-healthy time past the staleness bound.
//  3. Send the identical completion again.
//  4. Assert the replay still returns the cached 200 body.
func TestASnapshotThatWentStaleDuringPoCStillServesACachedReply(t *testing.T) {
	live := newHarness(t, func(configuration *config.Config) {
		configuration.Chain.SnapshotMaxAgeSeconds = 30
		configuration.Modes.PoCMode = config.PoCModeOff
	})
	live.snapshots.snapshot = chain.PhaseSnapshot{LastHealthyAt: harnessClock}
	first := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, callerHeaders("caller-a"))
	live.snapshots.snapshot = chain.PhaseSnapshot{
		RequestsBlocked: true, BlockReason: chain.BlockReasonPoC, EpochPhase: chain.EpochPhasePoCGenerate,
		LastHealthyAt: harnessClock.Add(-time.Hour),
	}

	replay := live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, callerHeaders("caller-a"))

	if replay.Code != http.StatusOK || replay.Body.String() != first.Body.String() {
		t.Fatalf("replay during a PoC that went stale: got %d %q, want the cached 200", replay.Code, replay.Body.String())
	}
}

// Test flow:
//  1. Send a chat whose reply finished, and one whose reply stopped mid-answer, from different callers.
//  2. Assert the cache counted one store and one refusal named for why, each on the request's model.
func TestTheCacheCountsWhatItStoredAndWhyItRefused(t *testing.T) {
	live := newHarness(t)

	live.inference.reply = `{"choices":[{"index":0,"message":{"content":"hi"},"finish_reason":"stop"}]}`
	live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, callerHeaders("caller-a"))
	live.inference.reply = `{"choices":[{"index":0,"message":{"content":"half"}}]}`
	live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, callerHeaders("caller-b"))

	live.cached.mu.Lock()
	defer live.cached.mu.Unlock()
	want := []string{"qwen/stored", "qwen/" + filters.CacheRefusedUnfinished}
	if !slices.Equal(live.cached.results, want) {
		t.Fatalf("CacheResult calls = %v, want %v", live.cached.results, want)
	}
}

// Test flow:
//  1. Configure a response cache smaller than the reply a chat will produce.
//  2. Send that chat.
//  3. Assert the cache counts the reply it was offered, refused as too large before any entry was built.
func TestTheCacheCountsAReplyPastItsEntryLimitAsTooLarge(t *testing.T) {
	live := newHarness(t, func(configuration *config.Config) { configuration.Cache.ChatCacheMaxBytes = 64 })
	live.inference.reply = `{"choices":[{"index":0,"message":{"content":"` + strings.Repeat("x", 256) + `"},"finish_reason":"stop"}]}`

	live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, callerHeaders("caller-a"))

	live.cached.mu.Lock()
	defer live.cached.mu.Unlock()
	if want := []string{"qwen/" + cacheRefusedTooLarge}; !slices.Equal(live.cached.results, want) {
		t.Fatalf("CacheResult calls = %v, want %v", live.cached.results, want)
	}
}

// Test flow:
//  1. Send the same chat twice from one caller, spelling the Bearer scheme differently the second time.
//  2. Assert only one race ran: the key, not its spelling, names the caller's cache partition.
func TestOneKeySpelledTwoWaysSharesOneCachePartition(t *testing.T) {
	live := newHarness(t)

	live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, map[string]string{"Authorization": "Bearer caller-a"})
	live.request(t, http.MethodPost, "/v1/chat/completions", chatBody, map[string]string{"Authorization": "bearer  caller-a"})

	if got := live.inference.runs.Load(); got != 1 {
		t.Fatalf("races = %d, want 1: the second spelling of the same key must hit the cache", got)
	}
}

const budgetedReplyBytes = 64 * 1024

// Test flow:
//  1. Give the gateway a response buffer budget far smaller than one streamed reply.
//  2. Stream that reply to a caller twice.
//  3. Assert both reached the caller whole, neither was served from the cache, and nothing is left held: recording a reply for the cache spends the same budget as folding one.
func TestRecordingAStreamedReplyForTheCacheSpendsTheBufferBudget(t *testing.T) {
	live := newHarness(t)
	live.server.buffers = NewBufferBudget(1024)
	chunk := "data: {\"choices\":[{\"delta\":{\"content\":\"" + strings.Repeat("x", budgetedReplyBytes) + "\"},\"finish_reason\":\"stop\"}]}\n\n"
	live.inference.chunks = []string{chunk, "data: [DONE]\n\n"}

	first := live.request(t, http.MethodPost, "/v1/chat/completions", streamChatBody, callerHeaders("caller-a"))
	second := live.request(t, http.MethodPost, "/v1/chat/completions", streamChatBody, callerHeaders("caller-a"))

	if !strings.Contains(first.Body.String(), chunk) || !strings.Contains(second.Body.String(), chunk) {
		t.Fatal("a streamed reply did not reach its caller whole")
	}
	if got := live.inference.runs.Load(); got != 2 {
		t.Fatalf("races: got %d, want 2: a reply the budget could not hold must not be cached", got)
	}
	if held := live.server.buffers.Held(); held != 0 {
		t.Fatalf("buffer budget held %d bytes after both requests, want 0", held)
	}
}

// Test flow:
//  1. Build two cache keys for the same normalized body, one with the empty-answer flag and one without.
//  2. Assert they differ: a reply classified under one flag is never served to a request under the other.
func TestCacheKeySeparatesTheEmptyAnswerFlag(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	normalized := filters.Result{Model: "qwen", Body: []byte(`{"model":"qwen"}`)}
	flagged := normalized
	flagged.EmptyAnswerCountsTokens = true

	if cacheKeyFor(request, normalized) == cacheKeyFor(request, flagged) {
		t.Error("the empty-answer flag does not change the cache key")
	}
}
