package api

import (
	"cmp"
	"context"
	"errors"
	"sync"

	"devshard/cmd/gateway/engine"
)

func (c *clientStream) recordTo(log *streamLog) { c.log = log }

func (c *clientStream) detachSocket() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.log.detachSocket()
}

func (c *clientStream) rewriteFailure() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rewriteErr
}

func (c *clientStream) departed(ctx context.Context, cause error) bool {
	if errors.Is(cause, engine.ErrReplyRefused) || errors.Is(cause, engine.ErrStopped) {
		return false
	}
	return ctx.Err() != nil || !c.log.socketAttached()
}

func (c *clientStream) departedVerdict() string {
	if written, _ := c.delivered(); written > 0 {
		return deliveryFailedMidStream
	}
	return deliveryFailedBeforeFirstByte
}

func (c *clientStream) finishDetached(outcome engine.RaceOutcome) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	defer func() { c.closed = true }()
	if failure := outcome.Failure(); failure != nil {
		_ = c.failStreamLocked(failure)
		return
	}
	_ = c.closeStreamLocked()
}

type outcomeRelay struct {
	mu      sync.Mutex
	settle  func(engine.RaceOutcome)
	outcome *engine.RaceOutcome
	left    bool
}

func (s *Server) newOutcomeRelay(client *clientStream, log *streamLog, requestID, model string) *outcomeRelay {
	if log == nil {
		return nil
	}
	return &outcomeRelay{settle: func(final engine.RaceOutcome) {
		client.finishDetached(final)
		s.concludeStream(log, requestID, model, final.EscrowID, cmp.Or(final.Failure(), client.rewriteFailure()))
	}}
}

func (r *outcomeRelay) report(outcome engine.RaceOutcome) {
	if r == nil {
		return
	}
	r.mu.Lock()
	if !r.left {
		r.outcome = &outcome
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()
	r.settle(outcome)
}

func (r *outcomeRelay) leave() {
	r.mu.Lock()
	r.left = true
	reported := r.outcome
	r.mu.Unlock()
	if reported != nil {
		r.settle(*reported)
	}
}
