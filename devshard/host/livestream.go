package host

import (
	"bytes"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// InflightReplayBufferTTL bounds how long a live per-inference SSE buffer may
// stay registered for reconnect attach. Aligned with the gateway streaming
// hard timeout so a long healthy generation remains attachable; prune detaches
// the map entry without Closing an in-flight producer (see pruneLiveStreamLocked).
var InflightReplayBufferTTL = 30 * time.Minute

// LiveStreamMaxRAMBytes soft-caps the in-RAM resume log. Past this, new
// Subscribe/Attach calls fail with ErrLiveStreamOverCap while the producer and
// existing readers continue (primary path must not lose bytes).
var LiveStreamMaxRAMBytes int64 = 32 << 20 // 32 MiB

var (
	// ErrResumeCursorPast is returned when the reconnect cursor is beyond the
	// host's buffered prefix (gateway should escalate).
	ErrResumeCursorPast = errors.New("resume cursor past live buffer")
	// ErrInvalidResumeCursor is returned when delivered_events / delivered_partial
	// are negative (malformed wire input).
	ErrInvalidResumeCursor = errors.New("invalid resume cursor")
	// ErrLiveStreamGone is returned when there is no live buffer to attach to.
	ErrLiveStreamGone = errors.New("live stream unavailable")
	// ErrLiveStreamPruned is Subscribe's own TTL check, for callers holding a
	// *LiveStream directly. The host attach path reports ErrLiveStreamGone
	// instead, because prune drops the map entry without Closing the stream.
	ErrLiveStreamPruned = errors.New("live stream pruned")
	// ErrLiveStreamOverCap is returned when the RAM soft cap is exceeded and
	// new reconnect attaches are refused.
	ErrLiveStreamOverCap = errors.New("live stream over ram cap")
)

// LiveStream is a per-inference append-only byte log with independent readers
// (R6 / R9). The ML producer only appends under a short lock and never performs
// gateway network I/O. Each gateway connection (primary or reconnect) is a
// reader with its own absolute byte offset into the log.
//
// It implements http.ResponseWriter so the inference proxy can write into it.
type LiveStream struct {
	mu         sync.Mutex
	cond       *sync.Cond
	events     [][]byte // complete newline-terminated SSE lines
	forming    []byte   // bytes of the currently incomplete event
	totalBytes int64    // sum(len(events))+len(forming)
	overCap    bool
	subs       []*liveSubscriber
	primary    http.ResponseWriter // header target only; body via subscriber #0
	done       bool
	err        error
	createdAt  time.Time
	header     http.Header
	wroteHdr   bool

	// primaryDone is closed when subscriber #0's drain goroutine exits. The
	// caller that owns the underlying ResponseWriter must wait on it before
	// writing anything else (e.g. devshard_meta), otherwise two goroutines
	// write one response and the trailer can overtake the body tail.
	primaryDone chan struct{}

	// clientDetached is set when the primary gateway writer fails. Surfaced to
	// proxyTextStreamResponse so IncInferenceClientDetachedDrain still fires
	// even though Write to the hub always succeeds.
	clientDetached atomic.Bool
}

// liveSubscriber is an independent reader of the byte log. Payload always comes
// from the log; there is no per-chunk push channel (and no drop-on-full).
//
// evIdx / evAbs are a monotonic cursor hint into events so a reader never
// rescans the log from event 0: each event is visited once over the reader's
// lifetime, making a wakeup cost O(undelivered bytes) instead of O(all events).
type liveSubscriber struct {
	offset    int64 // absolute producer bytes already delivered to this reader
	evIdx     int   // index of the event holding offset (never past the last event)
	evAbs     int64 // absolute start offset of events[evIdx]
	closed    bool
	isPrimary bool
}

func newLiveStream() *LiveStream {
	s := &LiveStream{
		createdAt: time.Now(),
		header:    make(http.Header),
	}
	s.cond = sync.NewCond(&s.mu)
	return s
}

func (s *LiveStream) Header() http.Header {
	if s.header == nil {
		s.header = make(http.Header)
	}
	return s.header
}

func (s *LiveStream) WriteHeader(statusCode int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wroteHdr {
		return
	}
	s.wroteHdr = true
	if s.primary != nil {
		for k, vv := range s.header {
			for _, v := range vv {
				s.primary.Header().Add(k, v)
			}
		}
		s.primary.WriteHeader(statusCode)
	}
}

// Write appends producer bytes to the resume log and wakes readers. Always
// returns success after buffering so a dead / slow gateway writer cannot abort
// ML drain. No network I/O is performed under the lock. Bytes are copied once
// into forming (append) and again when a complete line is sealed into events.
func (s *LiveStream) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return len(p), nil
	}
	s.bufferLocked(p)
	if LiveStreamMaxRAMBytes > 0 && s.totalBytes > LiveStreamMaxRAMBytes {
		s.overCap = true
	}
	s.cond.Broadcast()
	return len(p), nil
}

