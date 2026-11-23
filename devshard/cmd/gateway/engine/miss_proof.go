package engine

import (
	"sync/atomic"

	"devshard/gatewayprotocol/streamevidence"
)

const (
	heldFinishUntaken int32 = iota
	heldFinishTaken
	heldFinishReleased
)

// MissProof is the evidence a verifier recomputes. See ../docs/race.md, "Error misses".
type MissProof struct {
	ResponsePayload []byte
	Complete        bool
	Truncated       bool

	finishTx        []byte
	releaseFinish   func()
	finishOwnership atomic.Int32
}

func (p *MissProof) releaseHeldFinish() {
	if p != nil && p.releaseFinish != nil && p.finishOwnership.Swap(heldFinishReleased) != heldFinishReleased {
		p.releaseFinish()
	}
}

func (p *MissProof) takeHeldFinish() {
	if p != nil {
		p.finishOwnership.CompareAndSwap(heldFinishUntaken, heldFinishTaken)
	}
}

func (p *MissProof) releaseUntakenFinish() {
	if p != nil && p.releaseFinish != nil && p.finishOwnership.CompareAndSwap(heldFinishUntaken, heldFinishReleased) {
		p.releaseFinish()
	}
}

func (p *MissProof) completeness() MissProofCompleteness {
	switch {
	case p == nil:
		return MissProofNone
	case p.Truncated:
		return MissProofTruncated
	case p.Complete:
		return MissProofWhole
	}
	return MissProofPartial
}

type missProver interface {
	missProof() (MissProof, bool)
	releaseMissProof()
}

type errorStreamRetainer struct {
	maxBytes  int
	lines     []string
	bytesHeld int
	complete  bool
	truncated bool
	released  bool
}

func newErrorStreamRetainer(maxBytes int) *errorStreamRetainer {
	return &errorStreamRetainer{maxBytes: maxBytes}
}

func (r *errorStreamRetainer) retain(events []byte) {
	if r == nil || r.released || r.truncated || len(events) == 0 {
		return
	}
	for _, line := range streamevidence.DataLines(events) {
		held := len(line) + 1
		if r.bytesHeld+held > r.maxBytes {
			r.truncated = true
			return
		}
		r.lines = append(r.lines, line)
		r.bytesHeld += held
		if streamevidence.IsDoneLine(line) {
			r.complete = true
		}
	}
}

func (r *errorStreamRetainer) truncate() {
	if r != nil {
		r.truncated = true
	}
}

func (r *errorStreamRetainer) release() {
	if r == nil {
		return
	}
	r.lines, r.bytesHeld, r.released = nil, 0, true
}

func (r *errorStreamRetainer) proof() (MissProof, bool) {
	if r == nil || len(r.lines) == 0 {
		return MissProof{}, false
	}
	payload, err := streamevidence.Payload(r.lines)
	if err != nil {
		return MissProof{}, false
	}
	if !streamevidence.ProvesErrorMiss(payload) {
		return MissProof{}, false
	}
	return MissProof{ResponsePayload: payload, Complete: r.complete, Truncated: r.truncated}, true
}
