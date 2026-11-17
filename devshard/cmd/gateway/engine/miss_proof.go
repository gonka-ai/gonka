package engine

import (
	"common/completionapi"
	"devshard/gatewayproto"
)

const maxMissProofBytes = 64 * 1024

// MissProof is the evidence a verifier recomputes. See ../docs/race.md, "Error misses".
type MissProof struct {
	ResponsePayload []byte
	Complete        bool
	Truncated       bool

	finish *gatewayproto.FinishHold
}

func (p *MissProof) releaseHeldFinish() {
	if p != nil {
		p.finish.Release()
	}
}

func (p *MissProof) takeHeldFinish() {
	if p != nil {
		p.finish.Take()
	}
}

func (p *MissProof) releaseUntakenFinish() {
	if p != nil {
		p.finish.ReleaseUntaken()
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
	retained *gatewayproto.EvidenceCollector
}

func newErrorStreamRetainer(maxBytes int) *errorStreamRetainer {
	return &errorStreamRetainer{retained: gatewayproto.NewEvidenceCollector(maxBytes, nil, nil)}
}

func (r *errorStreamRetainer) retain(events []byte) {
	if r != nil {
		r.retained.AppendEvents(events)
	}
}
func (r *errorStreamRetainer) truncate() {
	if r != nil {
		r.retained.MarkTruncated()
	}
}
func (r *errorStreamRetainer) release() {
	if r != nil {
		r.retained.Release()
	}
}

func (r *errorStreamRetainer) proof() (MissProof, bool) {
	if r == nil {
		return MissProof{}, false
	}
	payload := r.retained.Payload()
	if len(payload) == 0 {
		return MissProof{}, false
	}
	if _, isError := completionapi.IsTerminalErrorResponse(payload); !isError {
		return MissProof{}, false
	}
	return MissProof{ResponsePayload: payload, Complete: r.retained.Complete(), Truncated: r.retained.Truncated()}, true
}

func eventDataLines(events []byte) []string   { return gatewayproto.EventDataLines(events) }
func isDevshardEnvelopeLine(line string) bool { return gatewayproto.IsProtocolDataLine(line) }
