package transport

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
)

// chatGzipCoalesceMin is the uncompressed cut for one ChatFrame.
// 1024 bytes is about five vLLM token events (id, object, created, model, delta).
// BestSpeed keeps its match table from 128 bytes up; this cut is larger so one
// Huffman tree covers those tokens.
// chatGzipCoalesceMax bounds a stream that never sends a blank line.
const (
	chatGzipCoalesceMin = 1024
	chatGzipCoalesceMax = 16 << 10
)

// ChatGzipEmitter is one gzip.Writer (BestSpeed). Concatenating chunks
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
	e.gz, _ = gzip.NewWriterLevel(&e.buf, gzip.BestSpeed)
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
// The executor still flushes every scanner line. This sink holds those
// flushes and emits one gzip block when:
//   - at least chatGzipCoalesceMin uncompressed bytes are pending and they
//     end on an SSE event (\n\n), about five tokens, or
//   - pending bytes reach chatGzipCoalesceMax, so a missing blank line
//     cannot stall the stream, or
//   - Close, which is after [DONE] and devshard_meta. The short tail waits
//     for that instead of becoming its own block.
type ChatFrameSink struct {
	header  http.Header
	status  int
	gzip    *ChatGzipEmitter
	sendErr error
	pending int
	tail    [2]byte
}

func NewChatFrameSink(send func([]byte) error) *ChatFrameSink {
	return &ChatFrameSink{
		header: make(http.Header),
		gzip:   NewChatGzipEmitter(send),
	}
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
		s.note(p[:n])
	}
	if err != nil {
		return n, err
	}
	if s.pending >= chatGzipCoalesceMax {
		if ferr := s.flushGzip(); ferr != nil {
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

func (s *ChatFrameSink) eventReady() bool {
	return s.pending >= chatGzipCoalesceMin && s.tail[0] == '\n' && s.tail[1] == '\n'
}

func (s *ChatFrameSink) Flush() {
	_ = s.FlushErr()
}

// FlushErr emits a ChatFrame when the held SSE reaches chatGzipCoalesceMin
// on an event boundary, or chatGzipCoalesceMax. A shorter complete event
// stays buffered. http.Flusher.Flush cannot return stream.Send errors.
// The receipt calls FlushNow so a failed frame still returns before execution.
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
	if s.pending >= chatGzipCoalesceMax || s.eventReady() {
		return s.flushGzip()
	}
	return nil
}

// FlushNow sends the held bytes even when they are under chatGzipCoalesceMin.
// The receipt uses this so a failed frame returns before RunExecution.
func (s *ChatFrameSink) FlushNow() error {
	if s == nil || s.gzip == nil {
		return io.ErrClosedPipe
	}
	if s.sendErr != nil {
		return s.sendErr
	}
	return s.flushGzip()
}

func (s *ChatFrameSink) flushGzip() error {
	if s.pending == 0 {
		return nil
	}
	if err := s.gzip.Flush(); err != nil {
		s.sendErr = err
		return err
	}
	s.pending = 0
	return nil
}

func (s *ChatFrameSink) Close() error {
	if s == nil {
		return io.ErrClosedPipe
	}
	if s.gzip == nil {
		return s.sendErr
	}
	s.pending = 0
	err := s.gzip.Close()
	if s.sendErr != nil {
		return s.sendErr
	}
	if err != nil {
		s.sendErr = err
	}
	return err
}
