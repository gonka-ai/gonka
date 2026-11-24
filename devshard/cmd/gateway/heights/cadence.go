package heights

import "devshard/cmd/gateway/config"

// Cadence is the half of a session that keeps a quiet escrow syncing; *user.Session satisfies it.
type Cadence interface {
	StartHeartbeatLoop()
}

// StartCadence opens the heartbeat turn only where the fleet carries height sync. See README.md.
func StartCadence(cadence Cadence, settings config.HeightSync) {
	if cadence == nil || !settings.Enabled {
		return
	}
	cadence.StartHeartbeatLoop()
}