// Flush is a no-op on the hub: each reader flushes its own ResponseWriter after
// writing. Kept for http.Flusher compatibility with the inference proxy.
func (s *LiveStream) Flush() {}

// ClientDetached reports whether the primary gateway writer has failed.
// Used by the inference proxy to count detached drains.
func (s *LiveStream) ClientDetached() bool {
	return s.clientDetached.Load()
}

// SetPrimary registers the first gateway ResponseWriter as subscriber #0 and
// starts an independent drain goroutine. Headers still use the primary writer
// synchronously via WriteHeader; body bytes are read from the log at the
// reader's pace so a slow primary cannot stall ML.
func (s *LiveStream) SetPrimary(w http.ResponseWriter) {
	if w == nil {
		return
	}
	s.mu.Lock()
	s.primary = w
	sub := &liveSubscriber{offset: 0, isPrimary: true}
	s.subs = append(s.subs, sub)
	primaryDone := make(chan struct{})
	s.primaryDone = primaryDone
	s.mu.Unlock()
	go func() {
		defer close(primaryDone)
		_ = s.drainSubscriber(w, sub)
	}()
}

// WaitPrimary blocks until subscriber #0 has drained the log and released the
// primary ResponseWriter. Call it after Close and before writing any trailing
// bytes to that writer; without it the producer's request goroutine and the
// drain goroutine race on one response.
func (s *LiveStream) WaitPrimary() {
	s.mu.Lock()
	primaryDone := s.primaryDone
	s.mu.Unlock()
	if primaryDone == nil {
		return
	}
	<-primaryDone
}

func (s *LiveStream) bufferLocked(p []byte) {
	s.forming = append(s.forming, p...)
	s.totalBytes += int64(len(p))
	for {
		nl := bytes.IndexByte(s.forming, '\n')
		if nl == -1 {
			return
		}
		event := append([]byte(nil), s.forming[:nl+1]...)
		s.forming = s.forming[nl+1:]
		// Blank SSE separators ("\n" after "data: …\n") are not wire events.
		// Merge them into the previous content event so host event indexes match
		// the gateway cursor (recordDeliveredForward skips blanks) and the
		// cached replaySSEBodyFromCursor event list.
		if isBlankSSELine(event) && len(s.events) > 0 {
			last := len(s.events) - 1
			s.events[last] = append(s.events[last], event...)
			continue
		}
		s.events = append(s.events, event)
	}
}

// isBlankSSELine reports whether line is an SSE separator (whitespace/empty
// between events). line may include a trailing '\n'.
func isBlankSSELine(line []byte) bool {
	line = bytes.TrimSuffix(line, []byte{'\n'})
	line = bytes.TrimSuffix(line, []byte{'\r'})
	return len(bytes.TrimSpace(line)) == 0
}

// Expired reports whether the live buffer should be detached from the attach map.
func (s *LiveStream) Expired(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.expiredLocked(now)
}

func (s *LiveStream) expiredLocked(now time.Time) bool {
	return now.Sub(s.createdAt) > InflightReplayBufferTTL
}

// Close marks the stream finished and wakes all readers.
func (s *LiveStream) Close(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return
	}
	s.done = true
	s.err = err
	s.primary = nil
	s.cond.Broadcast()
}

// EventCount returns complete buffered events (test helper).
func (s *LiveStream) EventCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events)
}

// FormingLen returns the incomplete-event byte length (test helper).
func (s *LiveStream) FormingLen() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.forming)
}

// TotalBytes returns the resume-log size (test helper).
func (s *LiveStream) TotalBytes() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.totalBytes
}

// Subscribe replays from the resume cursor then follows the live tail until
// the stream completes. First post-subscribe byte is emitted without waiting
// for ML/[DONE]. Reader I/O happens outside the lock so a slow reconnect
// client cannot stall the ML producer.
func (s *LiveStream) Subscribe(w http.ResponseWriter, deliveredEvents, deliveredPartial int64) error {
	if err := validateResumeCursor(deliveredEvents, deliveredPartial); err != nil {
		return err
	}

	s.mu.Lock()
	held := true
	defer func() {
		if held {
			s.mu.Unlock()
		}
	}()

	if s.expiredLocked(time.Now()) {
		return ErrLiveStreamPruned
	}
	if s.overCap {
		return ErrLiveStreamOverCap
	}
	offset, err := s.deliveredAbsOffsetLocked(deliveredEvents, deliveredPartial)
	if err != nil {
		return err
	}
	if s.done && offset >= s.totalBytes {
		streamErr := s.err
		s.mu.Unlock()
		held = false
		return streamErr
	}
	sub := &liveSubscriber{offset: offset}
	s.subs = append(s.subs, sub)
	s.mu.Unlock()
	held = false

	return s.drainSubscriber(w, sub)
}

