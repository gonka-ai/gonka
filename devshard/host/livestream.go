package host

import (
	"bytes"
	"errors"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"devshard/types"
)

func init() {
	// Citest: force a primary-writer detach after N successful drain
	// writes so the gateway sees a mid-stream drop while the producer keeps
	// draining ML. Zero (default) disables the hook.
	if v := os.Getenv("DEVSHARD_TEST_DETACH_PRIMARY_AFTER_WRITES"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			TestDetachPrimaryAfterWrites.Store(n)
		}
	}
}

// TestDetachPrimaryAfterWrites, when > 0, makes each LiveStream's primary
// drain fail after that many successful writeReplay calls (ClientDetached).
// Used by reconnect e2e to inject a gateway↔host drop without killing ML.
var TestDetachPrimaryAfterWrites atomic.Int64

// InflightReplayBufferTTL bounds how long a live per-inference SSE buffer may
// stay registered for reconnect attach. Derived from protocol ExecutionTimeout
// (same budget as host drain and gateway StreamingAttemptHardTimeout); prune
// detaches the map entry without Closing an in-flight producer
// (see pruneLiveStreamLocked).
var InflightReplayBufferTTL = types.DefaultExecutionTimeout()

// LiveStreamMaxRAMBytes soft-caps the in-RAM resume log. Past this, head-trim
// drops bytes already consumed by every live reader, and new Subscribe/Attach
// calls fail with ErrLiveStreamOverCap while retained size stays over the cap.
var LiveStreamMaxRAMBytes int64 = 32 << 20 // 32 MiB

// LiveStreamMaxFormingBytes caps a single incomplete SSE line. Upstream lines
// are already bounded by the inference proxy scanner (~1 MiB); this is a
// defensive bound for non-line writers.
var LiveStreamMaxFormingBytes int64 = 1 << 20 // 1 MiB

// LiveStreamReaderStallTimeout is how long a non-primary subscriber may sit
// with undelivered bytes and a frozen offset before Subscribe returns
// ErrSubscriberLagged. Must exceed AttemptReconnectBudget; sized like the
// gateway InterChunkStallTimeout, not the 1s ladder budget.
var LiveStreamReaderStallTimeout = 60 * time.Second

// LiveStreamPrimaryWriteTimeout is the per-Write deadline for every live
// reader (primary and reconnect). Breach is treated as a write error: drop
// the reader, keep producing. For the primary this surfaces as ClientDetached.
var LiveStreamPrimaryWriteTimeout = 30 * time.Second

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
	// ErrSubscriberLagged is returned when a non-primary reader stops advancing
	// while bytes are pending. The generation stays available for other attaches.
	ErrSubscriberLagged = errors.New("live stream subscriber lagged")
	// ErrLiveStreamFormingOversize is returned when a single incomplete line
	// exceeds LiveStreamMaxFormingBytes.
	ErrLiveStreamFormingOversize = errors.New("live stream forming line oversize")
)

// LiveStream is a per-inference append-only byte log with independent readers.
// The ML producer only appends under a short lock and never performs
// gateway network I/O. Each gateway connection (primary or reconnect) is a
// reader with its own absolute byte offset into the log.
//
// It implements http.ResponseWriter so the inference proxy can write into it.
type LiveStream struct {
	mu         sync.Mutex
	cond       *sync.Cond
	events     [][]byte // complete newline-terminated SSE lines (retained window)
	forming    []byte   // bytes of the currently incomplete event
	totalBytes int64    // absolute tip: bytesBase + retained
	bytesBase  int64    // absolute byte offset of events[0]
	eventsBase int64    // wire content-event count trimmed before events[0]
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

	// testPrimaryWrites counts successful primary writeReplay calls when
	// TestDetachPrimaryAfterWrites is set (citest reconnect fault injection).
	testPrimaryWrites int64
}

