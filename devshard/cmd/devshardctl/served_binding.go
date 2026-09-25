package main

import (
	"context"

	"devshard/user"
)

func (e *Redundancy) handleServedBinding(nonce uint64, hostIndex int, participantKey string, verdict user.ServedBinding) {
	if e.metrics != nil {
		e.metrics.RecordServedBinding(string(verdict))
	}
	if verdict != user.ServedBindingMismatch {
		return
	}
	e.servedBindingStrikes.Add(1)
	go func() {
		defer e.servedBindingStrikes.Done()
		e.strikeServedBindingMismatch(nonce, hostIndex, participantKey)
	}()
}

func (e *Redundancy) strikeServedBindingMismatch(nonce uint64, hostIndex int, participantKey string) {
	if participantKey == "" {
		participantKey = legacyHostPerfKey(hostIndex)
	}
	logInferenceWarn(context.Background(), e.devshardID, nonce, "served_binding_failed", "participant", participantKey)
	if e.perf == nil {
		return
	}
	e.recordFailureSample(RequestSample{
		HostIdx:        hostIndex,
		ParticipantKey: participantKey,
		Model:          normalizeModelID(e.model),
		Responsive:     false,
	})
	if e.participantLimiter != nil && e.perf.ParticipantFailureThresholdExceeded(participantKey) {
		e.participantLimiter.ObserveStalledWinner(participantKey)
	}
}

func bindsReceivedStream(attempt *inflight) bool {
	return attempt != nil && attempt.resp != nil && !attempt.probe && !attempt.phaseTransitionAborted
}

func (e *Redundancy) recordFailureSample(sample RequestSample) {
	e.perf.Record(sample)
	if e.metrics != nil {
		e.metrics.ObserveRequestSample(e.devshardID, sample)
	}
}
