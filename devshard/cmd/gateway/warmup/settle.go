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

// settleUnfinishedProbe resolves a nonce committed outside the scheduler, which nothing else ever would.
func (w *Prober) settleUnfinishedProbe(ctx context.Context, escrowID, model string, params user.InferenceParams, nonce uint64, acknowledged bool) {
	if w.posters == nil {
		return
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

	poster, resolved := w.posters(escrowID, params)
	if !resolved {
		w.recordTimeout(engine.TimeoutEvent{
			EscrowID: escrowID, Model: model, Nonce: nonce, Kind: kind,
			Action: engine.TimeoutActionSkipped, Reason: engine.TimeoutReasonNoPoster,
		})
		return
	}
	w.recordTimeout(event)

	posted := event
	vote, err := poster.SettleTimeout(ctx, engine.TimeoutStep{Nonce: nonce, StartedAt: time.Unix(params.StartedAt, 0)})
	posted.Action, posted.Reason = engine.TimeoutOutcome(vote, err, false)
	w.recordTimeout(posted)

	if w.narrator != nil {
		w.narrator.WarmupVoted(escrowID, nonce, posted.Action, posted.Reason)
	}
}

func (w *Prober) recordTimeout(event engine.TimeoutEvent) {
	if w.timeouts != nil {
		w.timeouts.RecordTimeout(event)
	}
}