// liveSubscriber is an independent reader of the byte log. Payload always comes
// from the log; there is no per-chunk push channel (and no drop-on-full).
//
// evIdx / evAbs are a monotonic cursor hint into events so a reader never
// rescans the log from event 0: each event is visited once over the reader's
// lifetime, making a wakeup cost O(undelivered bytes) instead of O(all events).
type liveSubscriber struct {
	offset         int64 // absolute producer bytes already delivered to this reader
	evIdx          int   // index into events holding offset (never past last event)
	evAbs          int64 // absolute start offset of events[evIdx]
	closed         bool
	isPrimary      bool
	lastProgressAt time.Time // last successful offset advance; not refreshed by Append
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
	if LiveStreamMaxFormingBytes > 0 && int64(len(s.forming)) > LiveStreamMaxFormingBytes {
		s.done = true
		s.err = ErrLiveStreamFormingOversize
		s.primary = nil
		s.cond.Broadcast()
		return len(p), nil
	}
	s.trimHeadLocked()
	s.recomputeOverCapLocked()
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
	now := time.Now()
	sub := &liveSubscriber{offset: 0, evAbs: s.bytesBase, isPrimary: true, lastProgressAt: now}
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

func (s *LiveStream) retainedBytesLocked() int64 {
	return s.totalBytes - s.bytesBase
}

func (s *LiveStream) recomputeOverCapLocked() {
	if LiveStreamMaxRAMBytes <= 0 {
		s.overCap = false
		return
	}
	s.overCap = s.retainedBytesLocked() > LiveStreamMaxRAMBytes
}

// trimHeadLocked drops whole events already consumed by every live reader
// until retained size is within LiveStreamMaxRAMBytes (or no further safe
// trim is possible). Caller must hold s.mu.
func (s *LiveStream) trimHeadLocked() {
	if LiveStreamMaxRAMBytes <= 0 || s.retainedBytesLocked() <= LiveStreamMaxRAMBytes {
		return
	}
	minOff := s.minLiveReaderOffsetLocked()
	for len(s.events) > 0 && s.retainedBytesLocked() > LiveStreamMaxRAMBytes {
		ev := s.events[0]
		oldBase := s.bytesBase
		evEnd := oldBase + int64(len(ev))
		if evEnd > minOff {
			break
		}
		if !isBlankSSELine(ev) {
			s.eventsBase++
		}
		s.bytesBase = evEnd
		s.events = s.events[1:]
		for _, sub := range s.subs {
			if sub.closed {
				continue
			}
			// Absolute tip of the hint is unchanged for later events; only the
			// slice index shifts. Hints that pointed at the removed event reseed.
			if sub.evIdx > 0 && sub.evAbs > oldBase {
				sub.evIdx--
			} else {
				s.reseedSubscriberHintLocked(sub)
			}
		}
	}
}

func (s *LiveStream) minLiveReaderOffsetLocked() int64 {
	minOff := s.totalBytes
	any := false
	for _, sub := range s.subs {
		if sub.closed {
			continue
		}
		any = true
		if sub.offset < minOff {
			minOff = sub.offset
		}
	}
	if !any {
		// No live readers: trim freely (whole events) until under the RAM cap.
		return s.totalBytes
	}
	return minOff
}

func (s *LiveStream) reseedSubscriberHintLocked(sub *liveSubscriber) {
	sub.evIdx = 0
	sub.evAbs = s.bytesBase
	if sub.offset < s.bytesBase {
		return
	}
	for sub.evIdx+1 < len(s.events) {
		evEnd := sub.evAbs + int64(len(s.events[sub.evIdx]))
		if evEnd > sub.offset {
			break
		}
		sub.evAbs = evEnd
		sub.evIdx++
	}
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

// TotalBytes returns the absolute tip of the resume log (test helper).
func (s *LiveStream) TotalBytes() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.totalBytes
}

// RetainedBytes returns hot-RAM size of the resume window (test helper).
func (s *LiveStream) RetainedBytes() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.retainedBytesLocked()
}

// BytesBase returns the absolute offset of the first retained event (test helper).
func (s *LiveStream) BytesBase() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bytesBase
}

// OverCap reports whether new attaches are refused (test helper).
func (s *LiveStream) OverCap() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.overCap
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
	if s.err != nil && errors.Is(s.err, ErrLiveStreamFormingOversize) {
		return ErrLiveStreamFormingOversize
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
	sub := &liveSubscriber{
		offset:         offset,
		lastProgressAt: time.Now(),
	}
	s.reseedSubscriberHintLocked(sub)
	s.subs = append(s.subs, sub)
	s.mu.Unlock()
	held = false

	return s.drainSubscriber(w, sub)
}

