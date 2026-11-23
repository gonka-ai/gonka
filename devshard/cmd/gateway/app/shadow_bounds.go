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
	limits := b.configuration.Load().For(model).Limits
	return shadow.ModelBounds{
		OutputCap:     limits.OutputTokenCap(),
		ContextLength: limits.ContextLength(b.snapshots.Snapshot().Models[model].MaxModelLen),
	}
}
