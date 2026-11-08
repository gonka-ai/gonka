package main

import (
	"context"
	"net/http"

	"devshard/memwatch"
	"devshard/user"
)

// gatewayInventory is the session-map half of a gateway snapshot. Every field
// is a count. The JSON names are also the log fields.
type gatewayInventory struct {
	Runtimes           int    `json:"runtimes"`
	LiveInferences     int    `json:"live_inferences"`
	SealedInferences   int    `json:"sealed_inferences"`
	HeartbeatNonces    int    `json:"heartbeat_nonces"`
	RetainedDiffs      int    `json:"retained_diffs"`
	PendingTxs         int    `json:"pending_txs"`
	SignatureNonces    int    `json:"signature_nonces"`
	NonceStates        int    `json:"nonce_states"`
	AppliedTxKeys      int    `json:"applied_tx_keys"`
	PendingTxKeys      int    `json:"pending_tx_keys"`
	HostsCatchingUp    int    `json:"hosts_catching_up"`
	ActiveUserRequests int    `json:"active_user_requests"`
	PendingRaceCleanup int    `json:"pending_race_cleanup"`
	TimeoutVotePosts   int    `json:"timeout_vote_posts"`
	FattestEscrow      string `json:"fattest_escrow,omitempty"`
	FattestLive        int    `json:"fattest_live"`
}

func (g gatewayInventory) LogAttrs() []any {
	return []any{
		"runtimes", g.Runtimes,
		"live_inferences", g.LiveInferences,
		"sealed_inferences", g.SealedInferences,
		"heartbeat_nonces", g.HeartbeatNonces,
		"retained_diffs", g.RetainedDiffs,
		"pending_txs", g.PendingTxs,
		"signature_nonces", g.SignatureNonces,
		"nonce_states", g.NonceStates,
		"applied_tx_keys", g.AppliedTxKeys,
		"pending_tx_keys", g.PendingTxKeys,
		"hosts_catching_up", g.HostsCatchingUp,
		"active_user_requests", g.ActiveUserRequests,
		"pending_race_cleanup", g.PendingRaceCleanup,
		"timeout_vote_posts", g.TimeoutVotePosts,
		"fattest_escrow", g.FattestEscrow,
		"fattest_live", g.FattestLive,
	}
}

func (inv *gatewayInventory) add(c user.MemoryCounts) {
	inv.LiveInferences += c.Live
	inv.SealedInferences += c.Sealed
	inv.HeartbeatNonces += c.HeartbeatNonces
	inv.RetainedDiffs += c.RetainedDiffs
	inv.PendingTxs += c.PendingTxs
	inv.SignatureNonces += c.SignatureNonces
	inv.NonceStates += c.NonceStates
	inv.AppliedTxKeys += c.AppliedTxKeys
	inv.PendingTxKeys += c.PendingTxKeys
	inv.HostsCatchingUp += c.HostsCatchingUp
	if c.Live > inv.FattestLive {
		inv.FattestLive = c.Live
		inv.FattestEscrow = c.EscrowID
	}
}

// memoryInventory sums map lengths across loaded runtimes. It copies the
// runtime list under g.mu, then reads each session after releasing it, so a
// large inference map is never walked and the gateway lock is not held across
// session locks.
func (g *Gateway) memoryInventory() memwatch.Inventory {
	if g == nil {
		return gatewayInventory{}
	}
	g.mu.Lock()
	runtimes := make([]*devshardRuntime, len(g.runtimeOrder))
	copy(runtimes, g.runtimeOrder)
	g.mu.Unlock()

	var inv gatewayInventory
	inv.Runtimes = len(runtimes)
	inv.TimeoutVotePosts = len(timeoutVotePosts)
	for _, rt := range runtimes {
		if rt == nil {
			continue
		}
		inv.ActiveUserRequests += int(rt.activeUserRequests.Load())
		inv.PendingRaceCleanup += int(rt.pendingRaceCleanup.Load())
		if rt.session == nil {
			continue
		}
		inv.add(rt.session.MemoryCounts())
	}
	return inv
}

// startGatewayMemoryLog starts the snapshot logger. stop cancels it and waits
// for the in-flight sample, so Close does not race a count.
func startGatewayMemoryLog(g *Gateway) (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	g.memory = memwatch.New(memwatch.Config{Inventory: g.memoryInventory})
	g.memory.Start(ctx)
	return func() {
		cancel()
		g.memory.Wait()
	}
}

// handleDebugMemory serves the latest snapshot. It does not take a new one.
// Admin-gated via the /v1/debug/ prefix.
func (g *Gateway) handleDebugMemory(w http.ResponseWriter, r *http.Request) {
	if g == nil || g.memory == nil {
		http.Error(w, "memory log is not running", http.StatusServiceUnavailable)
		return
	}
	g.memory.ServeHTTP(w, r)
}
