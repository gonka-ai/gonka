package warmup

import (
	"context"
	"time"

	"devshard/cmd/gateway/engine"
	"devshard/user"
)

type Timeouts interface {
	RecordTimeout(event engine.TimeoutEvent)
}

// Voter settles a nonce the gateway committed outside the scheduler's serving path, which nothing else ever would.
type Voter struct {
	Posters  Posters
	Timeouts Timeouts
}

// Settle resolves a nonce no reply finished and reports the vote it posted; voted is false when no poster could be resolved, which it records as skipped.
func (v Voter) Settle(ctx context.Context, escrowID, model string, params user.InferenceParams, nonce uint64, acknowledged bool) (posted engine.TimeoutEvent, voted bool) {
	if v.Posters == nil {
		return engine.TimeoutEvent{}, false
	}
	kind := engine.TimeoutKindRefused
	if acknowledged {
		kind = engine.TimeoutKindExecution
	}
	event := engine.TimeoutEvent{
		EscrowID: escrowID,
		Model:    model,
		Nonce:    nonce,
		Kind:     kind,
		Action:   engine.TimeoutActionStarted,
	}

	poster, resolved := v.Posters(escrowID, params)
	if !resolved {
		v.recordTimeout(engine.TimeoutEvent{
			EscrowID: escrowID, Model: model, Nonce: nonce, Kind: kind,
			Action: engine.TimeoutActionSkipped, Reason: engine.TimeoutReasonNoPoster,
		})
		return engine.TimeoutEvent{}, false
	}
	v.recordTimeout(event)

	posted = event
	vote, err := poster.SettleTimeout(ctx, engine.TimeoutStep{Nonce: nonce, StartedAt: time.Unix(params.StartedAt, 0)})
	posted.Action, posted.Reason = engine.TimeoutOutcome(vote, err, false)
	v.recordTimeout(posted)
	return posted, true
}

func (v Voter) recordTimeout(event engine.TimeoutEvent) {
	if v.Timeouts != nil {
		v.Timeouts.RecordTimeout(event)
	}
}

// settleUnfinishedProbe resolves a nonce committed outside the scheduler, which nothing else ever would.
func (w *Prober) settleUnfinishedProbe(ctx context.Context, escrowID, model string, params user.InferenceParams, nonce uint64, acknowledged bool) {
	posted, voted := Voter{Posters: w.posters, Timeouts: w.timeouts}.Settle(ctx, escrowID, model, params, nonce, acknowledged)
	if voted && w.narrator != nil {
		w.narrator.WarmupVoted(escrowID, nonce, posted.Action, posted.Reason)
	}
}
