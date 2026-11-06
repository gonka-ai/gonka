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
	return s.catchUpForSendWith(ctx, hostIdx, target, nil)
}

// catchUpForSendWith is catchUpForSend that also runs onFault, once and on
// the caller's goroutine, when the chunked catch-up it waits on finds the host
// unreachable. See awaitHostCatchUp.
func (s *Session) catchUpForSendWith(ctx context.Context, hostIdx int, target uint64, onFault func()) ([]types.Diff, error) {
	s.mu.Lock()
	diffs, ok, err := s.inlineCatchUpLocked(hostIdx, target)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if ok {
		return diffs, nil
	}

	if err := s.awaitHostCatchUp(ctx, hostIdx, target-1, onFault); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	diffs, ok, err = s.inlineCatchUpLocked(hostIdx, target)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("host %d is still more than %d diffs behind nonce %d after catch-up (cursor %d)",
			hostIdx, catchUpChunkSize, target, s.hostSyncNonce[hostIdx])
	}
	return diffs, nil
}

// awaitHostCatchUp waits for hostIdx to be caught up through `through`.
// onFault, when set, runs at most once: when a chunk try to the host goes
// unanswered at the transport level, or at once if the host's last try did.
func (s *Session) awaitHostCatchUp(ctx context.Context, hostIdx int, through uint64, onFault func()) error {
	if refusal := s.hostCatchUpRefusal(hostIdx).standing(s.clock()); refusal != nil {
		return refusal
	}
	defer s.timeStep(StepHostCatchUp, time.Now())
	run := s.startHostCatchUp(hostIdx, through)
	var fault <-chan struct{}
	if onFault != nil {
		fault = s.hostCatchUpFault(hostIdx)
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-run.done:
			return run.err
		case <-fault:
			fault = nil
			onFault()
		}
	}
}

// hostCatchUpFault is closed at the first catch-up chunk try hostIdx leaves
// unanswered and replaced once a chunk lands, so it is per host, not per run:
// a send queued behind another run's turn hears of that run's faults too.
func (s *Session) hostCatchUpFault(hostIdx int) <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hostCatchUpFaultLocked(hostIdx)
}

func (s *Session) hostCatchUpFaultLocked(hostIdx int) chan struct{} {
	if s.catchUpFaults == nil {
		s.catchUpFaults = make(map[int]chan struct{})
	}
	fault, ok := s.catchUpFaults[hostIdx]
	if !ok {
		fault = make(chan struct{})
		s.catchUpFaults[hostIdx] = fault
	}
	return fault
}

// noteCatchUpTry records whether a chunk try reached hostIdx. See hostCatchUpFault.
func (s *Session) noteCatchUpTry(hostIdx int, unreachable bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fault := s.hostCatchUpFaultLocked(hostIdx)
	select {
	case <-fault:
		if !unreachable {
			s.catchUpFaults[hostIdx] = make(chan struct{})
		}
	default:
		if unreachable {
			close(fault)
		}
	}
}

// catchUpTurn is one host's chunked catch-up lock. It is a channel rather than
// a sync.Mutex so that a send waiting its turn is a durable block: inside a
// synctest bubble a goroutine parked on a mutex stops the fake clock, and the
// run holding the turn through a resend backoff would never wake.
type catchUpTurn chan struct{}

func (turn catchUpTurn) take() { turn <- struct{}{} }

func (turn catchUpTurn) tryTake() bool {
	select {
	case turn <- struct{}{}:
		return true
	default:
		return false
	}
}

func (turn catchUpTurn) give() { <-turn }

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
		turn := s.hostCatchUpLock(hostIdx)
		turn.take()
		defer turn.give()
		s.mu.Lock()
		delete(s.sendCatchUpNext, hostIdx)
		through := run.through
		s.mu.Unlock()
		client, release := s.finalizeClient(hostIdx)
		defer release()
		run.err = s.sendCatchUpThrough(ctx, hostIdx, client, through)
		s.hostCatchUpRefusal(hostIdx).record(ctx, run.err, s.clock())
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
			s.noteCatchUpTry(hostIdx, false)
			return resp, nil
		}
		if ctx.Err() == nil && catchUpHostUnreachable(err) {
			s.noteCatchUpTry(hostIdx, true)
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

// catchUpHostUnreachable is a chunk try the host never answered: no HTTP
// status and no Connect code a host sends. A host that answers, storage_busy
// included, is applying its backlog at its own pace and is not one.
func catchUpHostUnreachable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || transport.IsStorageBusy(err) {
		return false
	}
	var status *transport.UpstreamStatusError
	if errors.As(err, &status) {
		return false
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce.Code() == connect.CodeUnavailable || ce.Code() == connect.CodeUnknown
	}
	return true
}

// catchUpChunkRetryable is a failure an identical resend can clear: no
// response, a 5xx or 429 (storage_busy among them), or a Connect code for the
// same. A divergence, fork, other 4xx, or a host absent from the chain would
// fail the same way again.
func catchUpChunkRetryable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, ErrHostAbsentFromChain) {
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
