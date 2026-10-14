package user

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"connectrpc.com/connect"

	"devshard/host"
	"devshard/logging"
	"devshard/state"
	"devshard/storage"
	"devshard/transport"
	"devshard/types"
)

// A failed catch-up chunk is resent in place before the catch-up gives up.
// The host applies a chunk all or nothing and an identical resend is a no-op,
// so a resend never skips or doubles a nonce. The cursor moves only on a
// response, so a chunk that never succeeds is resent on the next run.
var (
	catchUpChunkAttempts  = 4
	catchUpChunkRetryBase = 250 * time.Millisecond
	catchUpChunkRetryMax  = 5 * time.Second
)

var errCatchUpClosed = errors.New("session closed")

// catchUpForSend returns the inline catch-up for a request to hostIdx that
// carries target. A host more than one chunk behind is first sent every nonce
// before target through chunked catch-up, so no request carries more than
// one chunk: the host applies target only after every nonce before it.
//
// The chunked catch-up runs on the session's lifetime context, not ctx: an
// attempt that is canceled stops waiting, but the host keeps being caught up
// for the next request. Concurrent sends to the same host share it.
func (s *Session) catchUpForSend(ctx context.Context, hostIdx int, target uint64) ([]types.Diff, error) {
	s.mu.Lock()
	diffs, ok := s.inlineCatchUpLocked(hostIdx, target)
	s.mu.Unlock()
	if ok {
		return diffs, nil
	}

	run := s.startHostCatchUp(hostIdx, target-1)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-run.done:
		if run.err != nil {
			return nil, run.err
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	diffs, ok = s.inlineCatchUpLocked(hostIdx, target)
	if !ok {
		return nil, fmt.Errorf("host %d is still more than %d diffs behind nonce %d after catch-up (cursor %d)",
			hostIdx, catchUpChunkSize, target, s.hostSyncNonce[hostIdx])
	}
	return diffs, nil
}

// hostCatchUpRun is one background catch-up of a host. err is set before done
// is closed.
type hostCatchUpRun struct {
	through uint64
	done    chan struct{}
	err     error
}

// startHostCatchUp brings hostIdx through `through` in the background. A send
// that arrives while a run for the host is still waiting to start joins it and
// raises its target, so a host that keeps failing costs one run at a time, not
// one per send.
func (s *Session) startHostCatchUp(hostIdx int, through uint64) *hostCatchUpRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sendCatchUpClosed {
		run := &hostCatchUpRun{done: make(chan struct{}), err: errCatchUpClosed}
		close(run.done)
		return run
	}
	if run := s.sendCatchUpNext[hostIdx]; run != nil {
		run.through = max(run.through, through)
		return run
	}
	if s.sendCatchUpCtx == nil {
		s.sendCatchUpCtx, s.sendCatchUpCancel = context.WithCancel(context.Background())
	}
	if s.sendCatchUpNext == nil {
		s.sendCatchUpNext = make(map[int]*hostCatchUpRun)
	}
	run := &hostCatchUpRun{through: through, done: make(chan struct{})}
	s.sendCatchUpNext[hostIdx] = run
	ctx := s.sendCatchUpCtx
	s.sendCatchUps.Add(1)
	s.noteCatchingUp(hostIdx, 1)
	go func() {
		defer s.sendCatchUps.Done()
		defer s.noteCatchingUp(hostIdx, -1)
		defer close(run.done)
		hostMu := s.hostCatchUpLock(hostIdx)
		hostMu.Lock()
		defer hostMu.Unlock()
		s.mu.Lock()
		delete(s.sendCatchUpNext, hostIdx)
		through := run.through
		s.mu.Unlock()
		run.err = s.sendCatchUpThrough(ctx, hostIdx, s.getFinalizeClients()[hostIdx], through)
	}()
	return run
}

func (s *Session) stopSendCatchUps() {
	s.mu.Lock()
	s.sendCatchUpClosed = true
	if s.sendCatchUpCancel != nil {
		s.sendCatchUpCancel()
	}
	s.mu.Unlock()
	s.sendCatchUps.Wait()
}

func (s *Session) noteCatchingUp(hostIdx int, delta int32) {
	if hostIdx >= 0 && hostIdx < len(s.catchingUp) {
		s.catchingUp[hostIdx].Add(delta)
	}
}

// HostCatchingUp reports whether a chunked catch-up to hostIdx is running.
// It takes no lock, so a session-picker chooser may call it under s.mu.
func (s *Session) HostCatchingUp(hostIdx int) bool {
	return hostIdx >= 0 && hostIdx < len(s.catchingUp) && s.catchingUp[hostIdx].Load() > 0
}

// OtherHostReady reports whether some participant other than hostIdx's has no
// chunked catch-up running. Lock-free like HostCatchingUp.
func (s *Session) OtherHostReady(hostIdx int) bool {
	if hostIdx < 0 || hostIdx >= len(s.group) {
		return false
	}
	addr := s.group[hostIdx].ValidatorAddress
	for i, slot := range s.group {
		if slot.ValidatorAddress != addr && !s.HostCatchingUp(i) {
			return true
		}
	}
	return false
}

// sendCatchUpChunk sends one chunk, resending it in place on a transient
// failure. Each try has its own catchUpChunkTimeout; a try that used the whole
// timeout is not repeated.
func (s *Session) sendCatchUpChunk(ctx context.Context, hostIdx int, client HostClient, chunk []types.Diff) (*host.HostResponse, error) {
	chunkNonce := chunk[len(chunk)-1].Nonce
	delay := catchUpChunkRetryBase
	for attempt := 1; ; attempt++ {
		chunkCtx, cancel := context.WithTimeout(ctx, catchUpChunkTimeout)
		resp, err := client.Send(chunkCtx, host.HostRequest{Diffs: chunk, Nonce: chunkNonce}, nil, nil)
		timedOut := chunkCtx.Err() != nil && ctx.Err() == nil
		cancel()
		if err == nil {
			return resp, nil
		}
		if attempt >= catchUpChunkAttempts || timedOut || ctx.Err() != nil || !catchUpChunkRetryable(err) {
			return nil, err
		}
		logging.Info("sendCatchUp chunk resend", "subsystem", "finalize", "escrow", s.escrowID,
			"host", s.HostLabel(hostIdx), "host_idx", hostIdx,
			"chunk_first_nonce", chunk[0].Nonce, "chunk_last_nonce", chunkNonce,
			"attempt", attempt, "storage_busy", transport.IsStorageBusy(err), "error", err)
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, err
		case <-t.C:
		}
		delay = min(delay*2, catchUpChunkRetryMax)
	}
}

// catchUpChunkRetryable is a failure an identical resend can clear: no
// response, a 5xx or 429 (storage_busy among them), or a Connect code for the
// same. A divergence, fork, or other 4xx would fail the same way again.
func catchUpChunkRetryable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if state.IsPostStateRootMismatchError(err) ||
		errors.Is(err, types.ErrStateHashMismatch) ||
		errors.Is(err, storage.ErrDiffFork) {
		return false
	}
	if transport.IsStorageBusy(err) {
		return true
	}
	var status *transport.UpstreamStatusError
	if errors.As(err, &status) {
		return status.StatusCode >= http.StatusInternalServerError || status.StatusCode == http.StatusTooManyRequests
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		switch ce.Code() {
		case connect.CodeUnavailable, connect.CodeResourceExhausted, connect.CodeInternal,
			connect.CodeUnknown, connect.CodeAborted, connect.CodeDeadlineExceeded:
			return true
		default:
			return false
		}
	}
	return true
}
