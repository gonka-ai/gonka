// Package probe holds the token budget of a probe: its reservation and its declared input length. See README.md.
package probe

import (
	"devshard/state"
	"devshard/user"
)

// MaxTokens is the probe's output reservation: the smallest one the host's state machine accepts.
const MaxTokens = uint64(state.MinReservationTokens)

// Params builds a probe for model started at startedAt, reserving MaxTokens and declaring prompt's length as its input.
func Params(model string, startedAt int64, prompt []byte) user.InferenceParams {
	return user.InferenceParams{
		Model:       model,
		Prompt:      prompt,
		InputLength: uint64(len(prompt)),
		MaxTokens:   MaxTokens,
		StartedAt:   startedAt,
	}
}
