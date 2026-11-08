package user

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"devshard/logging"
	"devshard/types"
)

var validationPollBackoff = [...]time.Duration{time.Second, 2 * time.Second, 5 * time.Second}

type mempoolFetcher interface {
	GetMempool(context.Context) ([]*types.DevshardTx, error)
}

type validationProgressRun struct {
	ctx      context.Context
	cancel   context.CancelFunc
	wake     chan struct{}
	progress uint64 // Protected by Session.mu; only changes to a dispute advance it.
}

// GetMempool follows the same host catch-up and eligibility path as the network query.
func (c *InProcessClient) GetMempool(ctx context.Context) ([]*types.DevshardTx, error) {
	if err := c.Host.CatchUpFromStore(ctx); err != nil {
		logging.Debug("validation mempool catch-up failed", "error", err)
	}
	c.Host.EnqueueDueValidations()
	return c.Host.MempoolTxs(), nil
}

func validationMessage(tx *types.DevshardTx) bool {
	return tx != nil && (tx.GetValidation() != nil || tx.GetValidationVote() != nil)
}

func (s *Session) hasPendingDisputeMessagesLocked() bool {
	for _, tx := range s.pendingTxs {
		if validationMessage(tx) && (s.validationProgressNeeded || tx.GetValidationVote() != nil || !tx.GetValidation().Valid) {
			return true
		}
	}
	return false
}

type validationProgressMark struct {
	status         types.InferenceStatus
	valid, invalid uint32
}

// Capture only records named by validation messages, never the whole escrow.
func (s *Session) validationProgressMarksLocked(txs []*types.DevshardTx) map[uint64]validationProgressMark {
	var marks map[uint64]validationProgressMark
	for _, tx := range txs {
		if !validationMessage(tx) {
			continue
		}
		id, ok := hostTxInferenceID(tx)
		if !ok {
			continue
		}
		if _, seen := marks[id]; seen {
			continue
		}
		if marks == nil {
			marks = make(map[uint64]validationProgressMark)
		}
		mark := validationProgressMark{}
		if rec, exists := s.sm.Inference(id); exists {
			mark = validationProgressMark{status: rec.Status, valid: rec.VotesValid, invalid: rec.VotesInvalid}
		}
		marks[id] = mark
	}
	return marks
}

func (s *Session) noteValidationDiffLocked(txs []*types.DevshardTx, before map[uint64]validationProgressMark) {
	changed := false
	for id, after := range s.validationProgressMarksLocked(txs) {
		prior := before[id]
		// Applied may include an idempotent validation or a vote on unrelated
		// work. Only a changed dispute should shorten the next poll.
		if (prior.status == types.StatusChallenged || after.status == types.StatusChallenged) && prior != after {
			changed = true
			break
		}
	}
	if !changed {
		return
	}
	s.validationProgressNeeded = true
	if run := s.validationProgress; run != nil {
		run.progress++
	}
	s.wakeValidationProgressLocked()
}

func (s *Session) validationProgressAllowedLocked() bool {
	return !s.validationProgressClosed && !s.finalizeInFlight.Load() && s.sm.Phase() == types.PhaseActive && !s.reservedForFinalizationLocked(s.nonce+1)
}

func (s *Session) wakeValidationProgressLocked() {
	if !s.validationProgressAllowedLocked() || (!s.validationProgressNeeded && !s.hasPendingDisputeMessagesLocked()) {
		return
	}
	if run := s.validationProgress; run != nil {
		select {
		case run.wake <- struct{}{}:
		default:
		}
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	run := &validationProgressRun{ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1)}
	s.validationProgress = run
	s.validationProgressWG.Add(1)
	go s.runValidationProgress(run)
}

func (s *Session) validationWorkRemainingLocked() bool {
	_, counts := s.sm.InferenceStatusCounts()
	s.validationProgressNeeded = counts[types.StatusChallenged] > 0
	return s.validationProgressNeeded || s.hasPendingDisputeMessagesLocked()
}

func (s *Session) runValidationProgress(run *validationProgressRun) {
	defer s.validationProgressWG.Done()
	defer run.cancel()
	defer func() {
		s.mu.Lock()
		if s.validationProgress == run {
			s.validationProgress = nil
		}
		s.mu.Unlock()
	}()
	progressState := func() (uint64, bool) {
		s.mu.Lock()
		defer s.mu.Unlock()
		more := s.validationProgressAllowedLocked() && s.validationWorkRemainingLocked()
		if !more && s.validationProgress == run {
			s.validationProgress = nil
		}
		return run.progress, more
	}
	deliver := func() bool {
		_, err := s.drainValidationProgress(run.ctx)
		if errors.Is(err, types.ErrNonceLimitExceeded) || errors.Is(err, types.ErrInsufficientBalance) {
			return false
		}
		if err != nil && run.ctx.Err() == nil {
			logging.Debug("validation delivery deferred", "escrow", s.escrowID, "error", err)
		}
		return true
	}
	step := 0
	var seenProgress uint64
poll:
	for run.ctx.Err() == nil {
		if _, more := progressState(); !more || !deliver() {
			return
		}
		s.collectValidationMessages(run)
		progress, more := progressState()
		if !more {
			return
		}
		if progress != seenProgress {
			seenProgress = progress
			step = 0
		}
		deadline := time.Now().Add(validationPollBackoff[step])
		timer := time.NewTimer(time.Until(deadline))
		for {
			select {
			case <-run.ctx.Done():
				timer.Stop()
				return
			case <-run.wake:
				// A response can deliver a ready vote while the poll timer runs.
				// Ordinary traffic and duplicate gossip must not restart polling.
				if !deliver() {
					timer.Stop()
					return
				}
				progress, more = progressState()
				if !more {
					timer.Stop()
					return
				}
				if progress != seenProgress {
					seenProgress = progress
					step = 0
					earlier := time.Now().Add(validationPollBackoff[0])
					if earlier.Before(deadline) {
						deadline = earlier
						timer.Reset(time.Until(deadline))
					}
				}
			case <-timer.C:
				if step < len(validationPollBackoff)-1 {
					step++
				}
				continue poll
			}
		}
	}
}

