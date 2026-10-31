package api

import (
	"errors"
	"time"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
)

// admission is the pre-queue chain check. See README.md, "What the boundary hands the engine".
func admission(snapshot chain.PhaseSnapshot, modes config.Modes, now time.Time, maxAgeSeconds int64) error {
	if !modes.BlocksRequests(snapshot) {
		if stale := staleness(snapshot, now, maxAgeSeconds); stale != nil {
			return stale
		}
		return nil
	}
	return &BlockedError{
		Reason:     snapshot.BlockReason,
		Phase:      snapshot.EpochPhase,
		Confirming: snapshot.ConfirmationPoCPhase,
	}
}

func blockedOnlyByPoC(refusal error) bool {
	var blocked *BlockedError
	if !errors.As(refusal, &blocked) {
		return false
	}
	return blocked.Reason == chain.BlockReasonPoC || blocked.Reason == chain.BlockReasonConfirmationPoC
}

func staleness(snapshot chain.PhaseSnapshot, now time.Time, maxAgeSeconds int64) error {
	if maxAgeSeconds <= 0 {
		return nil
	}
	limit := time.Duration(maxAgeSeconds) * time.Second
	if snapshot.LastHealthyAt.IsZero() {
		return &ChainStaleError{Age: limit}
	}
	if age := now.Sub(snapshot.LastHealthyAt); age >= limit {
		return &ChainStaleError{Age: age}
	}
	return nil
}
