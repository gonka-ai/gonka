package engine

import (
	"slices"
	"time"
)

type owedVoteBook interface {
	Owe(vote OwedVote)
	Paid(escrowID string, nonce uint64)
}

// OwedVote is one refusal vote as the book keeps it, with the params its race dispatched.
type OwedVote struct {
	EscrowID    string
	Nonce       uint64
	StartedAt   time.Time
	RequestID   string
	Participant string
	Model       string
	Params      any
}

func owesRefusal(step TimeoutStep) bool {
	return step.Post && step.Kind == SettleByVote && step.Event.Kind == TimeoutKindRefused
}

func (e *Engine) oweRefusals(outcome RaceOutcome, params any, poster TimeoutPoster, steps []TimeoutStep) {
	if e.deps.OwedVotes == nil || poster == nil {
		return
	}
	for _, step := range steps {
		if owesRefusal(step) {
			e.deps.OwedVotes.Owe(OwedVote{
				EscrowID: outcome.EscrowID, Nonce: step.Nonce, StartedAt: step.StartedAt,
				RequestID: outcome.RequestID, Participant: step.Event.Participant, Model: outcome.Model, Params: params,
			})
		}
	}
}

func (e *Engine) payRefusals(escrowID string, steps, stillOwed []TimeoutStep) {
	if e.deps.OwedVotes == nil {
		return
	}
	for _, step := range steps {
		owed := slices.ContainsFunc(stillOwed, func(other TimeoutStep) bool { return other.Nonce == step.Nonce })
		if owesRefusal(step) && !owed {
			e.deps.OwedVotes.Paid(escrowID, step.Nonce)
		}
	}
}

// ReplayOwedVotes owes again the refusal votes a previous process kept and never posted, each holding its escrow as its race did. See race.md, "Owed refusal votes across a restart".
func (e *Engine) ReplayOwedVotes(votes []OwedVote) {
	for _, vote := range votes {
		e.replayOwedVote(vote)
	}
}

func (e *Engine) replayOwedVote(vote OwedVote) {
	registration := e.admit()
	if registration == nil {
		return
	}
	poster := e.poster(vote.EscrowID, vote.Params)
	target, release, held := e.deps.Targets.Target(vote.EscrowID)
	if poster == nil || !held {
		if held {
			release()
		}
		registration.release()
		if e.deps.OwedVotes != nil {
			e.deps.OwedVotes.Paid(vote.EscrowID, vote.Nonce)
		}
		return
	}
	registration.holdTarget(target, release)
	step := TimeoutStep{
		Nonce: vote.Nonce, StartedAt: vote.StartedAt, Post: true, Kind: SettleByVote,
		Event: TimeoutEvent{
			RequestID: vote.RequestID, EscrowID: vote.EscrowID, Participant: vote.Participant, Model: vote.Model,
			Nonce: vote.Nonce, Kind: TimeoutKindRefused, Action: TimeoutActionStarted, Reason: TimeoutReasonNone,
		},
	}
	outcome := RaceOutcome{RequestID: vote.RequestID, EscrowID: vote.EscrowID, Model: vote.Model}
	e.queueVotes(outcome, poster, []TimeoutStep{step}, func() {}, registration.release)
}
