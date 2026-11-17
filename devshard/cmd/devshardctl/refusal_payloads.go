package main

import (
	"devshard/accounting"
	"devshard/host"
	"devshard/user"
)

const maxRefusalSweepAttempts = 3

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
	return result.Outcome == string(accounting.TimeoutVoteCollectionFailed) || result.Outcome == string(accounting.TimeoutDiffSendFailed)
}
