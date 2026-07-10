package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const chatResponseCacheTTL = time.Hour

// chatCacheSweepInterval bounds how often Set pays for a full expiry sweep.
// Expiry used to be enforced only lazily inside Get for the exact key being
// looked up; since keys are hashes of full request bodies, unique requests
// were never looked up again and their entries lived until process restart.
const chatCacheSweepInterval = time.Minute

// defaultChatCacheMaxBytes caps the total body bytes held by the cache
// (overridable via DEVSHARD_CHAT_CACHE_MAX_BYTES). The cap is a safety net
// against traffic bursts within the TTL window; the sweep handles steady
// state.
const defaultChatCacheMaxBytes = int64(256 << 20)

// chatCacheEntryOverhead approximates the per-entry cost beyond the body:
// map bucket, key string (64-hex sha256), and struct fields.
const chatCacheEntryOverhead = 256

type chatResponseCache struct {
	mu         sync.Mutex
	ttl        time.Duration
	maxBytes   int64
	entries    map[string]cachedChatResponse
	totalBytes int64
	lastSweep  time.Time
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

func newChatResponseCache(ttl time.Duration, maxBytes int64) *chatResponseCache {
	if ttl <= 0 {
		ttl = chatResponseCacheTTL
	}
	if maxBytes <= 0 {
		maxBytes = defaultChatCacheMaxBytes
	}
	return &chatResponseCache{
		ttl:      ttl,
		maxBytes: maxBytes,
		entries:  make(map[string]cachedChatResponse),
	}
}

func chatCacheEntrySize(entry cachedChatResponse) int64 {
	return int64(len(entry.Body)+len(entry.ContentType)+len(entry.EscrowID)+len(entry.SourceRequestID)) + chatCacheEntryOverhead
}

// deleteLocked removes key from the map and adjusts the byte total.
// Caller must hold c.mu.
func (c *chatResponseCache) deleteLocked(key string) {
	entry, ok := c.entries[key]
	if !ok {
		return
	}
	delete(c.entries, key)
	c.totalBytes -= chatCacheEntrySize(entry)
	if c.totalBytes < 0 {
		// Entries written directly in tests bypass accounting.
		c.totalBytes = 0
	}
}

// sweepExpiredLocked scans the whole map and drops entries whose TTL has
// passed, at most once per chatCacheSweepInterval. Caller must hold c.mu.
func (c *chatResponseCache) sweepExpiredLocked(now time.Time) {
	if now.Sub(c.lastSweep) < chatCacheSweepInterval {
		return
	}
	c.lastSweep = now
	for key, entry := range c.entries {
		if !entry.ExpiresAt.After(now) {
			c.deleteLocked(key)
		}
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
		c.deleteLocked(key)
		return cachedChatResponse{}, false
	}
	if responseBodyHasNonCacheableError(entry.Body) {
		c.deleteLocked(key)
		return cachedChatResponse{}, false
	}
	entry.Body = append([]byte(nil), entry.Body...)
	return entry, true
}

func (c *chatResponseCache) Set(key string, entry cachedChatResponse, now time.Time) {
	if c == nil || key == "" || len(entry.Body) == 0 || strings.TrimSpace(entry.EscrowID) == "" {
		return
	}
	if !cacheableResponse(entry.StatusCode, entry.Body) {
		return
	}
	if entry.ExpiresAt.IsZero() {
		entry.ExpiresAt = now.Add(c.ttl)
	}
	entry.Body = append([]byte(nil), entry.Body...)

	c.mu.Lock()
	defer c.mu.Unlock()
	c.sweepExpiredLocked(now)

	c.deleteLocked(key) // drop any previous version's byte count
	c.entries[key] = entry
	c.totalBytes += chatCacheEntrySize(entry)

	// Size cap: evict arbitrary entries (map iteration order) until under
	// the limit. This is a dedup cache -- evicting a "wrong" entry only
	// costs one cache miss, so eviction order isn't worth tracking.
	if chatCacheEntrySize(entry) > c.maxBytes {
		// Entry is too large to fit under the cap; don't cache it.
		c.deleteLocked(key)
		return
	}
	for other := range c.entries {
		if c.totalBytes <= c.maxBytes {
			break
		}
		if other != key {
			c.deleteLocked(other)
		}
	}
}

// Stats reports the current entry count and approximate retained bytes.
func (c *chatResponseCache) Stats() (entryCount int, totalBytes int64) {
	if c == nil {
		return 0, 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries), c.totalBytes
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

func (w *gatewayChatCacheCapture) cacheEntry(escrowID string, stream bool, sourceRequestID string, requestErr error) (cachedChatResponse, bool) {
	if w == nil || w.writeErr != nil || w.body.Len() == 0 {
		return cachedChatResponse{}, false
	}
	if requestErr != nil {
		return cachedChatResponse{}, false
	}
	statusCode := w.statusCode()
	body := w.body.Bytes()
	if !cacheableResponse(statusCode, body) {
		return cachedChatResponse{}, false
	}
	return cachedChatResponse{
		EscrowID:        escrowID,
		Stream:          stream,
		StatusCode:      statusCode,
		ContentType:     w.Header().Get("Content-Type"),
		Body:            append([]byte(nil), body...),
		SourceRequestID: sourceRequestID,
	}, true
}

func cacheableResponse(statusCode int, body []byte) bool {
	if len(body) == 0 || responseBodyHasNonCacheableError(body) {
		return false
	}
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	if statusCode >= 200 && statusCode < 300 {
		return true
	}
	if statusCode == http.StatusBadRequest {
		details, ok := jsonErrorPayloadDetails(body)
		return ok && isCacheableOpenAIErrorDetails(details)
	}
	return false
}

func responseBodyHasNonCacheableError(body []byte) bool {
	if details, ok := sseChunkErrorDetails(body); ok {
		return !isCacheableOpenAIErrorDetails(details)
	}
	if details, ok := jsonErrorPayloadDetails(body); ok {
		return !isCacheableOpenAIErrorDetails(details)
	}
	return false
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

func isCacheableOpenAIErrorDetails(details sseErrorDetails) bool {
	msg := strings.ToLower(details.Message)
	typ := strings.ToLower(details.Type)
	code := strings.ToLower(details.Code)
	if strings.TrimSpace(msg) == "" {
		return false
	}
	if isRetriableCapabilityErrorMessage(details.Message) {
		return false
	}
	for _, marker := range []string{
		"context canceled",
		"context cancelled",
		"client disconnected",
		"request canceled",
		"request cancelled",
		"timeout",
		"timed out",
		"rate limit",
		"overloaded",
		"temporarily unavailable",
		"service unavailable",
		"internal server error",
		"unsupported model",
		"model not found",
		"model_not_found",
		"does not exist",
		"not supported on this model",
	} {
		if strings.Contains(msg, marker) || strings.Contains(typ, marker) || strings.Contains(code, marker) {
			return false
		}
	}
	return true
}
