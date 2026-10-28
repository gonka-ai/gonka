// Package hostping pings the hosts the live escrows use. See README.md.
package hostping

import (
	"strings"

	"common/probe"

	"devshard/cmd/gateway/registry"
	"devshard/transport"
)

const (
	clockLeaf  = "/clock"
	healthLeaf = "/healthz"
)

type liveDials interface {
	HostDials() []registry.HostDial
}

// Targets snapshots the live hosts once per probe wave.
type Targets struct {
	Live liveDials
}

func (t Targets) Targets() []probe.Target {
	if t.Live == nil {
		return nil
	}
	dials := t.Live.HostDials()
	targets := make([]probe.Target, 0, len(dials))
	for _, dial := range dials {
		targets = append(targets, probe.Target{
			Key:         dial.ParticipantKey,
			ClockURL:    probeURL(dial, clockLeaf),
			FallbackURL: probeURL(dial, healthLeaf),
		})
	}
	return targets
}

func probeURL(dial registry.HostDial, leaf string) string {
	prefix := strings.TrimSpace(dial.RoutePrefix)
	if prefix == "" {
		prefix = transport.DefaultRoutePrefix()
	}
	if !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}
	return strings.TrimRight(dial.BaseURL, "/") + strings.TrimRight(prefix, "/") + leaf
}
