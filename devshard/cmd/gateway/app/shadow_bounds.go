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
		OutputCap:     uint64(max(limits.MaxTokensCap, 0)),
		ContextLength: limits.ContextLength(b.snapshots.Snapshot().Models[model].MaxModelLen),
	}
}
