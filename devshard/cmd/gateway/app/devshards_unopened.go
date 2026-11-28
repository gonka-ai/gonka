package app

import (
	"errors"
	"sync"

	"devshard/bridge"
	"devshard/cmd/gateway/env"
	"devshard/cmd/gateway/metrics"
	"devshard/cmd/gateway/registry"
	"devshard/user"
)

type unopenedEscrows struct {
	mu      sync.Mutex
	current []metrics.UnopenedEscrow
}

func registerUnopened(telemetry *metrics.Metrics) *unopenedEscrows {
	unopened := &unopenedEscrows{}
	telemetry.Register(metrics.NewUnopenedCollector(unopened.List))
	return unopened
}

func (u *unopenedEscrows) Replace(escrows []metrics.UnopenedEscrow) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.current = escrows
}

func (u *unopenedEscrows) List() []metrics.UnopenedEscrow {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]metrics.UnopenedEscrow(nil), u.current...)
}

func unopenedReason(cause error) string {
	switch {
	case errors.Is(cause, user.ErrLocalStateUnrecoverable):
		return metrics.UnopenedLocalState
	case errors.Is(cause, env.ErrPrivateKeyMissing):
		return metrics.UnopenedKeyMissing
	case errors.Is(cause, bridge.ErrChainUnavailable):
		return metrics.UnopenedChain
	case errors.Is(cause, registry.ErrDraining):
		return metrics.UnopenedDraining
	}
	return metrics.UnopenedOther
}
