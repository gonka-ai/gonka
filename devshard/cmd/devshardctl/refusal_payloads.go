package main

import (
	"context"
	"time"

	"devshard/accounting"
	"devshard/chainoracle/blocks/headertime"
	"devshard/host"
	"devshard/user"
)

const maxRefusalSweepAttempts = 3

// softTimeoutRetryPause is the gap between a soft timeout vote failure and the
// next VoteTimeout. The first attempt may open on the gateway wall clock while
// verifiers still miss the stamp row (AnswerWait → feed_behind → reject). Wait
// on the map/floor refusal clock, up to AnswerWait, so a later attempt can see
// the same height the first call lacked.
func softTimeoutRetryPause(ctx context.Context, session *user.Session, nonce uint64) error {
	if session == nil {
		return ctx.Err()
	}
	limit := time.Now().Add(headertime.AnswerWait)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, deadline := session.TimeoutDeadline(nonce, time.Time{})
		now := time.Now()
		if !now.Before(limit) {
			return nil
		}
		wake := deadline
		if !wake.After(now) {
			// Already open on the map clock: brief yield so a re-vote does not
			// stack on a verifier answer hold from the previous round.
			wake = now.Add(250 * time.Millisecond)
		}
		if wake.After(limit) {
			wake = limit
		}
		timer := time.NewTimer(wake.Sub(now))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
			if !deadline.After(time.Now()) || !time.Now().Before(limit) {
				return nil
			}
		}
	}
}

type keptRefusal struct {
	payload        *host.InferencePayload
	failedAttempts int
}

func (e *Redundancy) rememberRefusalPayload(nonce uint64, payload *host.InferencePayload) {
	if e == nil || payload == nil {
		return
	}
	e.refusalPayloadMu.Lock()
	defer e.refusalPayloadMu.Unlock()
	if e.refusalPayloads == nil {
		e.refusalPayloads = make(map[uint64]*keptRefusal)
	}
	e.refusalPayloads[nonce] = &keptRefusal{payload: payload}
}

func (e *Redundancy) refusalPayload(nonce uint64) (*host.InferencePayload, bool) {
	if e == nil {
		return nil, false
	}
	e.refusalPayloadMu.Lock()
	defer e.refusalPayloadMu.Unlock()
	kept, found := e.refusalPayloads[nonce]
	if !found {
		return nil, false
	}
	return kept.payload, true
}

func (e *Redundancy) hasRefusalPayload(nonce uint64) bool {
	_, kept := e.refusalPayload(nonce)
	return kept
}

func (e *Redundancy) forgetRefusalPayload(nonce uint64) {
	if e == nil {
		return
	}
	e.refusalPayloadMu.Lock()
	defer e.refusalPayloadMu.Unlock()
	delete(e.refusalPayloads, nonce)
}

func (e *Redundancy) noteRefusalSweepFailed(nonce uint64) {
	if e == nil {
		return
	}
	e.refusalPayloadMu.Lock()
	defer e.refusalPayloadMu.Unlock()
	kept, found := e.refusalPayloads[nonce]
	if !found {
		return
	}
	kept.failedAttempts++
	if kept.failedAttempts >= maxRefusalSweepAttempts {
		delete(e.refusalPayloads, nonce)
	}
}

func (e *Redundancy) forgetRefusalPayloadsUnless(stillPending func(nonce uint64) bool) {
	if e == nil {
		return
	}
	e.refusalPayloadMu.Lock()
	defer e.refusalPayloadMu.Unlock()
	for nonce := range e.refusalPayloads {
		if !stillPending(nonce) {
			delete(e.refusalPayloads, nonce)
		}
	}
}

func timeoutVoteUnjudged(result user.TimeoutResult) bool {
	switch result.Outcome {
	case string(accounting.TimeoutVoteCollectionFailed),
		string(accounting.TimeoutDiffSendFailed),
		string(accounting.TimeoutInsufficientVotes):
		// insufficient_votes includes verifier feed_behind / not-due rejects
		// after AnswerWait; keep the payload and retry once clocks can align.
		return true
	default:
		return false
	}
}
