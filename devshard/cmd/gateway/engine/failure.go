package engine

func (o RaceOutcome) failure() error {
	switch {
	case o.Succeeded:
		return nil
	case len(o.Attempts) == 0:
		return ErrAllAttemptsFailed
	case o.winnerStreamed():
		if hostErr := o.hostError(); hostErr != nil {
			return hostErr
		}
		if o.winnerTruncated() {
			return ErrAnswerTruncated
		}
		return ErrWinnerIncomplete
	}
	if hostErr := o.hostError(); hostErr != nil {
		return hostErr
	}
	if len(o.Attempts) > 0 && o.everyAttempt(refusedAsUnavailable) {
		return ErrHostsUnavailable
	}
	if o.everyAttempt(endedByProofOfCompute) {
		return ErrPoCTookTheHosts
	}
	if o.everyAttempt(AttemptOutcome.emptyStream) {
		return ErrEmptyStream
	}
	return ErrAllAttemptsFailed
}

func endedByProofOfCompute(attempt AttemptOutcome) bool {
	return attempt.PhaseTransitionAborted
}

func refusedAsUnavailable(attempt AttemptOutcome) bool {
	return attempt.Terminal == TerminalUnavailable || attempt.Terminal == TerminalThrottled
}

func (o RaceOutcome) winnerTruncated() bool {
	return o.winnerHolds(func(attempt AttemptOutcome) bool {
		return attempt.Terminal == TerminalStreamTruncated && !attempt.HostTerminated && !attempt.NonceFinished
	})
}

func (o RaceOutcome) winnerStreamed() bool {
	return o.winnerHolds(func(attempt AttemptOutcome) bool { return attempt.ContentChunks > 0 })
}

func (o RaceOutcome) winnerHolds(holds func(AttemptOutcome) bool) bool {
	for _, attempt := range o.Attempts {
		if o.IsWinner(attempt) && holds(attempt) {
			return true
		}
	}
	return false
}

// hostError prefers the crowned attempt's refusal, then a trusted refusal or rejection that rules out a retry; a shorter host's context refusal answers only when every host refused. See race.md, "Escalation".
func (o RaceOutcome) hostError() *HostApplicationError {
	var found *AttemptOutcome
	foundRetryRuledOut := false
	for index := range o.Attempts {
		attempt := &o.Attempts[index]
		if attempt.ErrorSource == "" {
			continue
		}
		if o.IsWinner(*attempt) {
			found = attempt
			break
		}
		if o.refusedByAShorterHost(*attempt) {
			continue
		}
		retryRuledOut := answersForEveryHost(*attempt)
		if found == nil || (retryRuledOut && !foundRetryRuledOut) {
			found, foundRetryRuledOut = attempt, retryRuledOut
		}
	}
	if found == nil {
		found = o.longestShorterHostRefusal()
	}
	if found == nil {
		return nil
	}
	return &HostApplicationError{
		Code:    found.ErrorCode,
		Type:    found.ErrorType,
		Message: found.ErrorMessage,
		Payload: found.ErrorPayload,
	}
}

func (o RaceOutcome) refusedByAShorterHost(attempt AttemptOutcome) bool {
	return CapabilityOf(attempt).servableElsewhere(o.ModelContextLength, o.InputTokensFromBytes+o.OutputTokens)
}

// longestShorterHostRefusal names the most any host offered, and only once every host refused: a host that failed otherwise may have run the whole length.
func (o RaceOutcome) longestShorterHostRefusal() *AttemptOutcome {
	var longest *AttemptOutcome
	for index := range o.Attempts {
		attempt := &o.Attempts[index]
		if !o.refusedByAShorterHost(*attempt) {
			return nil
		}
		if longest == nil || CapabilityOf(*attempt).ContextLimit > CapabilityOf(*longest).ContextLimit {
			longest = attempt
		}
	}
	return longest
}

func (o RaceOutcome) everyAttempt(holds func(AttemptOutcome) bool) bool {
	for _, attempt := range o.Attempts {
		if !holds(attempt) {
			return false
		}
	}
	return true
}
