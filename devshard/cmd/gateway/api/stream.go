package api

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"net/http"
	"sync"

	json "github.com/goccy/go-json"

	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/filters"
)

// See README.md, "Streaming the reply".
const (
	maxBufferedResponseBytes = filters.MaxStreamCarryBytes
)

var sseEventSeparator = []byte("\n\n")

type clientStream struct {
	// An attempt goroutine can still write after the handler returned, when Go invalidates the writer.
	mu     sync.Mutex
	closed bool

	writer            http.ResponseWriter
	controller        *http.ResponseController
	requestID         string
	streaming         bool
	logprobs          filters.LogprobIntent
	rewriter          *filters.StreamRewriter
	folder            *filters.BodyFolder
	budget            *BufferBudget
	charged           int64
	overBudget        bool
	written           int64
	started           bool
	terminated        bool
	pendingEvent      bool
	replayRefusedBy   error
	answeredHostError *engine.HostApplicationError
}

func newClientStream(w http.ResponseWriter, requestID string, streaming, usage bool, logprobs filters.LogprobIntent, promptTokens uint64, budget *BufferBudget) *clientStream {
	stream := &clientStream{
		writer:     w,
		controller: http.NewResponseController(w),
		requestID:  requestID,
		streaming:  streaming,
		logprobs:   logprobs,
		budget:     budget,
	}
	if streaming {
		stream.rewriter = filters.NewStreamRewriter(logprobs, usage, promptTokens)
	} else {
		stream.folder = filters.NewBodyFolder(logprobs, promptTokens)
	}
	return stream
}

func (c *clientStream) Header() http.Header { return c.writer.Header() }

func (c *clientStream) delivered() (written int64, terminated bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.written, c.terminated
}

func (c *clientStream) Write(chunk []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return len(chunk), nil
	}
	if !c.streaming {
		written, err := c.folder.Write(chunk)
		if err != nil {
			return 0, err
		}
		if held := c.folder.Held(); held > maxBufferedResponseBytes {
			return 0, fmt.Errorf("buffered response exceeds %d bytes", maxBufferedResponseBytes)
		} else if owed := held - c.charged; owed > 0 {
			if !c.budget.reserve(owed) {
				c.overBudget = true
				return 0, ErrResponseBufferFull
			}
			c.charged += owed
		}
		return written, nil
	}
	c.beginLocked("text/event-stream")
	rewritten, err := c.rewriter.Write(chunk)
	if err != nil {
		return 0, err
	}
	if err := c.emitHostEventsLocked(rewritten); err != nil {
		return 0, err
	}
	return len(chunk), nil
}

func (c *clientStream) Flush() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.flushLocked()
}

func (c *clientStream) flushLocked() {
	if c.started && !c.closed {
		_ = c.controller.Flush()
	}
}

func (c *clientStream) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	defer func() { c.closed = true }()
	if c.streaming {
		tailErr := c.flushTailLocked()
		if c.answeredHostError != nil {
			if _, err := c.emitLocked(errorEvent(c.answeredHostError)); err != nil {
				return err
			}
		}
		if err := c.terminateLocked(); err != nil {
			return err
		}
		return tailErr
	}
	if c.overBudget {
		writeError(c.writer, http.StatusServiceUnavailable, ErrResponseBufferFull.Error())
		c.started = true
		return ErrResponseBufferFull
	}
	folded := c.folder.Body()
	status, reply, hostErr := assembleReply(folded)
	if hostErr != nil {
		c.answeredHostError = hostErr
		if refusal := filters.CacheRefusal(status, folded); refusal != filters.CacheStorable {
			c.replayRefusedBy = hostBodyRefusedError{refusal: refusal}
		}
	}
	c.beginStatusLocked("application/json", status)
	written, err := c.writer.Write(reply)
	c.written += int64(written)
	return err
}

func (c *clientStream) discard() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	c.budget.release(c.charged)
	c.charged = 0
	if c.folder != nil {
		c.folder.Discard()
	}
}

func (c *clientStream) closeUnstarted() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started {
		return false
	}
	c.closed = true
	return true
}

