package hostevents

import (
	"context"
	"strconv"

	"common/nodemanager/gen"

	"devshard/bridge"
)

// Run starts the shared poller with escrow kinds included. The loop lives in
// Poller; Unimplemented keeps running and falls back to rehydrate.
func Run(ctx context.Context, cfg Config, sink Sink) {
	if sink == nil {
		if cfg.Log != nil {
			cfg.Log.Error("hostevents: sink is required")
		}
		return
	}
	NewPoller(PollerConfig{
		Config:         cfg,
		Escrow:         sink,
		IncludeEscrow:  true,
		BlocksVerified: true,
	}).Run(ctx)
}

// warmKeyEntryOf maps a warm-key host event onto the bridge's entry type. A
// grant event carries the granted-at height; an end event carries the
// revoked-at height and the end name.
func warmKeyEntryOf(ev *gen.HostEvent) (bridge.WarmKeyEntry, bool) {
	p := ev.GetWarmKey()
	if p == nil || p.GetGranter() == "" || p.GetGrantee() == "" || p.GetGrantId() == 0 {
		return bridge.WarmKeyEntry{}, false
	}
	entry := bridge.WarmKeyEntry{
		Granter:       p.GetGranter(),
		ID:            p.GetGrantId(),
		Grantee:       p.GetGrantee(),
		ExpiresAtUnix: p.GetExpiresAtUnix(),
	}
	if ev.GetKind() == gen.HostEventKind_HOST_EVENT_KIND_WARM_KEY_REVOKED {
		entry.RevokedAtHeight = p.GetHeight()
		entry.End = p.GetEnd()
		if entry.End == "" {
			entry.End = "WARM_KEY_LOG_END_REVOKED"
		}
	} else {
		entry.GrantedAtHeight = p.GetHeight()
	}
	return entry, true
}

func escrowIDString(ev *gen.HostEvent) string {
	if ev.GetEscrow() == nil {
		return ""
	}
	return strconv.FormatUint(ev.GetEscrow().GetEscrowId(), 10)
}
