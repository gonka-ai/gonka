package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

// The body is already normalized, so it no longer records what the client asked for: the intent must key too.
func chatCacheKey(model string, body []byte, intent clientResponseIntent) string {
	h := sha256.New()
	io.WriteString(h, strings.TrimSpace(model))
	h.Write([]byte{0})
	fmt.Fprintf(h, "%t|%t|%t", intent.keepLogprobs, intent.keepTopLogprobs, intent.keepUsage)
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
	entry.Body = append([]byte(nil), entry.Body...)
	return entry, true
}

// Set stores without re-validating: cacheEntry is the only gate. It reports
// false when the entry does not fit under the byte cap.
func (c *chatResponseCache) Set(key string, entry cachedChatResponse, now time.Time) bool {
	if c == nil || key == "" || len(entry.Body) == 0 || strings.TrimSpace(entry.EscrowID) == "" {
		return false
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
		c.deleteLocked(key)
		return false
	}
	for other := range c.entries {
		if c.totalBytes <= c.maxBytes {
			break
		}
		if other != key {
			c.deleteLocked(other)
		}
	}
	return true
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

// cacheEntry builds a cache entry from the captured response. The returned
// reason is empty when the entry is cacheable and otherwise names why it was
// skipped. The capture buffers every body it sees, including the ones it then
// refuses; a truncated multi-megabyte stream is held and scanned once just to
// be dropped. Deciding incrementally in Write would avoid that.
func (w *gatewayChatCacheCapture) cacheEntry(escrowID string, stream bool, sourceRequestID string, requestErr error) (cachedChatResponse, string) {
	switch {
	case requestErr != nil:
		return cachedChatResponse{}, "request_error"
	case w == nil || w.writeErr != nil:
		return cachedChatResponse{}, "write_error"
	case w.body.Len() == 0:
		return cachedChatResponse{}, "empty_body"
	}
	statusCode := w.statusCode()
	body := w.body.Bytes()
	if reason := cacheableChatResponse(statusCode, body, stream); reason != "" {
		return cachedChatResponse{}, reason
	}
	return cachedChatResponse{
		EscrowID:        escrowID,
		Stream:          stream,
		StatusCode:      statusCode,
		ContentType:     w.Header().Get("Content-Type"),
		Body:            append([]byte(nil), body...),
		SourceRequestID: sourceRequestID,
	}, ""
}

// cacheableResponse reports whether a response is a deterministic outcome
// safe to replay for a duplicate request: any 2xx, or a 400 whose OpenAI-style
// error body is itself deterministic (bad request shape), as opposed to a
// transient failure (rate limit, timeout, capability exhaustion, ...).
// The result is empty when cacheable, otherwise the skip reason.
func cacheableResponse(statusCode int, body []byte) string {
	if len(body) == 0 {
		return "empty_body"
	}
	if responseBodyHasNonCacheableError(body) {
		return "transient_error"
	}
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	if statusCode >= 200 && statusCode < 300 {
		return ""
	}
	if statusCode == http.StatusBadRequest {
		if details, ok := jsonErrorPayloadDetails(body); ok && isCacheableOpenAIErrorDetails(details) {
			return ""
		}
	}
	return "status"
}

// cacheableChatResponse layers semantic completion on cacheableResponse: a 2xx
// chat body replays only when every observed choice carries a terminal reason.
// A deterministic error body with no choices (a host rejection streams as a
// 200 SSE error event) stays cacheable; an error after a partial choice does not.
func cacheableChatResponse(statusCode int, body []byte, stream bool) string {
	if reason := cacheableResponse(statusCode, body); reason != "" {
		return reason
	}
	if statusCode != 0 && (statusCode < 200 || statusCode >= 300) {
		return ""
	}
	var state choiceCompletion
	if stream {
		state = foldStreamCompletion(body)
	} else {
		state.ingest(bytes.TrimSpace(body))
		state.done = true
	}
	return state.reason()
}

type choiceCompletion struct {
	seen     map[string]struct{}
	finished map[string]struct{}
	sawError bool
	done     bool
}

func (s *choiceCompletion) reason() string {
	switch {
	case len(s.seen) == 0 && s.sawError:
		return ""
	case len(s.seen) == 0:
		return "no_choices"
	case !s.done || len(s.finished) != len(s.seen):
		return "incomplete"
	}
	return ""
}

// foldStreamCompletion walks SSE events the way aggregateSSEStreamReader does:
// a blank line ends an event and consecutive data: lines are joined. A `[DONE]`
// before every observed choice is terminal is framing, not completion.
func foldStreamCompletion(body []byte) choiceCompletion {
	var state choiceCompletion
	var event []byte
	ingest := func() bool {
		data := bytes.TrimSpace(event)
		event = event[:0]
		if len(data) == 0 {
			return false
		}
		if bytes.Equal(data, []byte("[DONE]")) {
			state.done = true
			return true
		}
		state.ingest(data)
		return false
	}
	for _, line := range bytes.Split(body, []byte("\n")) {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			if ingest() {
				return state
			}
			continue
		}
		if !bytes.HasPrefix(trimmed, []byte("data:")) {
			continue
		}
		if len(event) > 0 {
			event = append(event, '\n')
		}
		event = append(event, bytes.TrimSpace(trimmed[len("data:"):])...)
	}
	ingest()
	return state
}

// ingest records each choice by index and marks it finished on a terminal
// finish_reason or stop_reason; a finished choice stays finished, as in the
// aggregator. Fields are raw JSON so an oddly typed index does not drop the event.
func (s *choiceCompletion) ingest(payload []byte) {
	if isHostErrorPayload(payload) {
		s.sawError = true
		return
	}
	if !bytes.Contains(payload, []byte(`"choices"`)) {
		return
	}
	if normalized, replaced := replaceNonFiniteNumbers(payload); replaced {
		payload = normalized
	}
	var event struct {
		Choices []struct {
			Index        json.RawMessage `json:"index"`
			FinishReason json.RawMessage `json:"finish_reason"`
			StopReason   json.RawMessage `json:"stop_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return
	}
	if s.seen == nil {
		s.seen = make(map[string]struct{})
		s.finished = make(map[string]struct{})
	}
	for _, choice := range event.Choices {
		index := string(bytes.TrimSpace(choice.Index))
		if index == "" {
			index = "0"
		}
		s.seen[index] = struct{}{}
		if terminalString(choice.FinishReason) || terminalString(choice.StopReason) || jsonNumber(choice.StopReason) {
			s.finished[index] = struct{}{}
		}
	}
}

// finish_reason is terminal only as a non-empty string; stop_reason may also be
// a token id. null, booleans, and empty strings are never terminal.
func terminalString(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 2 && trimmed[0] == '"'
}

func jsonNumber(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && (trimmed[0] == '-' || (trimmed[0] >= '0' && trimmed[0] <= '9'))
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

// isCacheableOpenAIErrorDetails reports whether an OpenAI-style error is a
// deterministic function of the request (safe to replay verbatim for a
// duplicate request) rather than a transient/runtime condition that could
// resolve differently on retry.
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
