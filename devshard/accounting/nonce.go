package accounting

import (
	"errors"
	"fmt"
)

func (b *Book) applyDiff(event DiffApplied) error {
	return b.withEscrow(event.EscrowID, func(escrow *escrowBook) error {
		if event.Nonce == 0 {
			return errors.New("diff nonce must be greater than zero")
		}
		if event.Nonce <= escrow.reducedThrough {
			return nil
		}
		if _, exists := escrow.appliedAhead[event.Nonce]; exists {
			return nil
		}
		if event.Nonce > escrow.latest {
			escrow.latest = event.Nonce
		}
		escrow.appliedAhead[event.Nonce] = struct{}{}
		defer escrow.advanceReduction()
		slotID := AssignedNonceSlot(event.Nonce, uint64(len(escrow.metadata.Slots)))
		if !event.Inference {
			key := CounterKey{
				SlotID:      slotID,
				Disposition: DispositionProtocolOnly,
			}
			escrow.increment(key)
			return nil
		}
		escrow.live[event.Nonce] = &nonceState{
			slotID:     slotID,
			inference:  true,
			quarantine: QuarantineNone,
		}
		return nil
	})
}

func (b *Book) applyProtocolTransition(event ProtocolTransition) error {
	return b.withEscrow(event.EscrowID, func(escrow *escrowBook) error {
		if event.Nonce == 0 {
			return errors.New("protocol transition nonce must be greater than zero")
		}
		switch event.Kind {
		case ProtocolChallenged:
			if _, resolved := escrow.verdicts[event.Nonce]; resolved {
				return nil
			}
			if _, exists := escrow.challenges[event.Nonce]; exists {
				return nil
			}
			slotID, err := escrow.slotForNonce(event.Nonce)
			if err != nil {
				return err
			}
			escrow.challenges[event.Nonce] = slotID
			return nil
		case ProtocolValidated:
			if _, resolved := escrow.verdicts[event.Nonce]; resolved {
				return nil
			}
			if _, err := escrow.slotForNonce(event.Nonce); err != nil {
				return err
			}
			delete(escrow.challenges, event.Nonce)
			escrow.verdicts[event.Nonce] = ProtocolValidated
			return nil
		case ProtocolInvalidated:
			if _, resolved := escrow.verdicts[event.Nonce]; resolved {
				return nil
			}
			slotID, ok := escrow.challenges[event.Nonce]
			if !ok {
				return nil
			}
			delete(escrow.challenges, event.Nonce)
			escrow.verdicts[event.Nonce] = ProtocolInvalidated
			escrow.invalidBySlot[slotID]++
			return nil
		}

		state, err := escrow.mutableNonce(event.Nonce)
		if err != nil {
			if event.Kind == ProtocolReceiptApplied || event.Kind == ProtocolFinishApplied {
				// Live attempt state is intentionally not persisted. A committed
				// diff tail may therefore reference a nonce that recovery can
				// only leave visible as unclassified.
				return nil
			}
			return err
		}
		switch event.Kind {
		case ProtocolReceiptApplied:
			state.receipt = true
		case ProtocolFinishApplied:
			state.finish = true
			if state.timeoutRequired && state.timeoutOutcome != TimeoutApplied {
				state.timeoutRequired = false
				state.timeoutKind = ""
				state.timeoutPhase = ""
				state.timeoutOutcome = ""
				state.timeoutReason = ""
			}
		default:
			return fmt.Errorf("invalid protocol transition %q", event.Kind)
		}
		escrow.reclassify(event.Nonce, state)
		return nil
	})
}

