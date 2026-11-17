package engine

// answer settles one claim; a suspicious host's is held rather than refused, because a refusal is permanent. See race.md, "Crowning".
func (c *raceCoordinator) answer(claim crownRequest) {
	switch attempt := c.byNonce[claim.Nonce]; {
	case attempt == nil || c.winner != nil:
		claim.Reply <- streamSuppressed
	case attempt.suspicious:
		c.claims = append(c.claims, claim)
	default:
		c.crownWinner(attempt, crownFirstClaim)
		claim.Reply <- streamWinner
	}
}

// settleClaims answers the held claims once the race can tell whether a rival will serve. See race.md, "Crown denial".
func (c *raceCoordinator) settleClaims() {
	if len(c.claims) == 0 {
		return
	}
	if c.winner == nil {
		switch {
		case c.rivalsMissedTheirDeadline():
			c.crownWinner(c.byNonce[c.claims[0].Nonce], crownRivalsMissedDeadline)
		case c.rivalPossible():
			return
		default:
			c.crownWinner(c.byNonce[c.claims[0].Nonce], crownNoRival)
		}
	}
	for _, claim := range c.claims {
		verdict := streamSuppressed
		if c.byNonce[claim.Nonce] == c.winner {
			verdict = streamWinner
		}
		claim.Reply <- verdict
	}
	c.claims = c.claims[:0]
}

// crownWinner is the single place one attempt becomes the client's answer, so the reason travels with it. A pick still waiting is given up: its nonce would serve nobody. See race.md, "Crowning".
func (c *raceCoordinator) crownWinner(attempt *liveAttempt, reason string) {
	c.winner = attempt
	c.stopPicking()
	c.traceStep(RaceStep{
		Kind: RaceStepAttemptCrowned, RequestID: c.request.RequestID, EscrowID: c.escrowID,
		Nonce: attempt.nonce, Participant: attempt.participant, Reason: reason,
	})
}

func (c *raceCoordinator) rivalsMissedTheirDeadline() bool {
	rivals := 0
	for _, attempt := range c.attempts {
		if attempt.done || c.holdsClaim(attempt.nonce) {
			continue
		}
		missed := attempt.receiptDeadline == deadlineMissed || attempt.firstTokenDeadline == deadlineMissed
		if !missed || !attempt.firstContent.IsZero() {
			return false
		}
		rivals++
	}
	return rivals > 0
}

func (c *raceCoordinator) holdsClaim(nonce uint64) bool {
	for _, claim := range c.claims {
		if claim.Nonce == nonce {
			return true
		}
	}
	return false
}

// rivalPossible reports an attempt other than the held claimants that could still be crowned. See race.md, "Crown denial".
func (c *raceCoordinator) rivalPossible() bool {
	return c.pending > len(c.claims) || c.picking() || c.moreImmediate > 0
}

// attemptSink is one attempt's byte path, running only on that attempt's goroutine.
type attemptSink struct {
	winner     *winnerWriter
	hasContent bool
}

func (s *attemptSink) Write(chunk []byte) (int, error) {
	if err := s.winner.Write(chunk, s.hasContent); err != nil {
		return 0, err
	}
	return len(chunk), nil
}
func (s *attemptSink) Flush() { s.winner.Flush() }

func (s *attemptSink) releasePrefix() { s.winner.releasePrefix() }

// contentGate hands the sink the one fact an io.Writer signature cannot carry, for the Write that follows.
type contentGate struct {
	streamClassifier
	sink *attemptSink
}

func (g contentGate) Classify(chunk []byte) chunkFacts {
	facts := g.streamClassifier.Classify(chunk)
	g.sink.hasContent = facts.Content
	return facts
}

func (g contentGate) missProof() (MissProof, bool) {
	prover, holds := g.asMissProver()
	if !holds {
		return MissProof{}, false
	}
	return prover.missProof()
}

func (g contentGate) releaseMissProof() {
	if prover, holds := g.asMissProver(); holds {
		prover.releaseMissProof()
	}
}

func (g contentGate) asMissProver() (missProver, bool) {
	prover, holds := g.streamClassifier.(missProver)
	return prover, holds
}
