package accounting

import "devshard/cmd/gateway/engine"

// WithTiming sets the four timing flags from the outcome of the attempt that streamed the nonce, for a client's race and a gateway probe alike. See ../nonces/README.md.
func (a Attempt) WithTiming(outcome engine.AttemptOutcome) Attempt {
	a.SlowReceipt = slowReceipt(outcome)
	a.SlowChunk = outcome.MaxChunkGap > SlowChunkGap
	a.ClockDrifted = clockDrifted(outcome)
	a.SlowDecode = engine.TimePerOutputToken(outcome) > SlowDecode
	return a
}

func slowReceipt(outcome engine.AttemptOutcome) bool {
	if outcome.SendTime.IsZero() || outcome.ReceiptTime.IsZero() {
		return false
	}
	return outcome.ReceiptTime.Sub(outcome.SendTime) > SlowReceipt
}

func clockDrifted(outcome engine.AttemptOutcome) bool {
	offset, measured := engine.ClockOffset(outcome)
	return measured && (offset > ClockDrift || offset < -ClockDrift)
}
