package app

import (
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/shadow"
)

type probeBounds struct {
	configuration *config.Holder
	snapshots     phaseSnapshots
}

func (b probeBounds) Of(model string) shadow.ModelBounds {
	limits := b.configuration.Load().Limits
	return shadow.ModelBounds{
		OutputCap:     limits.OutputTokenCap(model),
		ContextLength: limits.ContextLength(model, b.snapshots.Snapshot().Models[model].MaxModelLen),
	}
}
