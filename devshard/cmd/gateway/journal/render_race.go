package journal

import (
	"time"

	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/internal/logkey"
)

// attemptFinishFields is the widest a finish line gets: the fixed head, every delivery field, and every mark.
const attemptFinishFields = 60

func renderRaceStep(lines logSink, step *engine.RaceStep) {
	switch step.Kind {
	case engine.RaceStepNonceCommitted:
		lines.Info("nonce committed",
			logkey.Request, step.RequestID, logkey.Escrow, step.EscrowID, logkey.Nonce, step.Nonce,
			logkey.Host, logkey.ShortHost(step.Participant), logkey.Slot, step.Slot, logkey.Role, step.Role, logkey.Reason, step.Reason)
	case engine.RaceStepEscalationUnfilled:
		lines.Info("escalation unfilled",
			logkey.Request, step.RequestID, logkey.Escrow, step.EscrowID, logkey.Reason, step.Reason,
			logkey.Attempts, step.Attempts, logkey.Error, step.Err)
	case engine.RaceStepAttemptCrowned:
		lines.Info("attempt crowned",
			logkey.Request, step.RequestID, logkey.Escrow, step.EscrowID, logkey.Nonce, step.Nonce,
			logkey.Host, logkey.ShortHost(step.Participant), logkey.Reason, step.Reason)
	case engine.RaceStepAttemptFinished:
		renderAttemptFinished(lines, step)
	case engine.RaceStepNonceStranded:
		lines.Warn("nonce stranded",
			logkey.Request, step.RequestID, logkey.Escrow, step.EscrowID,
			logkey.Nonce, step.Nonce, logkey.Host, logkey.ShortHost(step.Participant), logkey.Role, step.Role)
	case engine.RaceStepHostBlocked:
		lines.Warn("host blocked for state divergence",
			logkey.Request, step.RequestID, logkey.Escrow, step.EscrowID,
			logkey.Nonce, step.Nonce, logkey.Host, logkey.ShortHost(step.Participant))
	case engine.RaceStepHostRewound:
		lines.Warn("host rewound for state divergence",
			logkey.Request, step.RequestID, logkey.Escrow, step.EscrowID,
			logkey.Nonce, step.Nonce, logkey.Host, logkey.ShortHost(step.Participant),
			logkey.Rewound, step.Rewound)
	case engine.RaceStepWinnerFinishLanded:
		lines.Info("served winner finish landed",
			logkey.Request, step.RequestID, logkey.Escrow, step.EscrowID,
			logkey.Nonce, step.Nonce, logkey.Host, logkey.ShortHost(step.Participant),
			logkey.FinishLagMS, step.FinishLag.Milliseconds())
	}
}

func renderAttemptFinished(lines logSink, step *engine.RaceStep) {
	if !step.HasOutcome {
		fields := []any{logkey.Request, step.RequestID, logkey.Escrow, step.EscrowID, logkey.Nonce, step.Nonce,
			logkey.Host, logkey.ShortHost(step.Participant), logkey.NonceFinished, step.NonceFinished}
		if step.Reason != "" {
			fields = append(fields, logkey.Reason, step.Reason)
		}
		lines.Info("attempt finished with no outcome", fields...)
		return
	}
	fields := appendAttemptMarks(appendAttemptDeliveryFields(attemptFinishHead(step), &step.Outcome), step)
	lines.Info("attempt finished", fields...)
}

func appendAttemptMarks(fields []any, step *engine.RaceStep) []any {
	if step.PhaseAborted {
		fields = append(fields, logkey.PhaseAborted, true)
	}
	if step.Outcome.ReceiptDeadlineMissed {
		fields = append(fields, logkey.MissedReceiptDeadline, true)
	}
	if step.Outcome.FirstTokenDeadlineMissed {
		fields = append(fields, logkey.MissedFirstTokenDeadline, true)
	}
	return fields
}

// attemptFinishHead is what every finished attempt reports, in a line reserved for the widest one it can grow into.
func attemptFinishHead(step *engine.RaceStep) []any {
	return append(make([]any, 0, attemptFinishFields),
		logkey.Request, step.RequestID, logkey.Escrow, step.EscrowID, logkey.Nonce, step.Nonce,
		logkey.Host, logkey.ShortHost(step.Participant),
		logkey.Terminal, step.Terminal.String(),
		logkey.NonceFinished, step.NonceFinished, logkey.StateDivergent, step.Outcome.StateDivergent,
		logkey.LocalFault, step.Outcome.LocalFault, logkey.GatewayPressure, step.Outcome.GatewayPressure)
}

// appendEmptyAnswerFields says what an answer the gateway read as empty actually carried. See race.md, "Reading an empty answer back".
func appendEmptyAnswerFields(fields []any, outcome *engine.AttemptOutcome) []any {
	if outcome.LastChunkHead == "" {
		return fields
	}
	fields = append(fields, logkey.LastChunkHead, outcome.LastChunkHead)
	if outcome.FirstChunkHead != "" {
		fields = append(fields, logkey.FirstChunkHead, outcome.FirstChunkHead)
	}
	if outcome.FinishReason != "" {
		fields = append(fields, logkey.FinishReason, outcome.FinishReason)
	}
	if outcome.UsagePromptTokens > 0 {
		fields = append(fields, logkey.UsagePromptTokens, outcome.UsagePromptTokens)
	}
	if outcome.LogprobTokens > 0 {
		fields = append(fields, logkey.LogprobTokens, outcome.LogprobTokens)
	}
	return fields
}

// appendAttemptDeliveryFields measures every duration from the dispatch, for a winner and a loser alike.
func appendAttemptDeliveryFields(fields []any, outcome *engine.AttemptOutcome) []any {
	fields = append(fields,
		logkey.ContentChunks, outcome.ContentChunks,
		logkey.StreamChunks, outcome.StreamChunks,
		logkey.OutputBytes, outcome.OutputBytes,
		logkey.UsageTokens, outcome.UsageCompletionTokens)
	if outcome.MaxChunkGap > 0 {
		fields = append(fields,
			logkey.MaxGapMS, outcome.MaxChunkGap.Milliseconds(),
			logkey.MaxGapAtChunk, outcome.MaxChunkGapAt,
			logkey.MeanGapMS, outcome.MeanChunkGap.Milliseconds())
	}
	fields = appendEmptyAnswerFields(fields, outcome)
	if outcome.UpstreamStatus != 0 {
		fields = append(fields, logkey.UpstreamStatus, outcome.UpstreamStatus)
		if outcome.UpstreamBody != "" {
			fields = append(fields, logkey.UpstreamBody, outcome.UpstreamBody)
		}
	}
	spans := [...]struct {
		name  string
		until time.Time
	}{
		{logkey.ReceiptMS, outcome.ReceiptTime},
		{logkey.FirstTokenMS, outcome.FirstToken},
		{logkey.FirstContentMS, outcome.FirstContent},
		{logkey.AttemptMS, outcome.Completed},
	}
	for _, span := range spans {
		if outcome.SendTime.IsZero() || span.until.IsZero() {
			continue
		}
		fields = append(fields, span.name, span.until.Sub(outcome.SendTime).Milliseconds())
	}
	return fields
}
