package host

import (
	"bytes"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"devshard/logging"
	"devshard/types"
)

// InflightReplayBufferTTL bounds how long a live per-inference SSE buffer may
// stay registered for reconnect attach. Derived from protocol ExecutionTimeout
// (same budget as host drain and gateway StreamingAttemptHardTimeout); prune
// detaches the map entry without Closing an in-flight producer
// (see pruneLiveStreamLocked).
var InflightReplayBufferTTL = types.DefaultExecutionTimeout()

// LiveStreamRingBytes is the trailing window of producer bytes kept in RAM.
// It is a cache, not the resume horizon: everything below it is served from the
// spool. Size it for "one write stall of the fastest reader", not for a whole
// generation — a reader that falls out of the window simply reads from disk.
var LiveStreamRingBytes int64 = 256 << 10 // 256 KiB

// LiveStreamMaxRAMBytes is the hard ceiling on the retained window, reachable
// only when the spool is unavailable or has failed. In that degraded mode the
// window cannot be reclaimed from disk, so readers below the trim point are
// dropped to enforce the ceiling rather than growing RAM without bound.
var LiveStreamMaxRAMBytes int64 = 4 << 20 // 4 MiB

// LiveStreamWriteChunkBytes caps one subscriber write. A reconnect that is far
// behind must not turn its backlog into a single multi-MiB write racing one
// deadline: chunking keeps each write short and lets the progress clock advance
// per chunk, so a healthy-but-slow link is never mistaken for a stalled one.
var LiveStreamWriteChunkBytes int64 = 256 << 10 // 256 KiB

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
	// ErrLiveStreamResumeUnavailable is returned when this generation has no
	// usable spool, so there is no resume tier to attach to. The generation
	// still completes and persists; a later reconnect is served from the
	// payload store instead.
	ErrLiveStreamResumeUnavailable = errors.New("live stream resume unavailable")
	// ErrSubscriberLagged is returned when a reader stops advancing while bytes
	// are pending, or when a degraded (spool-less) stream had to drop it to
	// respect the RAM ceiling. The generation stays available for other attaches.
	ErrSubscriberLagged = errors.New("live stream subscriber lagged")
	// ErrLiveStreamFormingOversize is returned when a single incomplete line
	// exceeds LiveStreamMaxFormingBytes.
	ErrLiveStreamFormingOversize = errors.New("live stream forming line oversize")
)

// liveStreamLimits is the tunable set a generation runs with. It is captured
// once at construction: an operator retuning a knob must not resize the window
// (or the deadlines) of a stream that is already draining.
type liveStreamLimits struct {
	ringBytes    int64
	maxRAMBytes  int64
	chunkBytes   int64
	formingBytes int64
	stallTimeout time.Duration
	writeTimeout time.Duration
}

func currentLiveStreamLimits() liveStreamLimits {
	return liveStreamLimits{
		ringBytes:    LiveStreamRingBytes,
		maxRAMBytes:  LiveStreamMaxRAMBytes,
		chunkBytes:   LiveStreamWriteChunkBytes,
		formingBytes: LiveStreamMaxFormingBytes,
		stallTimeout: LiveStreamReaderStallTimeout,
		writeTimeout: LiveStreamPrimaryWriteTimeout,
	}
}

