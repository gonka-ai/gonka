package transport

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
)

// ChatGzipEmitter is one gzip.Writer (BestSpeed) that sync-flushes per
// event into opaque chunks. Concatenating chunks reconstitutes the stream.
// Close with no Write emits nothing so a handler error before the first
// event is zero frames, not an empty gzip member.
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
type ChatFrameSink struct {
	header  http.Header
	status  int
	gzip    *ChatGzipEmitter
	sendErr error
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
	return s.gzip.Write(p)
}

func (s *ChatFrameSink) Flush() {
	_ = s.FlushErr()
}

// FlushErr flushes the gzip window and the current ChatFrame. http.Flusher.Flush
// cannot return stream.Send errors; writeSSEEvent uses this so a failed receipt
// frame fails ServeInference before RunExecution.
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
	if err := s.gzip.Flush(); err != nil {
		s.sendErr = err
		return err
	}
	return nil
}

func (s *ChatFrameSink) Close() error {
	if s == nil || s.gzip == nil {
		return s.sendErr
	}
	err := s.gzip.Close()
	if s.sendErr != nil {
		return s.sendErr
	}
	return err
}