func (c *clientStream) Fail(cause error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || !c.streaming || !c.started {
		return nil
	}
	defer func() { c.closed = true }()
	tailErr := c.flushTailLocked()
	if _, err := c.emitLocked(errorEvent(cause)); err != nil {
		return err
	}
	if errors.Is(cause, engine.ErrAnswerTruncated) {
		c.flushLocked()
		return tailErr
	}
	if err := c.terminateLocked(); err != nil {
		return err
	}
	return tailErr
}

func (c *clientStream) flushTailLocked() error {
	tail, err := c.rewriter.Close()
	if writeErr := c.emitHostEventsLocked(tail); writeErr != nil {
		return writeErr
	}
	return err
}

func (c *clientStream) emitHostEventsLocked(rewritten []byte) error {
	if c.rewriter.LastOutputMayCarryUpstreamError() {
		kept, failure, dropped := filters.DropUpstreamErrorEvents(rewritten)
		if dropped && c.answeredHostError == nil {
			c.answeredHostError = hostErrorFrom(failure)
		}
		rewritten = kept
	}
	forwarded := filters.TrimSSEDone(rewritten)
	if len(forwarded) == 0 {
		return nil
	}
	_, err := c.emitLocked(forwarded)
	return err
}

func (c *clientStream) terminateLocked() error {
	var err error
	if !c.terminated {
		_, err = c.emitLocked(filters.SSEDoneEvent)
	}
	c.flushLocked()
	return err
}

func (c *clientStream) emitLocked(events []byte) (int, error) {
	c.beginLocked("text/event-stream")
	// A host's last event without its blank line would otherwise have the next one glued onto it.
	if c.pendingEvent {
		separator, err := c.writer.Write(sseEventSeparator)
		c.written += int64(separator)
		if err != nil {
			return 0, err
		}
	}
	c.terminated = c.terminated || filters.HasSSEDone(events)
	c.pendingEvent = !filters.SSEEventTerminated(events)
	written, err := c.writer.Write(events)
	c.written += int64(written)
	return written, err
}

func errorEvent(cause error) []byte {
	payload, err := json.Marshal(errorEnvelope{Error: detailForError(cause)})
	if err != nil {
		return nil
	}
	return append(append([]byte("data: "), payload...), '\n', '\n')
}

// One place for every chat reply header, so a cache replay and a live race cannot answer differently.
func writeChatHeaders(header http.Header, requestID, escrowID, contentType string, streaming bool) {
	if requestID != "" {
		header.Set(RequestIDHeader, requestID)
	}
	if escrowID != "" {
		header.Set(EscrowHeader, escrowID)
	}
	header.Set("Content-Type", cmp.Or(contentType, "application/json"))
	if streaming {
		header.Set("Cache-Control", "no-cache")
		header.Set("Connection", "keep-alive")
	}
}

func assembleReply(body []byte) (status int, reply []byte, hostErr *engine.HostApplicationError) {
	if bytes.Equal(body, filters.TruncatedResponseBody) || bytes.Equal(body, filters.NoResponseDataBody) {
		return http.StatusBadGateway, body, nil
	}
	failure, carried := filters.UpstreamFailure(body)
	if !carried {
		return http.StatusOK, body, nil
	}
	hostErr = hostErrorFrom(failure)
	reply, err := json.Marshal(errorEnvelope{Error: detailForError(hostErr)})
	if err != nil {
		return http.StatusBadGateway, filters.NoResponseDataBody, hostErr
	}
	return hostErr.HTTPStatus(), reply, hostErr
}

func hostErrorFrom(failure filters.UpstreamError) *engine.HostApplicationError {
	return &engine.HostApplicationError{Code: failure.Code, Type: failure.Type, Message: failure.Message}
}

func (c *clientStream) replayRefusal() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.replayRefusedBy
}

func (c *clientStream) answeredHostFailure() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.answeredHostError == nil {
		return nil
	}
	return c.answeredHostError
}

func (c *clientStream) beginLocked(contentType string) {
	c.beginStatusLocked(contentType, http.StatusOK)
}

func (c *clientStream) beginStatusLocked(contentType string, status int) {
	if c.started {
		return
	}
	writeChatHeaders(c.writer.Header(), c.requestID, "", contentType, c.streaming)
	c.writer.WriteHeader(status)
	c.started = true
}
