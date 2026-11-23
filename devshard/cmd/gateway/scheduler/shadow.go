package scheduler

import "time"

const shadowHold = time.Second

// ShadowProber sends the probe a shadow participant's nonce is spent on. See routing.md, "Shadow participants".
type ShadowProber interface {
	// Params builds the probe's dispatch params from the profile of a live client request the drain is serving; StartedAt is in seconds. A non-zero skip says no probe can be built from them.
	Params(request *RequestProfile, startedAt int64) (params any, skip ProbeSkip)
	// Probe takes ownership of the committed nonce and releases the assignment's escrow hold once it holds its own; false leaves the hold to the caller.
	Probe(request ProbeRequest) bool
}

// ProbeRequest is a committed nonce bound to a shadow participant, with the params it was committed with.
type ProbeRequest struct {
	Assignment Assignment
	Model      string
	Params     any
}

func (d *dispatcher) shadowDecision() Decision {
	if d.shadow != nil {
		for _, queued := range d.waiting {
			if queued.abandoned.Load() {
				continue
			}
			profile := queued.profile
			if params, skip := d.shadow.Params(&profile, d.now().Unix()); skip == ProbeSkipNone {
				return probe{params: params}
			}
		}
	}
	return hold{until: d.now().Add(shadowHold)}
}

func (d *dispatcher) sendProbe(sent probe, taken reservation, prepared Prepared) {
	if prepared == nil {
		taken.releaseHold()
		return
	}
	assignment := Assignment{Escrow: d.escrowID, Host: taken.participant, Nonce: prepared, EscrowHold: taken.escrowHold}
	if d.shadow.Probe(ProbeRequest{Assignment: assignment, Model: d.model, Params: sent.params}) {
		return
	}
	d.recordGhost(Burn{Nonce: prepared.Nonce(), Participant: taken.participant, Reason: ghostUntrusted.reason()})
	taken.releaseHold()
}
