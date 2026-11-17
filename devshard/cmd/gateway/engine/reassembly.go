package engine

// sseClassifier joins one attempt's bounded reassembly to the SSE verdicts. See race.md, "Classification and reassembly".
type sseClassifier struct {
	emptyAnswerCountsTokens bool
	carry                   *carryBuffer
	overflow                func()
	retained                *errorStreamRetainer
}

func newSSEClassifier(budget *carryBudget, participant string, emptyAnswerCountsTokens bool, overflow func()) *sseClassifier {
	return &sseClassifier{
		emptyAnswerCountsTokens: emptyAnswerCountsTokens,
		carry:                   newCarryBuffer(budget, participant),
		overflow:                overflow,
		retained:                newErrorStreamRetainer(maxMissProofBytes),
	}
}

func (c *sseClassifier) Classify(chunk []byte) chunkFacts {
	events, firstDrop := c.carry.Take(chunk)
	if firstDrop {
		c.retained.truncate()
		if c.overflow != nil {
			c.overflow()
		}
	}
	c.retained.retain(events)
	return c.facts(classifyChunk(events, c.emptyAnswerCountsTokens))
}

// Flush classifies the unterminated final event, which reads as nothing at all until it does.
func (c *sseClassifier) Flush() chunkFacts {
	tail := c.carry.Tail()
	c.retained.retain(tail)
	return c.facts(classifyChunk(tail, c.emptyAnswerCountsTokens))
}

func (c *sseClassifier) Release() {
	c.carry.Release()
	c.retained.release()
}

func (c *sseClassifier) missProof() (MissProof, bool) { return c.retained.proof() }

func (c *sseClassifier) releaseMissProof() { c.retained.release() }

// facts keeps a capability refusal out of Error, while its message still reaches the perf recorder. See race.md, "An SSE error event counts as a chunk but never crowns".
func (c *sseClassifier) facts(signal chunkSignal) chunkFacts {
	facts := chunkFacts{
		Content:               signal.crownsWinner(),
		ContentSource:         signal.ContentSource,
		UsageCompletionTokens: signal.UsageCompletionTokens,
		UsagePromptTokens:     signal.UsagePromptTokens,
		LogprobTokens:         signal.LogprobTokens,
		FinishReason:          signal.FinishReason,
		TokensBurned:          signal.UsageCompletionTokens > 0 && c.emptyAnswerCountsTokens,
		LogprobsDecoded:       signal.LogprobsDecoded,
	}
	if !signal.Error.present() {
		return facts
	}
	capability := ParseCapabilityError(signal.Error.Message)
	facts.Error = !capability.Refused()
	facts.CapabilityRefused = capability.Refused()
	facts.Capability = capability
	facts.ErrorSource = signal.Error.Source
	facts.ErrorCode = signal.Error.Code
	facts.ErrorType = signal.Error.Type
	facts.ErrorMessage = signal.Error.Message
	facts.ErrorPayload = signal.Error.Payload
	return facts
}