// LiveStream is a per-inference append-only byte log with independent readers.
// The ML producer only appends under a short lock and never performs
// gateway network I/O. Each gateway connection (primary or reconnect) is a
// reader with its own absolute byte offset into the log.
//
// Storage is two-tier and the tiers are not a mode switch: the spool file is
// the log, and `events` / `forming` are a bounded RAM cache of its tail. A
// reader is served from RAM when its offset is at or past bytesBase and from
// the spool otherwise, re-evaluated on every iteration. That is what makes hot
// RAM independent of reader behaviour: a reader that stops consuming falls out
// of the window instead of pinning it.
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
	subs       []*liveSubscriber
	primary    http.ResponseWriter // header target only; body via subscriber #0
	done       bool
	err        error
	createdAt  time.Time
	header     http.Header
	wroteHdr   bool
	// hdrInFlight is set while WriteHeader is doing primary network I/O
	// outside the lock. Subscriber #0 must not write body bytes to the same
	// ResponseWriter during that window.
	hdrInFlight bool
	lim         liveStreamLimits

	// spool is the disk tier. spoolOffset is the absolute offset up to which
	// complete events have been written to it, and is the only barrier head-trim
	// respects: RAM may drop anything the spool already holds, regardless of
	// where readers are. spoolEvents mirrors the .idx entry count so a cursor
	// older than the RAM window can be resolved without touching the file.
	spool         *streamSpool
	spoolOffset   int64
	spoolEvents   int64
	spoolErr      error // non-nil disables resume for this generation
	spoolPumpDone chan struct{}
	released      bool // Release called; free the spool once readers are done

	// primaryDone is closed when subscriber #0's drain goroutine exits. The
	// caller that owns the underlying ResponseWriter must wait on it before
	// writing anything else (e.g. devshard_meta), otherwise two goroutines
	// write one response and the trailer can overtake the body tail.
	primaryDone chan struct{}

	// clientDetached is set when the primary gateway writer fails. Surfaced to
	// proxyTextStreamResponse so IncInferenceClientDetachedDrain still fires
	// even though Write to the hub always succeeds.
	clientDetached atomic.Bool

	// testPrimaryWrites counts successful primary writeReplay calls. Only the
	// testenvci fault injector reads it; unused in production builds.
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
	dropErr        error // why the producer side closed this reader, if it did
	isPrimary      bool
	lastProgressAt time.Time // last successful offset advance; not refreshed by Append
}