// drainSubscriber reads [sub.offset, totalBytes) from the log at the client's
// pace until the stream completes or the writer fails / stalls.
func (s *LiveStream) drainSubscriber(w http.ResponseWriter, sub *liveSubscriber) error {
	for {
		s.mu.Lock()
		for !sub.closed && !s.done && sub.offset >= s.totalBytes {
			s.cond.Wait()
			// Stall clock starts when undelivered data appears — not while idle.
			sub.lastProgressAt = time.Now()
		}
		if sub.closed {
			s.mu.Unlock()
			return nil
		}
		if sub.offset < s.totalBytes {
			if !sub.isPrimary && LiveStreamReaderStallTimeout > 0 {
				if time.Since(sub.lastProgressAt) >= LiveStreamReaderStallTimeout {
					s.mu.Unlock()
					s.unsubscribe(sub)
					return ErrSubscriberLagged
				}
			}
			chunk := s.copyPendingLocked(sub)
			next := s.totalBytes
			lastProgress := sub.lastProgressAt
			isPrimary := sub.isPrimary
			s.mu.Unlock()
			if err := writeReplay(w, chunk, lastProgress, isPrimary); err != nil {
				if isPrimary {
					s.clientDetached.Store(true)
					s.unsubscribe(sub)
					return nil // client gone; producer keeps draining
				}
				s.unsubscribe(sub)
				return ErrSubscriberLagged
			}
			s.mu.Lock()
			if !sub.closed && sub.offset < next {
				sub.offset = next
				sub.lastProgressAt = time.Now()
				s.trimHeadLocked()
				s.recomputeOverCapLocked()
			}
			s.mu.Unlock()
			if isPrimary {
				if limit := TestDetachPrimaryAfterWrites.Load(); limit > 0 {
					n := atomic.AddInt64(&s.testPrimaryWrites, 1)
					if n >= limit {
						// Bytes above already counted as delivered. Tear down
						// the TCP so the gateway sees a mid-stream drop and
						// can same-nonce reconnect to this still-live log.
						s.clientDetached.Store(true)
						s.unsubscribe(sub)
						if hj, ok := w.(http.Hijacker); ok {
							if conn, _, err := hj.Hijack(); err == nil {
								_ = conn.Close()
							}
						}
						return nil
					}
				}
			}
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
	s.trimHeadLocked()
	s.recomputeOverCapLocked()
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
	if deliveredEvents < s.eventsBase {
		return 0, ErrResumeCursorPast
	}

	abs := s.bytesBase
	contentSeen := s.eventsBase
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
		if abs < s.bytesBase {
			return 0, ErrResumeCursorPast
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
		out := abs + deliveredPartial
		if out < s.bytesBase {
			return 0, ErrResumeCursorPast
		}
		return out, nil
	}
	formingLen := int64(len(s.forming))
	if deliveredPartial > formingLen {
		return 0, ErrResumeCursorPast
	}
	out := abs + deliveredPartial
	if out < s.bytesBase {
		return 0, ErrResumeCursorPast
	}
	return out, nil
}

// copyPendingLocked copies [sub.offset, totalBytes) out of the log and advances
// sub's cursor hint. Caller must hold s.mu; the returned slice is safe after
// unlock. Runs under the producer's lock, so it must not walk already-delivered
// events: the hint keeps the scan proportional to the undelivered tail.
func (s *LiveStream) copyPendingLocked(sub *liveSubscriber) []byte {
	if sub.offset >= s.totalBytes {
		return nil
	}
	if sub.offset < s.bytesBase {
		// Should not happen for live readers; trim never crosses min offset.
		sub.offset = s.bytesBase
		s.reseedSubscriberHintLocked(sub)
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

func writeReplay(w http.ResponseWriter, replay []byte, lastProgressAt time.Time, isPrimary bool) error {
	if len(replay) == 0 {
		return nil
	}
	deadline := time.Time{}
	if LiveStreamPrimaryWriteTimeout > 0 {
		deadline = time.Now().Add(LiveStreamPrimaryWriteTimeout)
	}
	if !isPrimary && LiveStreamReaderStallTimeout > 0 && !lastProgressAt.IsZero() {
		stallAt := lastProgressAt.Add(LiveStreamReaderStallTimeout)
		if deadline.IsZero() || stallAt.Before(deadline) {
			deadline = stallAt
		}
	}
	if !deadline.IsZero() {
		if rc := http.NewResponseController(w); rc != nil {
			_ = rc.SetWriteDeadline(deadline)
		}
	}

	// Race Write against the deadline so test writers (and stacks that ignore
	// ResponseController deadlines) still surface stalls. A late Write result
	// after timeout is ignored; the reader has already been dropped.
	type writeResult struct{ err error }
	ch := make(chan writeResult, 1)
	go func() {
		_, err := w.Write(replay)
		if err == nil {
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		ch <- writeResult{err: err}
	}()
	if deadline.IsZero() {
		return (<-ch).err
	}
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case r := <-ch:
		return r.err
	case <-timer.C:
		return errors.New("live stream write deadline exceeded")
	}
}