// drainSubscriber reads [sub.offset, totalBytes) from the log at the client's
// pace until the stream completes or the writer fails.
func (s *LiveStream) drainSubscriber(w http.ResponseWriter, sub *liveSubscriber) error {
	for {
		s.mu.Lock()
		for !sub.closed && !s.done && sub.offset >= s.totalBytes {
			s.cond.Wait()
		}
		if sub.closed {
			s.mu.Unlock()
			return nil
		}
		if sub.offset < s.totalBytes {
			chunk := s.copyPendingLocked(sub)
			next := s.totalBytes
			s.mu.Unlock()
			if err := writeReplay(w, chunk); err != nil {
				if sub.isPrimary {
					s.clientDetached.Store(true)
				}
				s.unsubscribe(sub)
				return nil // client gone; producer keeps draining
			}
			s.mu.Lock()
			if !sub.closed && sub.offset < next {
				sub.offset = next
			}
			s.mu.Unlock()
			continue
		}
		// Caught up and stream is done.
		err := s.err
		s.mu.Unlock()
		return err
	}
}

func (s *LiveStream) unsubscribe(sub *liveSubscriber) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub.closed = true
	s.cond.Broadcast()
}

func validateResumeCursor(deliveredEvents, deliveredPartial int64) error {
	if deliveredEvents < 0 || deliveredPartial < 0 {
		return ErrInvalidResumeCursor
	}
	return nil
}

// deliveredAbsOffsetLocked converts the wire (events, partial) cursor into the
// absolute byte offset of the first undelivered byte. Caller must hold s.mu.
//
// Wire delivered_events counts only non-blank SSE lines (gateway
// recordDeliveredForward). Blank separators are skipped when counting and, at
// an event boundary (partial==0), already-forwarded trailing blanks are
// included in the absolute offset so resume does not re-emit them.
func (s *LiveStream) deliveredAbsOffsetLocked(deliveredEvents, deliveredPartial int64) (int64, error) {
	if err := validateResumeCursor(deliveredEvents, deliveredPartial); err != nil {
		return 0, err
	}

	var abs int64
	var contentSeen int64
	i := 0
	for i < len(s.events) && contentSeen < deliveredEvents {
		ev := s.events[i]
		abs += int64(len(ev))
		if !isBlankSSELine(ev) {
			contentSeen++
		}
		i++
	}
	if contentSeen < deliveredEvents {
		return 0, ErrResumeCursorPast
	}

	// At an event boundary the gateway has also consumed blank separators after
	// the last content line; advance past any that remain as solo entries
	// (defensive — bufferLocked normally merges them into the prior event).
	if deliveredPartial == 0 {
		for i < len(s.events) && isBlankSSELine(s.events[i]) {
			abs += int64(len(s.events[i]))
			i++
		}
		return abs, nil
	}

	for i < len(s.events) && isBlankSSELine(s.events[i]) {
		abs += int64(len(s.events[i]))
		i++
	}
	if i < len(s.events) {
		evLen := int64(len(s.events[i]))
		if deliveredPartial > evLen {
			return 0, ErrResumeCursorPast
		}
		return abs + deliveredPartial, nil
	}
	formingLen := int64(len(s.forming))
	if deliveredPartial > formingLen {
		return 0, ErrResumeCursorPast
	}
	return abs + deliveredPartial, nil
}

// copyPendingLocked copies [sub.offset, totalBytes) out of the log and advances
// sub's cursor hint. Caller must hold s.mu; the returned slice is safe after
// unlock. Runs under the producer's lock, so it must not walk already-delivered
// events: the hint keeps the scan proportional to the undelivered tail.
func (s *LiveStream) copyPendingLocked(sub *liveSubscriber) []byte {
	if sub.offset >= s.totalBytes {
		return nil
	}
	// The final event can still grow — bufferLocked merges blank SSE separators
	// into it — so the hint stops there and re-reads its tail next wakeup.
	for sub.evIdx+1 < len(s.events) {
		evEnd := sub.evAbs + int64(len(s.events[sub.evIdx]))
		if evEnd > sub.offset {
			break
		}
		sub.evAbs = evEnd
		sub.evIdx++
	}

	out := make([]byte, 0, s.totalBytes-sub.offset)
	abs := sub.evAbs
	for i := sub.evIdx; i < len(s.events); i++ {
		ev := s.events[i]
		if abs+int64(len(ev)) > sub.offset {
			start := int64(0)
			if sub.offset > abs {
				start = sub.offset - abs
			}
			out = append(out, ev[start:]...)
		}
		abs += int64(len(ev))
	}
	// abs is now the end of the events region; forming holds the tail.
	if formingLen := int64(len(s.forming)); formingLen > 0 {
		start := sub.offset - abs
		if start < 0 {
			start = 0
		}
		if start < formingLen {
			out = append(out, s.forming[start:]...)
		}
	}
	return out
}

func writeReplay(w http.ResponseWriter, replay []byte) error {
	if len(replay) == 0 {
		return nil
	}
	if _, err := w.Write(replay); err != nil {
		return err
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}
