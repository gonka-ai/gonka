package transport

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"

	"devshard/host"
)

// chatGzipCoalesceMax bounds pending bytes, so a stream that never sends a
// blank line still leaves.
const chatGzipCoalesceMax = 16 << 10

// chatGzipLevel is the lowest level that keeps the 32 KiB window across a sync
// flush. BestSpeed starts each flushed block with an empty window, so a token
// can match only within its own frame and a per-event flush pays for the
// repeated id, object, created and model every time. Flush still ends the
// block, so a frame is readable the moment it is sent.
const chatGzipLevel = 2

// ChatGzipEmitter is one gzip.Writer (chatGzipLevel). Concatenating chunks
// reconstitutes the stream. Close with no Write emits nothing so a handler
// error before the first event is zero frames, not an empty gzip member.
type ChatGzipEmitter struct {
	buf   bytes.Buffer
	gz    *gzip.Writer
	send  func([]byte) error
	wrote bool
}

func NewChatGzipEmitter(send func([]byte) error) *ChatGzipEmitter {
	e := &ChatGzipEmitter{send: send}
	e.gz, _ = gzip.NewWriterLevel(&e.buf, chatGzipLevel)
	return e
}

func (e *ChatGzipEmitter) Write(p []byte) (int, error) {
	if e == nil || e.gz == nil {
		return 0, io.ErrClosedPipe
	}
	if len(p) > 0 {
		e.wrote = true
	}
	return e.gz.Write(p)
}

func (e *ChatGzipEmitter) Flush() error {
	if e == nil || e.gz == nil {
		return io.ErrClosedPipe
	}
	if err := e.gz.Flush(); err != nil {
		return err
	}
	return e.emit()
}

func (e *ChatGzipEmitter) Close() error {
	if e == nil || e.gz == nil {
		return nil
	}
	if !e.wrote {
		e.gz = nil
		return nil
	}
	err := e.gz.Close()
	e.gz = nil
	if err != nil {
		return err
	}
	return e.emit()
}

func (e *ChatGzipEmitter) emit() error {
	if e.buf.Len() == 0 {
		return nil
	}
	chunk := append([]byte(nil), e.buf.Bytes()...)
	e.buf.Reset()
	if e.send == nil {
		return nil
	}
	return e.send(chunk)
}

// ChatFrameSink writes uncompressed SSE bytes into ChatGzipEmitter as if it
// were an http.ResponseWriter. ExecutionJob.ResponseWriter uses this.
//
// The executor still flushes every scanner line, so one token is two writes:
// the data line, then a blank line. The data-line flush is not a frame. The
// blank line finishes that same event, and the two lines leave together.
//
// Until the first event that carries output (content, reasoning, a tool call,
// an error or [DONE]) every finished event leaves alone, so the gateway can
// time the first token and pick a winner without waiting for a group. After
// it the sink counts finished events:
//   - joined (the default): one flush and one signature per group of events;
//   - separate: one flush per event, a signature on the last of each group.
//
// Pending bytes also leave at chatGzipCoalesceMax, so a stream that never
// sends a blank line cannot stall. Close sends the short tail with [DONE] and
// devshard_meta. Once a signer is set, the first frame, the first output,
// the last frame of each group and the tail carry a signature over the whole
// uncompressed prefix. No prefix is signed twice.
type ChatFrameSink struct {
	header  http.Header
	status  int
	gzip    *ChatGzipEmitter
	send    func(chunk, sig []byte, streamBytes uint64) error
	sendErr error
	pending int
	tail    [2]byte
	// event holds the bytes of the event being written, until the first
	// output: only then does the sink need to read an event.
	event    []byte
	wrote    bool
	schedule *host.ChatFrameSchedule
	signer   *ChatFrameSigner
	// events counts finished events since the last signed frame.
	events int
	// sawOutput is set by the first event that carries output.
	sawOutput bool
	signNext  bool
}

// perEventChatFrames is one frame per finished event, the schedule a stream
// keeps unless it is told to sign.
var perEventChatFrames = &host.ChatFrameSchedule{Every: 1, Separate: true}

// NewChatFrameSink emits one unsigned frame per finished event: the wire
// shape a gateway that predates signed frames reads.
func NewChatFrameSink(send func([]byte) error) *ChatFrameSink {
	return NewSignedChatFrameSink(func(chunk, _ []byte, _ uint64) error { return send(chunk) })
}

// NewSignedChatFrameSink emits one unsigned frame per finished event unless
// SignFrames sets a schedule and a signer first.
func NewSignedChatFrameSink(send func(chunk, sig []byte, streamBytes uint64) error) *ChatFrameSink {
	sink := &ChatFrameSink{header: make(http.Header), send: send, schedule: perEventChatFrames}
	sink.gzip = NewChatGzipEmitter(sink.emit)
	return sink
}

