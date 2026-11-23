package shadow

import (
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/filters"
)

// ModelBounds is what a model allows a probe to ask for; a ContextLength of config.NoContextLength is a model whose context length is not known, so only OutputCap bounds the probe.
type ModelBounds struct {
	OutputCap     uint64
	ContextLength uint64
}

// Bounds is satisfied by the adapter that reads a model's output cap and context length from the running config and the chain snapshot.
type Bounds interface {
	Of(model string) ModelBounds
}

func (b ModelBounds) ceiling(promptTokens int) uint64 {
	ceiling := min(ProbeMaxOutputTokens, b.OutputCap)
	if b.ContextLength == config.NoContextLength {
		return ceiling
	}
	prompt := uint64(max(promptTokens, 0)) + filters.ChatTemplateMinimumTokens
	if prompt >= b.ContextLength {
		return 0
	}
	return min(ceiling, b.ContextLength-prompt)
}
