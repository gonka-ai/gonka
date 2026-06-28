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

type chatResponseCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]cachedChatResponse
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
		ttl:     ttl,
		entries: make(map[string]cachedChatResponse),
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
	c.entries[key] = entry
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
