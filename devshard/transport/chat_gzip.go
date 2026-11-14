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
// With no signer, every finished event leaves on its own. With a signer, an
// event that is neither a content token nor terminal is dropped when it is an
// empty choice, and kept when it is protocol (the receipt, devshard_meta).
// The first content token is held until the next content token arrives, and
// that next token opens the following chunk. Later chunks hold Join content
// tokens and leave when the token after the group arrives. [DONE] and an
// error are appended to the open chunk, which is sent immediately and signed.
// A signature lands on chunk SignEvery, 2×SignEvery, and so on, and on the
// tail. Bytes of the held-back token are not hashed until that token is
// placed into a chunk that is being sent.
//
// Pending bytes also leave at chatGzipCoalesceMax, so a stream that never
// sends a blank line cannot stall. Close signs whatever is still open. No
// prefix is signed twice.
type ChatFrameSink struct {
	header  http.Header
	status  int
	gzip    *ChatGzipEmitter
	send    func(chunk, sig []byte, streamBytes, tokenCount uint64) error
	sendErr error
	pending int
	tail    [2]byte
	// event holds the bytes of the event being written. With a signer that is
	// the whole event, and it is not hashed until the event is classified.
	// Without one it is kept only until the first output.
	event    []byte
	wrote    bool
	schedule *host.ChatFrameSchedule
	signer   *ChatFrameSigner
	// events counts finished events since the last signed frame. The unsigned
	// path uses it. The signed path counts content tokens instead.
	events int
	// sawOutput is set by the first event that carries output. Unsigned path.
	sawOutput bool
	signNext  bool
	// bufPending and bufTail find the blank line that finishes the event the
	// signer is holding outside the gzip writer.
	bufPending int
	bufTail    [2]byte
	// openTokens is the content tokens in the chunk that has not been sent.
	// chunksSent is how many content chunks have been sent. totalTokens is
	// the content tokens already hashed. signTokens is the count the next
	// signature attests.
	openTokens  int
	chunksSent  int
	totalTokens uint64
	signTokens  uint64
}

// perEventChatFrames is one frame per finished event, the schedule a stream
// keeps unless it is told to sign.
var perEventChatFrames = &host.ChatFrameSchedule{Join: 1}

// NewChatFrameSink emits one unsigned frame per finished event: the wire
// shape a gateway that predates signed frames reads.
func NewChatFrameSink(send func([]byte) error) *ChatFrameSink {
	return NewSignedChatFrameSink(func(chunk, _ []byte, _, _ uint64) error { return send(chunk) })
}