// Preview with the normal state machine before composing: rejected or deferred
// messages must not cause an empty paid nonce in a quiet session.
func (s *Session) drainValidationProgress(ctx context.Context) (bool, error) {
	s.mu.Lock()
	if !s.validationProgressAllowedLocked() || !s.hasPendingDisputeMessagesLocked() {
		s.mu.Unlock()
		return false, nil
	}
	pending, held := s.splitPendingForComposeLocked(nil)
	sort.SliceStable(pending, func(i, j int) bool { return txPriority(pending[i]) < txPriority(pending[j]) })
	preview, err := s.sm.PreviewLocalBestEffort(s.nonce+1, pending)
	if err != nil {
		s.mu.Unlock()
		return false, err
	}
	hasVote := false
	for _, tx := range preview.Applied {
		hasVote = hasVote || validationMessage(tx)
	}
	if !hasVote {
		s.retainPendingLocked(append(append(held, preview.Deferred...), preview.Applied...), nil)
		s.mu.Unlock()
		return false, nil
	}
	span, err := s.drainUnpinnedPendingLocked()
	s.mu.Unlock()
	s.publishHeightSyncView()
	sendErr := s.dispatchDrainedDiffs(ctx, span)
	return len(span) > 0, errors.Join(err, sendErr)
}

func (s *Session) collectValidationMessages(run *validationProgressRun) bool {
	ctx, cancel := context.WithCancel(run.ctx)
	defer cancel()
	s.mu.Lock()
	allowed := s.validationProgressAllowedLocked() && s.validationWorkRemainingLocked()
	s.mu.Unlock()
	if !allowed {
		return false
	}
	type reply struct {
		idx int
		txs []*types.DevshardTx
		err error
	}
	hosts := s.uniquePhysicalHosts()
	clients := s.getFinalizeClients()
	replies := make(chan reply, len(hosts))
	var wg sync.WaitGroup
	defer wg.Wait()
	defer cancel()
	for _, target := range hosts {
		wg.Go(func() {
			hostCtx, stop := context.WithTimeout(ctx, catchUpChunkTimeout)
			defer stop()
			result := reply{idx: target.idx}
			defer func() { replies <- result }()
			if err := s.verifierQueue.acquire(hostCtx, target.addr); err != nil {
				result.err = err
				return
			}
			defer s.verifierQueue.release(target.addr)
			client := clients[target.idx]
			if err := s.sendCatchUpWith(hostCtx, target.idx, client); err != nil {
				result.err = err
				return
			}
			if fetcher, ok := client.(mempoolFetcher); ok {
				result.txs, result.err = fetcher.GetMempool(hostCtx)
			} else {
				result.err = s.refreshPendingTxsFromHost(hostCtx, target, client)
			}
		})
	}
	changed := false
	for left := len(hosts); left > 0; {
		select {
		case <-ctx.Done():
			return changed
		case <-run.wake:
		case result := <-replies:
			left--
			if result.err != nil && ctx.Err() == nil {
				logging.Debug("validation mempool query failed", "escrow", s.escrowID, "host", result.idx, "error", result.err)
			}
			s.mu.Lock()
			for _, tx := range result.txs {
				if validationMessage(tx) {
					s.addPendingFromHostLocked(result.idx, nil, tx)
				}
			}
			s.mu.Unlock()
		}
		madeDiff, err := s.drainValidationProgress(ctx)
		changed = changed || madeDiff
		if err != nil && ctx.Err() == nil {
			logging.Debug("validation delivery deferred", "escrow", s.escrowID, "error", err)
		}
		s.mu.Lock()
		more := s.validationProgressAllowedLocked() && s.validationWorkRemainingLocked()
		s.mu.Unlock()
		if !more {
			return changed
		}
	}
	return changed
}

func (s *Session) stopValidationProgress() {
	s.mu.Lock()
	s.validationProgressClosed = true
	if run := s.validationProgress; run != nil {
		run.cancel()
	}
	s.mu.Unlock()
	s.validationProgressWG.Wait()
}