func (b *Book) applyGhost(event Ghost) error {
	return b.withEscrow(event.EscrowID, func(escrow *escrowBook) error {
		state, err := escrow.mutableNonce(event.Nonce)
		if err != nil {
			if escrow.hasAppliedNonce(event.Nonce) {
				return nil
			}
			return err
		}
		phase, err := normalizePhase(event.DispatchPhase)
		if err != nil {
			return err
		}
		quarantine, err := normalizeQuarantine(event.Quarantine)
		if err != nil {
			return err
		}
		state.ghost = true
		state.dispatchPhase = phase
		state.quarantine = quarantine
		state.noSendReason = normalizeNoSendReason(event.Reason)
		state.detailReason = normalizeDetailReason(event.DetailReason)
		escrow.reclassify(event.Nonce, state)
		return nil
	})
}

func (b *Book) applyRealSend(event RealSend) error {
	return b.withEscrow(event.EscrowID, func(escrow *escrowBook) error {
		state, err := escrow.mutableNonce(event.Nonce)
		if err != nil {
			if escrow.hasAppliedNonce(event.Nonce) {
				return nil
			}
			return err
		}
		phase, err := normalizePhase(event.DispatchPhase)
		if err != nil {
			return err
		}
		quarantine, err := normalizeQuarantine(event.Quarantine)
		if err != nil {
			return err
		}
		state.sent = true
		state.dispatchPhase = phase
		state.quarantine = quarantine
		escrow.reclassify(event.Nonce, state)
		return nil
	})
}

func (b *Book) applyUsage(escrowID string, nonce uint64, usage Usage) error {
	return b.withEscrow(escrowID, func(escrow *escrowBook) error {
		state, err := escrow.mutableNonce(nonce)
		if err != nil {
			if escrow.hasAppliedNonce(nonce) {
				return nil
			}
			return err
		}
		state.usage = usage
		escrow.reclassify(nonce, state)
		return nil
	})
}

func (b *Book) applyTimeoutRequired(event TimeoutRequired) error {
	return b.withEscrow(event.EscrowID, func(escrow *escrowBook) error {
		state, err := escrow.mutableNonce(event.Nonce)
		if err != nil {
			if escrow.hasAppliedNonce(event.Nonce) {
				return nil
			}
			return err
		}
		if !state.sent {
			return errors.New("timeout required for a nonce that was not sent")
		}
		if event.Kind != TimeoutRefused && event.Kind != TimeoutExecution {
			return fmt.Errorf("invalid timeout kind %q", event.Kind)
		}
		phase, err := normalizePhase(event.EvaluationPhase)
		if err != nil {
			return err
		}
		origin, err := normalizeFailureOrigin(event.FailureOrigin)
		if err != nil {
			return err
		}
		if state.timeoutRequired {
			state.timeoutKind = event.Kind
			state.timeoutPhase = phase
			if state.failureOrigin == FailureTransportUnknown && origin != FailureTransportUnknown {
				state.failureOrigin = origin
			}
			if state.detailReason == "" || state.detailReason == "unknown" {
				state.detailReason = normalizeDetailReason(event.DetailReason)
			}
			escrow.reclassify(event.Nonce, state)
			return nil
		}
		state.timeoutRequired = true
		state.timeoutKind = event.Kind
		state.timeoutPhase = phase
		state.failureOrigin = origin
		state.detailReason = normalizeDetailReason(event.DetailReason)
		escrow.reclassify(event.Nonce, state)
		return nil
	})
}

func (b *Book) applyTimeoutOutcome(event TimeoutOutcomeRecorded) error {
	return b.withEscrow(event.EscrowID, func(escrow *escrowBook) error {
		state, err := escrow.mutableNonce(event.Nonce)
		if err != nil {
			if escrow.hasAppliedNonce(event.Nonce) {
				return nil
			}
			return err
		}
		if !state.timeoutRequired {
			return errors.New("timeout outcome recorded before timeout was required")
		}
		if !validTimeoutOutcome(event.Outcome) {
			return fmt.Errorf("invalid timeout outcome %q", event.Outcome)
		}
		state.timeoutOutcome = event.Outcome
		if event.Outcome == TimeoutApplied {
			state.timeoutReason = ""
		} else {
			state.timeoutReason = normalizeTimeoutReason(event.Reason)
		}
		escrow.reclassify(event.Nonce, state)
		return nil
	})
}

