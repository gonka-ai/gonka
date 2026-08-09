package host

import (
	"bytes"
	"errors"
	"net/http"
	"sync"
	"time"
)

// InflightReplayBufferTTL bounds how long a live per-inference SSE buffer may
// stay in RAM if drain never persists. Aligned with the inference drain timeout.
var InflightReplayBufferTTL = 5 * time.Minute

var (
	// ErrResumeCursorPast is returned when the reconnect cursor is beyond the
	// host's buffered prefix (gateway should escalate).
	ErrResumeCursorPast = errors.New("resume cursor past live buffer")
	// ErrLiveStreamGone is returned when there is no live buffer to attach to.
	ErrLiveStreamGone = errors.New("live stream unavailable")
	// ErrLiveStreamPruned is returned when the live buffer exceeded its TTL.
	ErrLiveStreamPruned = errors.New("live stream pruned")
)

// LiveStream is a per-inference fan-out hub: one ML producer, N gateway subscribers.
// It implements http.ResponseWriter so the inference proxy can write into it.
//
// Reconnect subscribers receive a one-shot replay from the resume cursor, then
// only producer bytes that arrive after that snapshot (no duplication).
type LiveStream struct {
	mu         sync.Mutex
	events     [][]byte // complete newline-terminated SSE lines
	forming    []byte   // bytes of the currently incomplete event
	totalBytes int64    // sum(len(events))+len(forming)
	subs       []*liveSubscriber
	primary    http.ResponseWriter
	done       bool
	err        error
	createdAt  time.Time
	header     http.Header
	wroteHdr   bool
}

type liveSubscriber struct {
	ch       chan []byte
	closed   bool
	sentAbs  int64 // absolute producer bytes already delivered to this sub
}

func newLiveStream() *LiveStream {
	return &LiveStream{
		createdAt: time.Now(),
		header:    make(http.Header),
	}
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

// Write accepts producer bytes, buffers them for resume, writes the primary
// path, and fans out new bytes to reconnect subscribers. Always returns success
// after buffering so a dead gateway writer cannot abort ML drain.
func (s *LiveStream) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	cp := append([]byte(nil), p...)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return len(p), nil
	}
	s.bufferLocked(cp)
	if s.primary != nil {
		if _, err := s.primary.Write(cp); err != nil {
			s.primary = nil
		} else if f, ok := s.primary.(http.Flusher); ok {
			f.Flush()
		}
	}
	for _, sub := range s.subs {
		if sub.closed {
			continue
		}
		// Subscribers are always caught up to totalBytes after Subscribe replay;
		// each Write is therefore entirely new for them.
		select {
		case sub.ch <- append([]byte(nil), cp...):
			sub.sentAbs += int64(len(cp))
		default:
		}
	}
	return len(p), nil
}

func (s *LiveStream) Flush() {
	s.mu.Lock()
	primary := s.primary
	s.mu.Unlock()
	if f, ok := primary.(http.Flusher); ok {
		f.Flush()
	}
}

// SetPrimary registers the first gateway ResponseWriter (the original request).
func (s *LiveStream) SetPrimary(w http.ResponseWriter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.primary = w
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
		s.events = append(s.events, event)
	}
}

// Expired reports whether the live buffer should be pruned.
func (s *LiveStream) Expired(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.expiredLocked(now)
}

func (s *LiveStream) expiredLocked(now time.Time) bool {
	return !s.done && now.Sub(s.createdAt) > InflightReplayBufferTTL
}

// Close marks the stream finished and unblocks subscribers.
func (s *LiveStream) Close(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return
	}
	s.done = true
	s.err = err
	for _, sub := range s.subs {
		if !sub.closed {
			close(sub.ch)
			sub.closed = true
		}
	}
	s.subs = nil
	s.primary = nil
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

// Subscribe replays from the resume cursor then follows the live tail until
// the stream completes. First post-subscribe byte is emitted without waiting
// for ML/[DONE].
func (s *LiveStream) Subscribe(w http.ResponseWriter, deliveredEvents, deliveredPartial int64) error {
	s.mu.Lock()
	if s.expiredLocked(time.Now()) {
		s.mu.Unlock()
		return ErrLiveStreamPruned
	}
	if err := s.replayLocked(w, deliveredEvents, deliveredPartial); err != nil {
		s.mu.Unlock()
		return err
	}
	if s.done {
		err := s.err
		s.mu.Unlock()
		return err
	}
	sub := &liveSubscriber{
		ch:      make(chan []byte, 64),
		sentAbs: s.totalBytes, // caught up through replay
	}
	s.subs = append(s.subs, sub)
	s.mu.Unlock()

	for chunk := range sub.ch {
		if _, err := w.Write(chunk); err != nil {
			s.unsubscribe(sub)
			return nil // client gone; producer keeps draining
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
	s.mu.Lock()
	err := s.err
	s.mu.Unlock()
	return err
}

func (s *LiveStream) unsubscribe(sub *liveSubscriber) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub.closed = true
}

func (s *LiveStream) replayLocked(w http.ResponseWriter, deliveredEvents, deliveredPartial int64) error {
	nEvents := int64(len(s.events))
	formingLen := int64(len(s.forming))

	if deliveredEvents > nEvents {
		return ErrResumeCursorPast
	}
	if deliveredEvents == nEvents {
		if deliveredPartial > formingLen {
			return ErrResumeCursorPast
		}
		if deliveredPartial < formingLen {
			if _, err := w.Write(s.forming[deliveredPartial:]); err != nil {
				return err
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		return nil
	}

	for i := deliveredEvents; i < nEvents; i++ {
		ev := s.events[i]
		if i == deliveredEvents && deliveredPartial > 0 {
			if deliveredPartial > int64(len(ev)) {
				return ErrResumeCursorPast
			}
			if deliveredPartial == int64(len(ev)) {
				continue
			}
			ev = ev[deliveredPartial:]
		}
		if _, err := w.Write(ev); err != nil {
			return err
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
	if formingLen > 0 {
		if _, err := w.Write(s.forming); err != nil {
			return err
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
	return nil
}
