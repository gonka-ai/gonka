package harness

import "devshard/testenv/mockopenai"

// MatchWindowBeforeMs and MatchWindowAfterMs bound an execution's start around the inference's
// MsgStartInference.started_at (whole seconds): the executor is asked right after the start, and
// a retry or duplicate comes within the execution timeout.
const (
	MatchWindowBeforeMs = 2_000
	MatchWindowAfterMs  = 90_000
)

// sameMaxTokens drops an execution whose request carried another max_tokens than the inference's
// MsgStartInference: it belongs to the same id of another escrow started in the window. An
// execution logged without the wire view, or an inference without the limit, is kept.
func sameMaxTokens(m mockopenai.RequestRecord, f *InferenceFacts) bool {
	if m.Wire == nil || m.Wire.MaxTokens == nil || f.MaxTokens == 0 {
		return true
	}
	return uint64(*m.Wire.MaxTokens) == f.MaxTokens
}