func (e *escrowBook) mutableNonce(nonce uint64) (*nonceState, error) {
	if nonce == 0 || nonce > e.latest {
		return nil, fmt.Errorf("nonce %d is not consumed (latest %d)", nonce, e.latest)
	}
	state := e.live[nonce]
	if state == nil || !state.inference {
		return nil, fmt.Errorf("nonce %d is not a mutable inference nonce", nonce)
	}
	return state, nil
}

func (e *escrowBook) hasAppliedNonce(nonce uint64) bool {
	if nonce == 0 {
		return false
	}
	if nonce <= e.reducedThrough {
		return true
	}
	_, ok := e.appliedAhead[nonce]
	return ok
}

func (e *escrowBook) advanceReduction() {
	for {
		next := e.reducedThrough + 1
		if _, ok := e.appliedAhead[next]; !ok {
			return
		}
		delete(e.appliedAhead, next)
		e.reducedThrough = next
	}
}

func (e *escrowBook) slotForNonce(nonce uint64) (uint32, error) {
	if nonce == 0 || nonce > e.latest {
		return 0, fmt.Errorf("nonce %d is not consumed (latest %d)", nonce, e.latest)
	}
	if state := e.live[nonce]; state != nil {
		return state.slotID, nil
	}
	return AssignedNonceSlot(nonce, uint64(len(e.metadata.Slots))), nil
}

func (e *escrowBook) reclassify(nonce uint64, state *nonceState) {
	if state.counted != nil {
		e.decrement(*state.counted)
		state.counted = nil
	}
	if key, ok := state.counterKey(); ok {
		e.increment(key)
		state.counted = &key
	}
	if state.isTerminal() {
		delete(e.live, nonce)
	}
}

func (s *nonceState) counterKey() (CounterKey, bool) {
	key := CounterKey{
		SlotID:                 s.slotID,
		DispatchPhase:          s.dispatchPhase,
		TimeoutEvaluationPhase: s.timeoutPhase,
		QuarantineMode:         s.quarantine,
		NoSendReason:           s.noSendReason,
		FailureOrigin:          s.failureOrigin,
		DetailReason:           s.detailReason,
		TimeoutKind:            s.timeoutKind,
		TimeoutOutcome:         s.timeoutOutcome,
		TimeoutReason:          s.timeoutReason,
	}
	switch {
	case s.ghost:
		key.Disposition = DispositionGhost
	case s.finish && s.usage == UsageWinner:
		key.Disposition = DispositionFinishedUsed
	case s.finish && s.usage == UsageLoser:
		key.Disposition = DispositionFinishedUnused
	case s.finish && s.usage == UsageUnknownValue:
		key.Disposition = DispositionFinishedUsageUnknown
	case s.timeoutRequired && s.receipt:
		key.Disposition = DispositionUnfinishedExecution
	case s.timeoutRequired:
		key.Disposition = DispositionUnfinishedRefused
	default:
		return CounterKey{}, false
	}
	return key, true
}

func (s *nonceState) isTerminal() bool {
	if s.ghost {
		return true
	}
	if s.finish && s.usage != "" {
		return true
	}
	return s.timeoutRequired && s.timeoutOutcome == TimeoutApplied
}

func (e *escrowBook) increment(key CounterKey) {
	e.counters[key]++
}

func (e *escrowBook) decrement(key CounterKey) {
	if e.counters[key] <= 1 {
		delete(e.counters, key)
		return
	}
	e.counters[key]--
}

func (e *escrowBook) compactAllTimeouts() {
	for nonce, state := range e.live {
		if state.timeoutRequired && state.timeoutOutcome != TimeoutApplied {
			delete(e.live, nonce)
		}
	}
}