func newLiveStream() *LiveStream {
	s := &LiveStream{
		createdAt: time.Now(),
		header:    make(http.Header),
		lim:       currentLiveStreamLimits(),
	}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// enableSpool attaches the disk tier and starts the pump. Without it the stream
// still produces and finishes, but reconnect attach is refused: there is no
// tier to resume from once bytes leave the small RAM window. Must be called
// before the first Write.
func (s *LiveStream) enableSpool(dir, escrowID string, inferenceID uint64) error {
	sp, err := newStreamSpool(dir, escrowID, inferenceID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.spool = sp
	s.spoolPumpDone = make(chan struct{})
	s.mu.Unlock()
	go s.runSpoolPump()
	return nil
}

func (s *LiveStream) Header() http.Header {
	if s.header == nil {
		s.header = make(http.Header)
	}
	return s.header
}

func (s *LiveStream) WriteHeader(statusCode int) {
	// Snapshot under the lock, then write after unlock. Holding s.mu across
	// primary network I/O would stall every LiveStream.Write and every
	// subscriber for as long as the primary TCP blocks — the same invariant
	// the body drain path already upholds.
	s.mu.Lock()
	if s.wroteHdr {
		s.mu.Unlock()
		return
	}
	s.wroteHdr = true
	primary := s.primary
	var hdr http.Header
	if primary != nil && s.header != nil {
		hdr = s.header.Clone()
	}
	if primary == nil {
		s.mu.Unlock()
		return
	}
	// Fence subscriber #0 for the duration of the header write: it targets the
	// same ResponseWriter, and a body write interleaved with WriteHeader would
	// corrupt the response.
	s.hdrInFlight = true
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.hdrInFlight = false
		s.cond.Broadcast()
		s.mu.Unlock()
	}()

	dst := primary.Header()
	for k, vv := range hdr {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
	primary.WriteHeader(statusCode)
}

// Write appends producer bytes to the resume log and wakes readers. Always
// returns success after buffering so a dead / slow gateway writer cannot abort
// ML drain. No network or disk I/O is performed here or under the lock: the
// spool is filled by runSpoolPump on its own goroutine.
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
	if s.lim.formingBytes > 0 && int64(len(s.forming)) > s.lim.formingBytes {
		s.done = true
		s.err = ErrLiveStreamFormingOversize
		s.primary = nil
		s.cond.Broadcast()
		// The producer is not told: failing its Write would abort the ML drain
		// that still owes us a durable body. But the log now diverges from that
		// body, so say so loudly instead of only surfacing it to attach.
		logging.Warn("live stream truncated: forming line over cap; resume disabled for this generation",
			"subsystem", "host",
			"forming_bytes", len(s.forming),
			"cap_bytes", s.lim.formingBytes,
			"total_bytes", s.totalBytes)
		return len(p), nil
	}
	s.trimHeadLocked()
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

// eventsEndLocked is the absolute offset just past the last complete event.
// Everything after it is still forming and has no event boundary yet, so it
// cannot be spooled or trimmed.
func (s *LiveStream) eventsEndLocked() int64 {
	return s.totalBytes - int64(len(s.forming))
}

func (s *LiveStream) spoolActiveLocked() bool {
	return s.spool != nil && s.spoolErr == nil
}

// SpoolActive reports whether this generation has a usable resume tier.
func (s *LiveStream) SpoolActive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.spoolActiveLocked()
}

// trimHeadLocked drops whole events from the RAM window down to
// LiveStreamRingBytes. With a spool the only barrier is what the spool already
// holds — reader offsets are irrelevant, because a reader below the window
// reads from disk. Caller must hold s.mu.
func (s *LiveStream) trimHeadLocked() {
	if s.lim.ringBytes <= 0 || s.retainedBytesLocked() <= s.lim.ringBytes {
		return
	}
	s.trimToBarrierLocked(s.trimBarrierLocked())
	if s.spoolActiveLocked() {
		return
	}
	// Degraded: nothing can be reclaimed from disk, so a reader that stops
	// consuming would grow RAM without bound. Past the hard ceiling, drop it.
	if s.lim.maxRAMBytes > 0 && s.retainedBytesLocked() > s.lim.maxRAMBytes {
		s.dropReadersBelowLocked(s.totalBytes - s.lim.ringBytes)
		s.trimToBarrierLocked(s.trimBarrierLocked())
	}
}

// trimBarrierLocked is the highest offset head-trim may reach.
func (s *LiveStream) trimBarrierLocked() int64 {
	if s.spoolActiveLocked() {
		return s.spoolOffset
	}
	return s.minLiveReaderOffsetLocked()
}

func (s *LiveStream) trimToBarrierLocked(barrier int64) {
	for len(s.events) > 0 && s.retainedBytesLocked() > s.lim.ringBytes {
		ev := s.events[0]
		oldBase := s.bytesBase
		evEnd := oldBase + int64(len(ev))
		if evEnd > barrier {
			return
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

// dropReadersBelowLocked closes every live reader still below off. Degraded
// mode only: with a spool a slow reader is served from disk instead.
func (s *LiveStream) dropReadersBelowLocked(off int64) {
	dropped := false
	for _, sub := range s.subs {
		if sub.closed || sub.offset >= off {
			continue
		}
		sub.closed = true
		sub.dropErr = ErrSubscriberLagged
		if sub.isPrimary {
			s.clientDetached.Store(true)
		}
		dropped = true
	}
	if dropped {
		logging.Warn("dropped live stream reader to respect RAM ceiling (no spool)",
			"subsystem", "host",
			"retained_bytes", s.retainedBytesLocked(),
			"ceiling_bytes", s.lim.maxRAMBytes)
		s.cond.Broadcast()
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
		// No live readers: trim freely (whole events) until under the window.
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

// runSpoolPump follows the log's complete-event tail and writes it to disk. It
// is the only writer of the spool, and advancing spoolOffset is what unlocks
// head-trim — so a stalled disk costs RAM window, never ML progress.
func (s *LiveStream) runSpoolPump() {
	defer close(s.spoolPumpDone)
	for {
		s.mu.Lock()
		for s.spoolActiveLocked() && !s.done && s.spoolOffset >= s.eventsEndLocked() {
			s.cond.Wait()
		}
		if !s.spoolActiveLocked() {
			s.mu.Unlock()
			return
		}
		end := s.eventsEndLocked()
		if s.spoolOffset >= end {
			// Caught up; only reachable here once the producer is finished.
			s.mu.Unlock()
			return
		}
		spool := s.spool
		payload, starts := s.copyForSpoolLocked(end)
		s.mu.Unlock()

		err := spool.append(payload, starts)

		s.mu.Lock()
		if err != nil {
			s.spoolErr = err
			s.cond.Broadcast()
			s.mu.Unlock()
			logging.Warn("live stream spool failed; resume disabled for this generation",
				"subsystem", "host", "error", err)
			return
		}
		s.spoolOffset = end
		s.spoolEvents += int64(len(starts))
		s.trimHeadLocked()
		s.cond.Broadcast()
		s.mu.Unlock()
	}
}

// copyForSpoolLocked returns the bytes in [spoolOffset, end) plus the absolute
// start offset of every content event beginning in that range. The walk is over
// the retained window only, which head-trim keeps bounded.
func (s *LiveStream) copyForSpoolLocked(end int64) ([]byte, []int64) {
	out := make([]byte, 0, end-s.spoolOffset)
	var starts []int64
	abs := s.bytesBase
	for _, ev := range s.events {
		evEnd := abs + int64(len(ev))
		if evEnd <= s.spoolOffset {
			abs = evEnd
			continue
		}
		if abs >= end {
			break
		}
		// A blank separator merged into an already-spooled event appends bytes
		// to it without starting a new one, so only a genuinely new event head
		// gets an index entry.
		if abs >= s.spoolOffset && !isBlankSSELine(ev) {
			starts = append(starts, abs)
		}
		start := int64(0)
		if s.spoolOffset > abs {
			start = s.spoolOffset - abs
		}
		stop := int64(len(ev))
		if abs+stop > end {
			stop = end - abs
		}
		if start < stop {
			out = append(out, ev[start:stop]...)
		}
		abs = evEnd
	}
	return out, starts
}

func (s *LiveStream) markSpoolFailed(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.spoolErr == nil {
		s.spoolErr = err
	}
	s.cond.Broadcast()
}

// Release hands the generation's scratch state back. Call it after Close and
// WaitPrimary: Close stops production, Release frees the spool. They are
// separate because a reconnect reader may still be draining the disk tier after
// the producer is finished.
func (s *LiveStream) Release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.released = true
	s.releaseSpoolLocked()
}

// releaseSpoolLocked closes and removes the spool once Release has been called
// and no reader is still draining it.
func (s *LiveStream) releaseSpoolLocked() {
	if s.spool == nil || !s.released {
		return
	}
	// Closed readers are compacted out of subs on unsubscribe, so any remaining
	// entry is still draining (and may still read the spool).
	if len(s.subs) > 0 {
		return
	}
	spool, pumpDone := s.spool, s.spoolPumpDone
	s.spool = nil
	s.cond.Broadcast() // wake the pump so it observes the released spool
	go func() {
		if pumpDone != nil {
			<-pumpDone
		}
		spool.close()
	}()
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

// SpooledBytes returns how much of the log is on disk (test helper).
func (s *LiveStream) SpooledBytes() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.spoolOffset
}

// Subscribe replays from the resume cursor then follows the live tail until
// the stream completes. First post-subscribe byte is emitted without waiting
// for ML/[DONE]. Reader I/O happens outside the lock so a slow reconnect
// client cannot stall the ML producer.
func (s *LiveStream) Subscribe(w http.ResponseWriter, deliveredEvents, deliveredPartial int64) error {
	if err := validateResumeCursor(deliveredEvents, deliveredPartial); err != nil {
		return err
	}
	if err := s.checkAttachable(); err != nil {
		return err
	}

	offset, err := s.resolveCursorOffset(deliveredEvents, deliveredPartial)
	if err != nil {
		return err
	}

	s.mu.Lock()
	if err := s.attachableLocked(); err != nil {
		s.mu.Unlock()
		return err
	}
	if s.done && offset >= s.totalBytes {
		streamErr := s.err
		s.mu.Unlock()
		return streamErr
	}
	sub := &liveSubscriber{
		offset:         offset,
		lastProgressAt: time.Now(),
	}
	s.reseedSubscriberHintLocked(sub)
	s.subs = append(s.subs, sub)
	s.mu.Unlock()

	return s.drainSubscriber(w, sub)
}

func (s *LiveStream) checkAttachable() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attachableLocked()
}

func (s *LiveStream) attachableLocked() error {
	if s.expiredLocked(time.Now()) {
		return ErrLiveStreamPruned
	}
	if s.err != nil && errors.Is(s.err, ErrLiveStreamFormingOversize) {
		return ErrLiveStreamFormingOversize
	}
	if !s.spoolActiveLocked() {
		// No disk tier means no resume horizon: anything below the small RAM
		// window is gone for good, so refuse rather than serve a hole.
		return ErrLiveStreamResumeUnavailable
	}
	return nil
}

// resolveCursorOffset converts a wire cursor into an absolute byte offset,
// using the RAM window when the cursor is inside it and the spool index when it
// is older. The spool read happens off-lock: index entries are immutable once
// written, so the producer never has to wait on a file read.
func (s *LiveStream) resolveCursorOffset(deliveredEvents, deliveredPartial int64) (int64, error) {
	s.mu.Lock()
	if deliveredEvents >= s.eventsBase {
		offset, err := s.deliveredAbsOffsetLocked(deliveredEvents, deliveredPartial)
		s.mu.Unlock()
		return offset, err
	}
	spool, spoolEvents, spoolOffset := s.spool, s.spoolEvents, s.spoolOffset
	active := s.spoolActiveLocked()
	s.mu.Unlock()

	if !active {
		return 0, ErrResumeCursorPast
	}
	return spoolCursorOffset(spool, spoolEvents, spoolOffset, deliveredEvents, deliveredPartial)
}

// spoolCursorOffset resolves a cursor whose event has already left the RAM
// window, against the on-disk event index.
func spoolCursorOffset(spool *streamSpool, spoolEvents, spoolOffset, deliveredEvents, deliveredPartial int64) (int64, error) {
	if deliveredEvents >= spoolEvents {
		return 0, ErrResumeCursorPast
	}
	base, err := spool.eventOffset(deliveredEvents)
	if err != nil {
		return 0, err
	}
	if deliveredPartial == 0 {
		return base, nil
	}
	// Bound the partial against the event it indexes into. The next index entry
	// is that event's end; for the last spooled event it is the spool tip.
	end := spoolOffset
	if deliveredEvents+1 < spoolEvents {
		if end, err = spool.eventOffset(deliveredEvents + 1); err != nil {
			return 0, err
		}
	}
	if base+deliveredPartial > end {
		return 0, ErrResumeCursorPast
	}
	return base + deliveredPartial, nil
}

// drainSubscriber reads [sub.offset, totalBytes) at the client's pace until the
// stream completes or the writer fails / stalls. Each pass writes at most
// LiveStreamWriteChunkBytes, from RAM when the reader is inside the window and
// from the spool when it has fallen behind it.
func (s *LiveStream) drainSubscriber(w http.ResponseWriter, sub *liveSubscriber) error {
	// Every exit path must deregister: a still-registered reader keeps the
	// spool (and, in degraded mode, the RAM window) alive.
	defer s.unsubscribe(sub)

	var spoolBuf []byte
	for {
		s.mu.Lock()
		// Two reasons to park, checked together so that waking for one cannot
		// skip the other: no undelivered bytes yet, or — for subscriber #0
		// only — WriteHeader is still writing to the same ResponseWriter.
		// Gating on hdrInFlight rather than wroteHdr keeps a stream whose
		// producer never writes a header from parking here forever.
		for !sub.closed && ((!s.done && sub.offset >= s.totalBytes) || (sub.isPrimary && s.hdrInFlight)) {
			s.cond.Wait()
			// Stall clock starts when undelivered data appears — not while idle.
			sub.lastProgressAt = time.Now()
		}
		if sub.closed {
			dropErr := sub.dropErr
			s.mu.Unlock()
			if sub.isPrimary {
				return nil // client gone; producer keeps draining
			}
			return dropErr
		}
		if sub.offset >= s.totalBytes {
			// Caught up and stream is done.
			err := s.err
			s.mu.Unlock()
			return err
		}
		if !sub.isPrimary && s.lim.stallTimeout > 0 &&
			time.Since(sub.lastProgressAt) >= s.lim.stallTimeout {
			s.mu.Unlock()
			return ErrSubscriberLagged
		}

		var chunk []byte
		var spoolAt, spoolLen int64
		if sub.offset < s.bytesBase {
			if !s.spoolActiveLocked() {
				s.mu.Unlock()
				return s.readerLost(sub)
			}
			spoolAt = sub.offset
			spoolLen = min(s.lim.chunkBytes, s.spoolOffset-sub.offset)
		} else {
			chunk = s.copyPendingLocked(sub, s.lim.chunkBytes)
		}
		spool := s.spool
		lastProgress := sub.lastProgressAt
		isPrimary := sub.isPrimary
		s.mu.Unlock()

		if spoolLen > 0 {
			if int64(cap(spoolBuf)) < spoolLen {
				spoolBuf = make([]byte, spoolLen)
			}
			n, err := spool.readAt(spoolBuf[:spoolLen], spoolAt)
			if err != nil || n == 0 {
				if err == nil {
					err = ErrSpoolClosed
				}
				s.markSpoolFailed(err)
				return s.readerLost(sub)
			}
			chunk = spoolBuf[:n]
		}
		if len(chunk) == 0 {
			continue
		}

		if err := writeReplay(w, chunk, s.lim.writeDeadline(lastProgress, isPrimary)); err != nil {
			if isPrimary {
				s.clientDetached.Store(true)
				return nil // client gone; producer keeps draining
			}
			return ErrSubscriberLagged
		}

		s.mu.Lock()
		if !sub.closed {
			sub.offset += int64(len(chunk))
			sub.lastProgressAt = time.Now()
			s.trimHeadLocked()
			s.cond.Broadcast()
		}
		s.mu.Unlock()
		if isPrimary && s.injectPrimaryDetachFault(w, sub) {
			return nil // client gone; producer keeps draining
		}
	}
}

// readerLost ends a reader whose bytes are no longer reachable: it fell out of
// the RAM window and there is no spool to fall back to. The primary is reported
// as a client detach (the producer keeps going into the durable body); anyone
// else gets ErrSubscriberLagged so the gateway can escalate.
func (s *LiveStream) readerLost(sub *liveSubscriber) error {
	if sub.isPrimary {
		s.clientDetached.Store(true)
		return nil
	}
	return ErrSubscriberLagged
}

func (s *LiveStream) unsubscribe(sub *liveSubscriber) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub.closed = true
	for i, existing := range s.subs {
		if existing == sub {
			s.subs = append(s.subs[:i], s.subs[i+1:]...)
			break
		}
	}
	s.trimHeadLocked()
	s.releaseSpoolLocked()
	s.cond.Broadcast()
}

func validateResumeCursor(deliveredEvents, deliveredPartial int64) error {
	if deliveredEvents < 0 || deliveredPartial < 0 {
		return ErrInvalidResumeCursor
	}
	return nil
}

// deliveredAbsOffsetLocked converts the wire (events, partial) cursor into the
// absolute byte offset of the first undelivered byte, for cursors inside the
// retained RAM window. Caller must hold s.mu.
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

// copyPendingLocked copies at most limit bytes of [sub.offset, totalBytes) out
// of the RAM window and advances sub's cursor hint. Caller must hold s.mu; the
// returned slice is safe after unlock. Runs under the producer's lock, so it
// must not walk already-delivered events: the hint keeps the scan proportional
// to the undelivered tail, and limit keeps the copy bounded.
func (s *LiveStream) copyPendingLocked(sub *liveSubscriber, limit int64) []byte {
	if sub.offset >= s.totalBytes {
		return nil
	}
	if sub.offset < s.bytesBase {
		// Reader belongs on the spool path; only reachable if the window moved
		// between the caller's check and here.
		sub.offset = s.bytesBase
	}
	if sub.evAbs < s.bytesBase || sub.evIdx >= len(s.events) {
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

	end := s.totalBytes
	if limit > 0 && end-sub.offset > limit {
		end = sub.offset + limit
	}
	out := make([]byte, 0, end-sub.offset)
	abs := sub.evAbs
	for i := sub.evIdx; i < len(s.events) && abs < end; i++ {
		ev := s.events[i]
		if abs+int64(len(ev)) > sub.offset {
			start := int64(0)
			if sub.offset > abs {
				start = sub.offset - abs
			}
			stop := int64(len(ev))
			if abs+stop > end {
				stop = end - abs
			}
			if start < stop {
				out = append(out, ev[start:stop]...)
			}
		}
		abs += int64(len(ev))
	}
	// abs is now the end of the events region; forming holds the tail.
	if formingLen := int64(len(s.forming)); formingLen > 0 && abs < end {
		start := sub.offset - abs
		if start < 0 {
			start = 0
		}
		stop := formingLen
		if abs+stop > end {
			stop = end - abs
		}
		if start < stop {
			out = append(out, s.forming[start:stop]...)
		}
	}
	return out
}

// writeDeadline bounds one write. The primary gets the write timeout; a
// reconnect additionally may not push past its stall deadline, so a socket that
// accepts nothing is caught inside the write rather than after it.
func (l liveStreamLimits) writeDeadline(lastProgressAt time.Time, isPrimary bool) time.Time {
	var deadline time.Time
	if l.writeTimeout > 0 {
		deadline = time.Now().Add(l.writeTimeout)
	}
	if !isPrimary && l.stallTimeout > 0 && !lastProgressAt.IsZero() {
		stallAt := lastProgressAt.Add(l.stallTimeout)
		if deadline.IsZero() || stallAt.Before(deadline) {
			deadline = stallAt
		}
	}
	return deadline
}

// writeReplay writes one chunk to a reader, bounded by deadline.
//
// The write runs inline on the drain goroutine on purpose. Handing it to a
// helper goroutine and abandoning it on timeout would leak that goroutine (and
// pin the chunk it holds) on a black-holed socket, and — for the primary —
// would let WaitPrimary return while another goroutine could still write to the
// same response, which is exactly the interleaving primaryDone exists to
// prevent. The deadline is enforced by the transport instead.
func writeReplay(w http.ResponseWriter, chunk []byte, deadline time.Time) error {
	if len(chunk) == 0 {
		return nil
	}
	if !deadline.IsZero() {
		if err := http.NewResponseController(w).SetWriteDeadline(deadline); err != nil {
			// Nothing can interrupt this writer, so the deadline is advisory
			// only. Real gateway connections support it; a wrapper that does not
			// can park this reader until the stack below gives up.
			logging.Debug("live stream writer does not support write deadlines",
				"subsystem", "host", "error", err)
		}
	}
	if _, err := w.Write(chunk); err != nil {
		return err
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}
