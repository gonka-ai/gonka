package app

import (
	"sync/atomic"

	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/metrics"
	"devshard/user"
)

type servedStreamHosts interface {
	RecordServedMismatch(participant, model string)
}

type servedStreamNarrator interface {
	HostServedUnsigned(escrowID, model, participant string, nonce uint64)
}

type appliedFinishes interface {
	OnFinishApplied(escrowID string, nonce uint64)
}

type finishNotice struct{ races atomic.Pointer[engine.Engine] }

func (n *finishNotice) attach(races *engine.Engine) { n.races.Store(races) }

func (n *finishNotice) OnFinishApplied(escrowID string, nonce uint64) {
	if n == nil {
		return
	}
	if races := n.races.Load(); races != nil {
		races.OnFinishApplied(escrowID, nonce)
	}
}

func servedBindingStrikes(hosts servedStreamHosts, narrator servedStreamNarrator, recorder *metrics.ServedBindingRecorder, finishes appliedFinishes) func(escrowID, model, participant string, nonce uint64, verdict user.ServedBinding) {
	return func(escrowID, model, participant string, nonce uint64, verdict user.ServedBinding) {
		finishes.OnFinishApplied(escrowID, nonce)
		recorder.Observed(model, string(verdict))
		if verdict != user.ServedBindingMismatch {
			return
		}
		narrator.HostServedUnsigned(escrowID, model, participant, nonce)
		hosts.RecordServedMismatch(participant, model)
	}
}