// SignFrames cuts frames on schedule, a nil schedule being the default, and
// signs them. It must come before the first write: a signature covers the
// stream from its first byte, so a sink that has already written stays
// unsigned.
func (s *ChatFrameSink) SignFrames(schedule *host.ChatFrameSchedule, signer *ChatFrameSigner) {
	if s == nil || s.wrote || s.signer != nil || signer == nil {
		return
	}
	s.signer = signer
	s.schedule = schedule
}

func (s *ChatFrameSink) Header() http.Header {
	if s.header == nil {
		s.header = make(http.Header)
	}
	return s.header
}

func (s *ChatFrameSink) WriteHeader(status int) {
	s.status = status
}

func (s *ChatFrameSink) Write(p []byte) (int, error) {
	if s.sendErr != nil {
		return 0, s.sendErr
	}
	n, err := s.gzip.Write(p)
	if n > 0 {
		s.wrote = true
		s.note(p[:n])
		if !s.sawOutput && len(s.event) < chatGzipCoalesceMax {
			s.event = append(s.event, p[:n]...)
		}
		if s.signer != nil {
			s.signer.add(p[:n])
		}
	}
	if err != nil {
		return n, err
	}
	if s.pending >= chatGzipCoalesceMax {
		if ferr := s.flushGzip(true); ferr != nil {
			return n, ferr
		}
	}
	return n, nil
}

func (s *ChatFrameSink) note(p []byte) {
	s.pending += len(p)
	switch len(p) {
	case 0:
	case 1:
		s.tail[0] = s.tail[1]
		s.tail[1] = p[0]
	default:
		s.tail[0] = p[len(p)-2]
		s.tail[1] = p[len(p)-1]
	}
}

// eventBoundary reports a finished SSE event that this flush has not counted
// yet. A line flush can land twice on the same blank line.
func (s *ChatFrameSink) eventBoundary() bool {
	return s.pending > 0 && s.tail[0] == '\n' && s.tail[1] == '\n'
}

func (s *ChatFrameSink) Flush() {
	_ = s.FlushErr()
}

// FlushErr runs the schedule on each finished SSE event. http.Flusher.Flush
// cannot return stream.Send errors. The receipt calls FlushNow so a failed
// frame still returns before execution.
func (s *ChatFrameSink) FlushErr() error {
	if s == nil {
		return io.ErrClosedPipe
	}
	if s.sendErr != nil {
		return s.sendErr
	}
	if s.gzip == nil {
		return io.ErrClosedPipe
	}
	if !s.eventBoundary() {
		return nil
	}
	s.tail = [2]byte{}
	s.events++
	if !s.sawOutput {
		s.sawOutput = sseEventCarriesOutput(s.event)
		s.event = s.event[:0]
		if s.sawOutput {
			s.event = nil
		}
		return s.flushGzip(s.sawOutput || !s.signedAny() || s.events >= chatFrameEvery(s.schedule))
	}
	if s.events >= chatFrameEvery(s.schedule) {
		return s.flushGzip(true)
	}
	if !chatFramesJoined(s.schedule) {
		return s.flushGzip(false)
	}
	return nil
}

// FlushNow sends the held bytes with a signature. The receipt uses this so a
// failed frame returns before RunExecution.
func (s *ChatFrameSink) FlushNow() error {
	if s == nil || s.gzip == nil {
		return io.ErrClosedPipe
	}
	if s.sendErr != nil {
		return s.sendErr
	}
	return s.flushGzip(true)
}

// flushGzip ends the gzip block and sends it, signed when sign is true and a
// signer is set. A signed frame starts the next group.
func (s *ChatFrameSink) flushGzip(sign bool) error {
	if s.pending == 0 {
		return nil
	}
	s.pending = 0
	s.tail = [2]byte{}
	s.signNext = sign
	if err := s.gzip.Flush(); err != nil {
		s.sendErr = err
		return err
	}
	if sign {
		s.events = 0
	}
	return nil
}

func (s *ChatFrameSink) signedAny() bool {
	return s.signer != nil && s.signer.seq > 0
}

// emit is the ChatGzipEmitter send hook. It attaches the frame's signature
// when the frame was meant to be signed and covers bytes no earlier
// signature did.
func (s *ChatFrameSink) emit(chunk []byte) error {
	sign := s.signNext
	s.signNext = false
	if !sign || s.signer == nil || !s.signer.hasUnsigned() {
		return s.send(chunk, nil, 0)
	}
	sig, streamBytes, err := s.signer.sign()
	if err != nil {
		return err
	}
	return s.send(chunk, sig, streamBytes)
}

func (s *ChatFrameSink) Close() error {
	if s == nil {
		return io.ErrClosedPipe
	}
	if s.gzip == nil {
		return s.sendErr
	}
	s.pending = 0
	s.event = nil
	s.signNext = true
	err := s.gzip.Close()
	if s.sendErr != nil {
		return s.sendErr
	}
	if err != nil {
		s.sendErr = err
	}
	return err
}
