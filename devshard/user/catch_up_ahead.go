package user

import (
	"context"
	"errors"
	"fmt"
	"time"

	"devshard/host"
	"devshard/logging"
	"devshard/types"
)

const (
	defaultVerifierGateWait = 2 * time.Second
	verifierDrainBudget     = 30 * time.Second
	catchUpChunkTimeout     = 60 * time.Second
)

var (
	ErrRequestTooLargeForHost = errors.New("request exceeds host body budget")
	ErrPromptTooLargeForHost  = fmt.Errorf("prompt alone exceeds host body budget: %w", ErrRequestTooLargeForHost)
	ErrTailTooLargeForHost    = fmt.Errorf("undrainable tail exceeds host body budget: %w", ErrRequestTooLargeForHost)
	ErrCatchUpNotStarted      = errors.New("catch-up did not start")
)

func newCatchUpGates(hostCount int) []chan struct{} {
	gates := make([]chan struct{}, hostCount)
	for i := range gates {
		gates[i] = make(chan struct{}, 1)
	}
	return gates
}

func (s *Session) diffsUpToNonceLocked(hostIdx int, targetNonce uint64) []types.Diff {
	lastSent := s.hostSyncNonce[hostIdx]
	if targetNonce > 0 && lastSent >= targetNonce {
		lastSent = targetNonce - 1
	}
	var tail []types.Diff
	for _, diff := range s.diffs {
		if diff.Nonce > lastSent && diff.Nonce <= targetNonce {
			tail = append(tail, diff)
		}
	}
	return tail
}

func (s *Session) tailToSendLocked(hostIdx int, targetNonce uint64, reserveBytes, budgetBytes int) ([]types.Diff, bool) {
	tail := s.diffsUpToNonceLocked(hostIdx, targetNonce)
	fits := catchUpFitsOneBody(tail, reserveBytes, budgetBytes)
	if fits && len(tail) > 0 {
		s.validateCatchUp(tail, targetNonce, hostIdx)
	}
	return tail, fits
}

func (s *Session) deliverCatchUpChunk(ctx context.Context, hostIdx int, client HostClient, chunk []types.Diff) (*host.HostResponse, error) {
	chunkNonce := chunk[len(chunk)-1].Nonce
	chunkCtx, cancel := context.WithTimeout(ctx, catchUpChunkTimeout)
	resp, err := client.Send(chunkCtx, host.HostRequest{Diffs: chunk, Nonce: chunkNonce}, nil, nil)
	cancel()
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.processResponse(hostIdx, resp, chunkNonce); err != nil {
		return nil, err
	}
	s.logSignatureProgress(resp.Nonce)
	return resp, nil
}

func (s *Session) enterCatchUpGate(ctx context.Context, hostIdx int, gateWait time.Duration) (func(), error) {
	waitCtx := ctx
	if gateWait > 0 {
		bounded, cancel := context.WithTimeout(ctx, gateWait)
		defer cancel()
		waitCtx = bounded
	}
	select {
	case s.catchUpGate[hostIdx] <- struct{}{}:
		return func() { <-s.catchUpGate[hostIdx] }, nil
	case <-waitCtx.Done():
		return nil, fmt.Errorf("host %d: %w: %w", hostIdx, ErrCatchUpNotStarted, waitCtx.Err())
	}
}

func (s *Session) verifierCatchUpTail(ctx context.Context, hostIdx int, reserveBytes int) ([]types.Diff, bool) {
	s.mu.Lock()
	targetNonce := s.nonce
	s.mu.Unlock()
	if targetNonce == 0 {
		return nil, false
	}

	drainCtx, cancel := context.WithTimeout(ctx, verifierDrainBudget)
	defer cancel()
	tail, err := s.catchUpTailForHost(drainCtx, hostIdx, targetNonce, reserveBytes, defaultVerifierGateWait)
	if err != nil {
		logging.Warn("verifier catch-up ahead failed", "subsystem", "session",
			"escrow", s.escrowID, "host", hostIdx, "target_nonce", targetNonce, "error", err)
		return nil, false
	}
	return tail, true
}

func (s *Session) catchUpTailForHost(ctx context.Context, hostIdx int, targetNonce uint64, reserveBytes int, gateWait time.Duration) ([]types.Diff, error) {
	s.mu.Lock()
	budgetBytes := s.catchUpBudgetLocked()
	tail, fits := s.tailToSendLocked(hostIdx, targetNonce, reserveBytes, budgetBytes)
	s.mu.Unlock()
	if fits {
		return tail, nil
	}
	if reserveBytes >= budgetBytes {
		return nil, ErrPromptTooLargeForHost
	}

	release, err := s.enterCatchUpGate(ctx, hostIdx, gateWait)
	if err != nil {
		return nil, err
	}
	defer release()

	client := s.finalizeClientFor(hostIdx)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		s.mu.Lock()
		tail, fits = s.tailToSendLocked(hostIdx, targetNonce, reserveBytes, budgetBytes)
		s.mu.Unlock()
		if fits {
			return tail, nil
		}

		drainableDiffs := tail
		for len(drainableDiffs) > 0 && drainableDiffs[len(drainableDiffs)-1].Nonce >= targetNonce {
			drainableDiffs = drainableDiffs[:len(drainableDiffs)-1]
		}
		if len(drainableDiffs) == 0 {
			return nil, ErrTailTooLargeForHost
		}

		chunk, err := catchUpChunk(drainableDiffs, budgetBytes)
		if err != nil {
			return nil, err
		}
		chunkNonce := chunk[len(chunk)-1].Nonce
		logging.Info("ensureHostCaughtUp chunk", "subsystem", "finalize", "escrow", s.escrowID,
			"nonce", chunkNonce, "host", hostIdx, "target_nonce", targetNonce,
			"diffs_in_chunk", len(chunk), "tail_diffs", len(tail))

		resp, err := s.deliverCatchUpChunk(ctx, hostIdx, client, chunk)
		s.publishHeightSyncView()
		if err != nil {
			return nil, fmt.Errorf("catch-up ahead of nonce %d to host %d: %w", targetNonce, hostIdx, err)
		}
		if resp.Nonce < chunkNonce {
			return nil, fmt.Errorf("catch-up ahead stalled at nonce %d on host %d", resp.Nonce, hostIdx)
		}
	}
}