// NewSignedChatFrameSink emits one unsigned frame per finished event unless
// SignFrames sets a schedule and a signer first.
func NewSignedChatFrameSink(send func(chunk, sig []byte, streamBytes, tokenCount uint64) error) *ChatFrameSink {
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
	if s.signer != nil {
		return s.writeSigned(p)
	}
	n, err := s.gzip.Write(p)
	if n > 0 {
		s.wrote = true
		s.note(p[:n])
		if !s.sawOutput && len(s.event) < chatGzipCoalesceMax {
			s.event = append(s.event, p[:n]...)
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

// writeSigned keeps the event out of the gzip writer until FlushErr has
// classified it. The token that closes a chunk is therefore not in the chunk
// it closes, and it is not in the hash.
func (s *ChatFrameSink) writeSigned(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	s.wrote = true
	s.event = append(s.event, p...)
	noteTail(&s.bufTail, &s.bufPending, p)
	if s.bufPending >= chatGzipCoalesceMax {
		if err := s.commit(s.event); err != nil {
			return len(p), err
		}
		s.event = nil
		s.bufPending = 0
		s.bufTail = [2]byte{}
		if err := s.flushGzip(false); err != nil {
			return len(p), err
		}
	}
	return len(p), nil
}

func (s *ChatFrameSink) commit(p []byte) error {
	if len(p) == 0 {
		return nil
	}
	n, err := s.gzip.Write(p)
	if n > 0 {
		s.pending += n
		if s.signer != nil {
			s.signer.add(p[:n])
		}
	}
	return err
}

func (s *ChatFrameSink) note(p []byte) {
	noteTail(&s.tail, &s.pending, p)
}

func noteTail(tail *[2]byte, pending *int, p []byte) {
	*pending += len(p)
	switch len(p) {
	case 0:
	case 1:
		tail[0] = tail[1]
		tail[1] = p[0]
	default:
		tail[0] = p[len(p)-2]
		tail[1] = p[len(p)-1]
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
	if s.signer != nil {
		return s.flushSigned()
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
		return s.flushGzip(s.sawOutput || !s.signedAny() || s.events >= chatFrameJoin(s.schedule))
	}
	if s.events >= chatFrameJoin(s.schedule) {
		return s.flushGzip(true)
	}
	return nil
}

// flushSigned applies the content schedule to every finished SSE event in the
// buffer. One flush may carry several events: a non-streaming body writes the
// answer and [DONE] together, and a stored replay flushes once at the end.
// Each event is scheduled on its own. An unfinished tail stays in s.event.
func (s *ChatFrameSink) flushSigned() error {
	for {
		i := bytes.Index(s.event, []byte("\n\n"))
		if i < 0 {
			break
		}
		ev := s.event[:i+2]
		s.event = s.event[i+2:]
		if len(s.event) == 0 {
			s.event = nil
		}
		if err := s.scheduleSignedEvent(ev); err != nil {
			s.syncSignedEventTail()
			return err
		}
	}
	s.syncSignedEventTail()
	return nil
}

// syncSignedEventTail keeps the coalesce cursor aligned with the unfinished
// tail flushSigned left behind.
func (s *ChatFrameSink) syncSignedEventTail() {
	s.bufPending = len(s.event)
	s.bufTail = [2]byte{}
	switch n := len(s.event); {
	case n >= 2:
		s.bufTail[0] = s.event[n-2]
		s.bufTail[1] = s.event[n-1]
	case n == 1:
		s.bufTail[1] = s.event[0]
	}
}

func (s *ChatFrameSink) scheduleSignedEvent(ev []byte) error {
	switch classifySSE(ev) {
	case sseContent:
		if s.openTokens >= s.chunkTarget() && s.pending > 0 {
			if err := s.flushChunk(false); err != nil {
				return err
			}
		}
		if err := s.commit(ev); err != nil {
			return err
		}
		s.openTokens++
		s.totalTokens++
		return nil
	case sseTerminal:
		if s.pending == 0 && s.openTokens == 0 {
			return nil
		}
		if err := s.commit(ev); err != nil {
			return err
		}
		return s.flushChunk(true)
	case sseProtocol:
		return s.commit(ev)
	default:
		return nil
	}
}

// chunkTarget is 1 for the first content chunk and Join for every chunk
// after it. The chunk is sent when the next content token arrives, so the
// open chunk reaches this many tokens and then waits.
func (s *ChatFrameSink) chunkTarget() int {
	if s.chunksSent == 0 {
		return 1
	}
	return chatFrameJoin(s.schedule)
}

// flushChunk sends the open content chunk. The chunk counter advances for
// every send. The chunk is signed when that counter is a multiple of
// SignEvery, or when force is set because the chunk is the tail.
func (s *ChatFrameSink) flushChunk(force bool) error {
	if s.pending == 0 {
		s.openTokens = 0
		return nil
	}
	s.chunksSent++
	sign := force || s.chunksSent%chatFrameSignEvery(s.schedule) == 0
	s.openTokens = 0
	s.signTokens = s.totalTokens
	return s.flushGzip(sign)
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
	if s.signer != nil && len(s.event) > 0 {
		if err := s.commit(s.event); err != nil {
			return err
		}
		s.event = nil
		s.bufPending = 0
		s.bufTail = [2]byte{}
	}
	// A content chunk released here is the tail of what is held. The receipt
	// has no content token, so it is signed without taking a chunk number.
	if s.signer != nil && s.openTokens > 0 {
		return s.flushChunk(true)
	}
	s.signTokens = s.totalTokens
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
	return s.signer != nil && s.signer.signedBytes > 0
}

// emit is the ChatGzipEmitter send hook. It attaches the frame's signature
// when the frame was meant to be signed and covers bytes no earlier
// signature did.
func (s *ChatFrameSink) emit(chunk []byte) error {
	sign := s.signNext
	s.signNext = false
	if !sign || s.signer == nil || !s.signer.hasUnsigned() {
		return s.send(chunk, nil, 0, 0)
	}
	sig, streamBytes, err := s.signer.sign(s.signTokens)
	if err != nil {
		return err
	}
	return s.send(chunk, sig, streamBytes, s.signTokens)
}

func (s *ChatFrameSink) Close() error {
	if s == nil {
		return io.ErrClosedPipe
	}
	if s.gzip == nil {
		return s.sendErr
	}
	if s.signer != nil {
		if err := s.flushSigned(); err != nil {
			return err
		}
		if len(s.event) > 0 {
			if err := s.commit(s.event); err != nil {
				return err
			}
		}
	}
	s.pending = 0
	s.event = nil
	s.signTokens = s.totalTokens
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
