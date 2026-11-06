package api

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"devshard/cmd/gateway/filters"
)

const (
	cacheEntryTTL      = time.Hour
	cacheSweepInterval = time.Minute
	cacheEntryOverhead = 256

	expectedStreamEvents = 64
)

// The two digests are held raw: hex would spend an allocation each to say the same thing.
type cacheKey struct {
	caller   [sha256.Size]byte
	body     [sha256.Size]byte
	escrow   string
	model    string
	logprobs filters.LogprobIntent
	stream   bool
	usage    bool

	emptyAnswerCountsTokens bool
}

type cachedResponse struct {
	escrowID    string
	stream      bool
	status      int
	contentType string
	body        []byte
	bounds      []int
	expiresAt   time.Time
}

type responseCache struct {
	mu         sync.Mutex
	ttl        time.Duration
	maxBytes   int64
	entries    map[cacheKey]cachedResponse
	totalBytes int64
	lastSweep  time.Time
	hits       atomic.Int64
	misses     atomic.Int64
}

func (c *responseCache) stats() (hits, misses, entries, byteSize int64) {
	if c == nil {
		return 0, 0, 0, 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits.Load(), c.misses.Load(), int64(len(c.entries)), c.totalBytes
}

// One entry is bounded by what a single reply may hold, not by the whole cache. See README.md.
func (c *responseCache) entryLimit() int64 {
	if c == nil {
		return 0
	}
	return min(c.maxBytes, maxBufferedResponseBytes)
}

func newResponseCache(maxBytes int64) *responseCache {
	if maxBytes <= 0 {
		return nil
	}
	return &responseCache{
		ttl:      cacheEntryTTL,
		maxBytes: maxBytes,
		entries:  make(map[cacheKey]cachedResponse),
	}
}

// The key carries everything that varies the answer, including what the normalised body no longer
// states. See README.md, "The response cache".
func cacheKeyFor(r *http.Request, normalized filters.Result) cacheKey {
	caller, _ := bearerKey(r.Header.Get("Authorization"))
	return cacheKey{
		caller:   sha256.Sum256([]byte(caller)),
		body:     sha256.Sum256(normalized.Body),
		escrow:   r.PathValue("id"),
		model:    strings.TrimSpace(normalized.Model),
		logprobs: normalized.Logprobs,
		stream:   normalized.ClientStream,
		usage:    normalized.ClientUsage,

		emptyAnswerCountsTokens: normalized.EmptyAnswerCountsTokens,
	}
}

func (c *responseCache) get(key cacheKey, now time.Time) (cachedResponse, bool) {
	if c == nil {
		return cachedResponse{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, held := c.entries[key]
	if !held {
		c.misses.Add(1)
		return cachedResponse{}, false
	}
	// Only the error verdict is re-asked: put is the only way in, and it asks the whole question there.
	if !entry.expiresAt.After(now) || filters.HasNonCacheableError(entry.body) {
		c.dropLocked(key)
		c.misses.Add(1)
		return cachedResponse{}, false
	}
	c.hits.Add(1)
	return entry, true
}

// put stores the entry, or names what refused it. See README.md, "The response cache".
func (c *responseCache) put(key cacheKey, entry cachedResponse, now time.Time) string {
	if c == nil {
		// No cache configured, so nothing refused the reply; chat returns before this on that path anyway.
		return filters.CacheStorable
	}
	if refusal := filters.CacheRefusal(entry.status, entry.body); refusal != filters.CacheStorable {
		return refusal
	}
	entry.expiresAt = now.Add(c.ttl)
	size := entrySize(entry)
	if size > c.maxBytes {
		return cacheRefusedTooLarge
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.sweepLocked(now)
	c.dropLocked(key)
	c.entries[key] = entry
	c.totalBytes += size
	c.evictToFitLocked(key)
	return filters.CacheStorable
}

func (c *responseCache) dropLocked(key cacheKey) {
	entry, held := c.entries[key]
	if !held {
		return
	}
	delete(c.entries, key)
	c.totalBytes = max(0, c.totalBytes-entrySize(entry))
}

func (c *responseCache) sweepLocked(now time.Time) {
	if now.Sub(c.lastSweep) < cacheSweepInterval {
		return
	}
	c.lastSweep = now
	for key, entry := range c.entries {
		if !entry.expiresAt.After(now) {
			c.dropLocked(key)
		}
	}
}

func (c *responseCache) evictToFitLocked(keep cacheKey) {
	for key := range c.entries {
		if c.totalBytes <= c.maxBytes {
			return
		}
		if key != keep {
			c.dropLocked(key)
		}
	}
}

func entrySize(entry cachedResponse) int64 {
	return int64(len(entry.body)+len(entry.contentType)+len(entry.escrowID)+8*len(entry.bounds)) + cacheEntryOverhead
}

// Reports what it managed to write, so a cache hit carries the same delivery record as a race.
func serveCached(w http.ResponseWriter, requestID string, entry cachedResponse) (written int64) {
	writeChatHeaders(w.Header(), requestID, entry.escrowID, entry.contentType, entry.stream)
	w.WriteHeader(entry.status)

	controller := http.NewResponseController(w)
	start := 0
	for _, end := range entry.bounds {
		sent, err := w.Write(entry.body[start:end])
		written += int64(sent)
		if err != nil {
			return written
		}
		if entry.stream {
			_ = controller.Flush()
		}
		start = end
	}
	return written
}

// Mirrors what the client receives, bounded per request in flight. See README.md, "The response cache".
type cacheRecorder struct {
	http.ResponseWriter
	limit      int64
	budget     *BufferBudget
	charged    int64
	status     int
	body       bytes.Buffer
	bounds     []int
	writeErr   error
	overflowed bool
}

// A streamed reply is recorded event by event, so its boundaries are worth reserving; a folded one arrives as a single body. The recording spends the shared buffer budget. See README.md, "The response cache".
func newCacheRecorder(w http.ResponseWriter, limit int64, streamed bool, budget *BufferBudget) *cacheRecorder {
	recorder := &cacheRecorder{ResponseWriter: w, limit: limit, budget: budget}
	if streamed {
		recorder.bounds = make([]int, 0, expectedStreamEvents)
	}
	return recorder
}

func (w *cacheRecorder) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *cacheRecorder) Write(chunk []byte) (int, error) {
	written, err := w.ResponseWriter.Write(chunk)
	if err != nil && w.writeErr == nil {
		w.writeErr = err
	}
	if written > 0 && !w.overflowed {
		if int64(w.body.Len()+written) > w.limit || !w.budget.reserve(int64(written)) {
			w.overflow()
		} else {
			w.charged += int64(written)
			w.body.Write(chunk[:written])
			w.bounds = append(w.bounds, w.body.Len())
		}
	}
	return written, err
}

func (w *cacheRecorder) overflow() {
	w.overflowed = true
	w.body = bytes.Buffer{}
	w.bounds = nil
	w.releaseBudget()
}

// releaseBudget hands back what the recording held; the cache accounts for an entry it keeps on its own.
func (w *cacheRecorder) releaseBudget() {
	w.budget.release(w.charged)
	w.charged = 0
}

func (w *cacheRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *cacheRecorder) entry(escrowID string, stream bool, unstorable error) (cachedResponse, bool) {
	if w.cannotBuildEntry(unstorable, escrowID) {
		return cachedResponse{}, false
	}
	return cachedResponse{
		escrowID:    escrowID,
		stream:      stream,
		status:      cmp.Or(w.status, http.StatusOK),
		contentType: w.Header().Get("Content-Type"),
		body:        exactly(w.body.Bytes()),
		bounds:      exactly(w.bounds),
	}, true
}

// cannotBuildEntry reports a recording with nothing an entry can be built from. See README.md, "The response cache".
func (w *cacheRecorder) cannotBuildEntry(unstorable error, escrowID string) bool {
	return w.entryRefusal(unstorable, escrowID) != filters.CacheStorable
}

func (w *cacheRecorder) entryRefusal(unstorable error, escrowID string) string {
	switch {
	case w.overflowed:
		return cacheRefusedTooLarge
	case w.writeErr != nil:
		return cacheRefusedWriteFailed
	case unstorable != nil:
		return cacheRefusedRaceFailed
	case w.body.Len() == 0:
		return filters.CacheRefusedEmptyBody
	case strings.TrimSpace(escrowID) == "":
		return cacheRefusedNoEscrow
	}
	return filters.CacheStorable
}

// exactly copies into a slice with no spare capacity, so a stored entry holds what entrySize charges it.
func exactly[E any](source []E) []E {
	copied := make([]E, len(source))
	copy(copied, source)
	return copied
}
