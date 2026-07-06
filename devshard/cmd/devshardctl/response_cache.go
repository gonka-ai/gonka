package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

const chatResponseCacheTTL = time.Hour

// defaultChatResponseCacheMaxEntries bounds how many cached chat responses the
// gateway keeps in memory at once. The cache is a single process-wide dedup
// layer keyed by the full request body, so under a normal chat workload
// (mostly unique bodies) it would otherwise grow by one full response body per
// distinct request and never shrink: an expired entry is only reclaimed when
// the *same* key is looked up again, which for unique bodies never happens.
// Capping the entry count keeps the footprint bounded regardless of traffic or
// uptime, matching the bounded-map idiom used by rateLimiter and gossip dedup.
const defaultChatResponseCacheMaxEntries = 2048

type chatResponseCache struct {
	mu         sync.Mutex
	ttl        time.Duration
	maxEntries int
	entries    map[string]cachedChatResponse
}

type cachedChatResponse struct {
	EscrowID        string
	Stream          bool
	StatusCode      int
	ContentType     string
	Body            []byte
	SourceRequestID string
	ExpiresAt       time.Time
}

func newChatResponseCache(ttl time.Duration) *chatResponseCache {
	if ttl <= 0 {
		ttl = chatResponseCacheTTL
	}
	return &chatResponseCache{
		ttl:        ttl,
		maxEntries: defaultChatResponseCacheMaxEntries,
		entries:    make(map[string]cachedChatResponse),
	}
}

func chatCacheKey(model string, body []byte) string {
	h := sha256.New()
	io.WriteString(h, strings.TrimSpace(model))
	h.Write([]byte{0})
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

func (c *chatResponseCache) Get(key string, now time.Time) (cachedChatResponse, bool) {
	if c == nil {
		return cachedChatResponse{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return cachedChatResponse{}, false
	}
	if !entry.ExpiresAt.After(now) {
		delete(c.entries, key)
		return cachedChatResponse{}, false
	}
	if responseBodyHasRetriableCapabilityError(entry.Body) {
		delete(c.entries, key)
		return cachedChatResponse{}, false
	}
	entry.Body = append([]byte(nil), entry.Body...)
	return entry, true
}

func (c *chatResponseCache) Set(key string, entry cachedChatResponse, now time.Time) {
	if c == nil || key == "" || len(entry.Body) == 0 || strings.TrimSpace(entry.EscrowID) == "" {
		return
	}
	if responseBodyHasRetriableCapabilityError(entry.Body) {
		return
	}
	if entry.ExpiresAt.IsZero() {
		entry.ExpiresAt = now.Add(c.ttl)
	}
	entry.Body = append([]byte(nil), entry.Body...)

	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[key]; !exists && c.maxEntries > 0 && len(c.entries) >= c.maxEntries {
		c.evictLocked(now)
	}
	c.entries[key] = entry
}

// evictLocked bounds the cache. It first reclaims every expired entry (the
// common case for a unique-body workload) and, only if the cache is still at
// capacity with live entries, drops the oldest half in a single pass. Batching
// amortizes the O(n) sweep across ~maxEntries/2 inserts instead of running it
// on every Set once the cache is hot. Callers must hold c.mu. On return the
// cache holds fewer than maxEntries entries, leaving room for the insert.
func (c *chatResponseCache) evictLocked(now time.Time) {
	for key, entry := range c.entries {
		if !entry.ExpiresAt.After(now) {
			delete(c.entries, key)
		}
	}
	if len(c.entries) < c.maxEntries {
		return
	}
	// ExpiresAt is insertion time plus a constant TTL, so ordering by ExpiresAt
	// is insertion order. Keep the newest maxEntries/2 entries, drop the rest.
	type keyExpiry struct {
		key     string
		expires time.Time
	}
	live := make([]keyExpiry, 0, len(c.entries))
	for key, entry := range c.entries {
		live = append(live, keyExpiry{key: key, expires: entry.ExpiresAt})
	}
	sort.Slice(live, func(i, j int) bool { return live[i].expires.Before(live[j].expires) })
	for i := 0; i < len(live)-c.maxEntries/2; i++ {
		delete(c.entries, live[i].key)
	}
}

func serveCachedChatResponse(w http.ResponseWriter, r *http.Request, entry cachedChatResponse) {
	if rid, ok := requestLogFromContext(r.Context()); ok {
		w.Header().Set("X-Request-Id", rid)
	}
	w.Header().Set("X-Devshard-ID", entry.EscrowID)
	if entry.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
	} else if entry.ContentType != "" {
		w.Header().Set("Content-Type", entry.ContentType)
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	statusCode := entry.StatusCode
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	w.WriteHeader(statusCode)
	_, _ = w.Write(entry.Body)
	if entry.Stream {
		_ = flushResponseWriter(w)
	}
}

type gatewayChatCacheCapture struct {
	http.ResponseWriter
	status   int
	body     bytes.Buffer
	writeErr error
}

func (w *gatewayChatCacheCapture) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
		w.ResponseWriter.WriteHeader(status)
	}
}

func (w *gatewayChatCacheCapture) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	if err != nil && w.writeErr == nil {
		w.writeErr = err
	}
	if n > 0 {
		w.body.Write(p[:n])
	}
	return n, err
}

func (w *gatewayChatCacheCapture) Flush() {
	_ = flushResponseWriter(w.ResponseWriter)
}

func (w *gatewayChatCacheCapture) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *gatewayChatCacheCapture) statusCode() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func (w *gatewayChatCacheCapture) cacheEntry(escrowID string, stream bool, sourceRequestID string) (cachedChatResponse, bool) {
	if w == nil || w.writeErr != nil || w.body.Len() == 0 {
		return cachedChatResponse{}, false
	}
	statusCode := w.statusCode()
	if statusCode < 200 {
		return cachedChatResponse{}, false
	}
	if responseBodyHasRetriableCapabilityError(w.body.Bytes()) {
		return cachedChatResponse{}, false
	}
	return cachedChatResponse{
		EscrowID:        escrowID,
		Stream:          stream,
		StatusCode:      statusCode,
		ContentType:     w.Header().Get("Content-Type"),
		Body:            append([]byte(nil), w.body.Bytes()...),
		SourceRequestID: sourceRequestID,
	}, true
}

func responseBodyHasRetriableCapabilityError(body []byte) bool {
	if details, ok := sseChunkErrorDetails(body); ok {
		return isRetriableCapabilityErrorMessage(details.Message)
	}
	if details, ok := jsonErrorPayloadDetails(body); ok {
		return isRetriableCapabilityErrorMessage(details.Message)
	}
	return false
}
