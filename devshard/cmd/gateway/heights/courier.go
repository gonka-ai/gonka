// Package heights carries mainnet height into the escrow's log. See README.md.
package heights

import (
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/internal/logkey"
	"devshard/heightsync"
	"devshard/logging"
	"devshard/transport"
)

// BuildCourier builds the envelope the gateway's host clients carry, or nil when height sync is off.
func BuildCourier(settings config.HeightSync, follower *Oracle) *transport.ClientConfig {
	if !settings.Enabled {
		return nil
	}
	peerTips := transport.NewHeightSyncPeerTips()
	scheduler, err := heightsync.NewAnchorScheduler(
		uint64(settings.AnchorK), uint64(settings.AnchorSlots),
		heightsync.NewPeerTipOracleSource(peerTips, peerTips.Freshness),
	)
	if err != nil {
		logging.Warn("height sync is off: the anchor cadence does not hold",
			logkey.Subsystem, "heightsync", logkey.Error, err)
		return nil
	}
	courier := &transport.ClientConfig{HeightSync: scheduler, HeightSyncPeerTips: peerTips}
	if follower != nil {
		courier.HeightSyncLogOracle = follower
	}
	return courier
}
