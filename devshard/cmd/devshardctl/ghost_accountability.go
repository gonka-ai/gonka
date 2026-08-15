package main

import (
	"strings"
	"sync/atomic"
)

// ghostAccountabilityState gates raising a refusal timeout on a nonce the gateway declined to send.
// It is a switch rather than a constant so an operator can stop charging without a rebuild.
var ghostAccountabilityState atomic.Bool

// A gateway nobody configured still charges refusals; without this a host that refuses every nonce
// is never answerable for any of them.
func init() { ghostAccountabilityState.Store(true) }

// ConfigureGhostAccountability stops accountability timeouts for an explicitly falsey value
// ("0", "false", "off", "no"); anything else, empty included, leaves them running.
func ConfigureGhostAccountability(raw string) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "0", "false", "off", "no":
		ghostAccountabilityState.Store(false)
	default:
		ghostAccountabilityState.Store(true)
	}
}

func ghostAccountabilityEnabled() bool {
	return ghostAccountabilityState.Load()
}

// accountable reports whether a burn is the host's own doing. PoC is unavailability the protocol
// grants, and an empty queue is our own scheduling, so neither earns a miss.
func (g ghostKind) accountable() bool {
	return g == ghostThrottled || g == ghostCapability
}
