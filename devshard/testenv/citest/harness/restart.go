package harness

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"devshard/testenv/config"

	"github.com/stretchr/testify/require"
)

// PostAdminDeactivateDevshard POSTs /v1/admin/devshards/{id}/deactivate.
// Retires the runtime from memory (same registry drop settle uses).
func PostAdminDeactivateDevshard(t *testing.T, client *http.Client, gatewayURL, adminAPIKey, escrowID string) {
	t.Helper()
	if client == nil {
		client = HTTPClient()
	}
	url := strings.TrimRight(gatewayURL, "/") + "/v1/admin/devshards/" + escrowID + "/deactivate"
	req, err := http.NewRequest(http.MethodPost, url, nil)
	require.NoError(t, err)
	setAdminBearer(req, adminAPIKey)
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "POST deactivate: %s", string(body))
}

type adminParticipantsBody struct {
	Participants []string `json:"participants"`
}

// ClearGatewayParticipantQuarantines lifts the cutoff of every participant on the escrow. Restarting every versiond replica fails requests, and the limiter cuts those hosts off.
func ClearGatewayParticipantQuarantines(t *testing.T, client *http.Client, gatewayURL, adminAPIKey, escrowID string) {
	t.Helper()
	if client == nil {
		client = HTTPClient()
	}
	var body adminParticipantsBody
	require.NoError(t, getGatewayJSONWithKey(client, strings.TrimRight(gatewayURL, "/")+"/v1/admin/devshards/"+escrowID+"/participants", adminAPIKey, &body))
	require.NotEmpty(t, body.Participants, "escrow %s lists no participants", escrowID)
	cleared := 0
	for _, participantKey := range body.Participants {
		err := PostGatewayJSON(client, strings.TrimRight(gatewayURL, "/")+"/v1/admin/participants/unquarantine", adminAPIKey, map[string]string{
			"participant_key": participantKey,
		}, nil)
		var statusErr *GatewayStatusError
		if errors.As(err, &statusErr) && statusErr.StatusCode == http.StatusNotFound {
			continue
		}
		require.NoError(t, err)
		cleared++
	}
	t.Logf("citest: cleared the cutoff of %d tracked participant(s) after versiond restart", cleared)
}

// RestartService stops and starts a compose service without removing volumes.
func RestartService(t *testing.T, stack *Stack, service string) {
	t.Helper()
	stack.StopService(t, service)
	stack.StartService(t, service)
}

// RestartServices restarts compose services in order (stop all, then start all).
func RestartServices(t *testing.T, stack *Stack, services ...string) {
	t.Helper()
	for _, svc := range services {
		stack.StopService(t, svc)
	}
	for _, svc := range services {
		stack.StartService(t, svc)
	}
}

// VersiondHostIDs returns compose service ids for configured versiond hosts.
func VersiondHostIDs(cfg *config.File) []string {
	if cfg == nil {
		return nil
	}
	ids := make([]string, len(cfg.Hosts))
	for i, h := range cfg.Hosts {
		ids[i] = h.ID
	}
	return ids
}

// WaitVersiondSessionHealthy polls router + gateway until versiond/devshardd recover after restart.
func WaitVersiondSessionHealthy(t *testing.T, stack *Stack, cfg *config.File, eps Endpoints, escrowID string) {
	t.Helper()
	if escrowID == "" {
		t.Fatal("escrow id is required")
	}
	client := HTTPClient()
	WaitGETOK(t, client, eps.RouterHTTP+"/healthz", 5*time.Minute, "versiond-router healthz", stack)
	// Session routes have no /healthz; mempool resolves the escrow via lazy load / RecoverSessions.
	sessionReady := RouterSessionURL(eps.RouterHTTP, cfg.Versiond.VersionName, escrowID, "/mempool")
	WaitGETOK(t, client, sessionReady, 5*time.Minute, "devshardd session mempool via router", stack)
	WaitRouterCatalogAdmitted(t, stack, 5*time.Minute)
	WaitGETOK(t, client, eps.GatewayHTTP+"/v1/status", 3*time.Minute, "gateway /v1/status", stack)
}

// WaitGatewaySessionSettled waits until the gateway-visible ledger (balance
// and nonces) is unchanged across consecutive polls and no open holds remain.
// After a versiond restart, timeout-refunds of reserved tokens and late
// invalidation refunds can still be landing; taking the "before" snapshot for
// RequireGatewaySessionAdvanced too early makes a later chat look like it
// increased the balance.
//
// Finished/validated/timed_out/challenged rows stay in the live map until
// auto-seal (default every 150 nonces), so live_inferences is not a settle
// signal. Only pending/started gate settle: those still hold a reservation.
func WaitGatewaySessionSettled(t *testing.T, client *http.Client, gatewayURL, adminAPIKey, escrowID string) GatewaySessionSnapshot {
	t.Helper()
	// Cover escrow refusal_timeout (60s in testenv) plus vote round-trip after
	// hosts recover; a stranded reservation cannot look "quiet" forever.
	const wait = 90 * time.Second
	const tick = 200 * time.Millisecond
	const stablePolls = 15 // ~3s of an unchanged ledger with no open holds
	var prev *GatewaySessionSnapshot
	stable := 0
	ok := AssertEventually(t, wait, tick, func() bool {
		snapshot := GetGatewaySessionSnapshot(t, client, gatewayURL, adminAPIKey, escrowID)
		if snapshot.OpenHolds > 0 {
			stable = 0
		} else if prev != nil && gatewaySessionLedgerQuiet(*prev, snapshot) {
			stable++
		} else {
			stable = 0
		}
		cur := snapshot
		prev = &cur
		return stable >= stablePolls
	})
	snapshot := GetGatewaySessionSnapshot(t, client, gatewayURL, adminAPIKey, escrowID)
	if !ok {
		t.Fatalf("session ledger did not settle in %s: live=%d open_holds=%d nonce=%d latest=%d balance=%d",
			wait, snapshot.LiveInferences, snapshot.OpenHolds, snapshot.SessionNonce, snapshot.LatestNonce, snapshot.Balance)
	}
	return snapshot
}

// gatewaySessionLedgerQuiet reports whether the restart-sensitive ledger is
// unchanged. Live inference count is ignored: a Finished record remains live
// until auto-seal. Open holds are checked separately by WaitGatewaySessionSettled.
func gatewaySessionLedgerQuiet(prev, cur GatewaySessionSnapshot) bool {
	return prev.Balance == cur.Balance &&
		prev.SessionNonce == cur.SessionNonce &&
		prev.LatestNonce == cur.LatestNonce
}

// RequireGatewaySessionAdvanced asserts a successful chat advanced session nonce/state.
func RequireGatewaySessionAdvanced(t *testing.T, before, after GatewaySessionSnapshot) {
	t.Helper()
	require.Equal(t, before.EscrowID, after.EscrowID, "escrow id changed")
	require.Equal(t, before.Phase, after.Phase, "phase changed after chat")
	require.Greater(t, after.SessionNonce, before.SessionNonce,
		"session nonce regressed: before=%d after=%d", before.SessionNonce, after.SessionNonce)
	require.GreaterOrEqual(t, after.LatestNonce, before.LatestNonce,
		"latest nonce regressed: before=%d after=%d", before.LatestNonce, after.LatestNonce)
	require.LessOrEqual(t, after.Balance, before.Balance,
		"balance increased after chat (before=%d after=%d open_holds=%d→%d); late reservation/invalidation refund likely interleaved",
		before.Balance, after.Balance, before.OpenHolds, after.OpenHolds)
}

// RequireGatewaySessionStable asserts gateway session identity survived a
// versiond restart. Height-sync heartbeats keep running on the gateway while a
// replica drains (drain 5s, DefaultHeartbeatInterval 24s), so the producer
// cursor and durable tip may advance; they must not go backwards. Balance may
// fall if those heartbeat nonces are charged, and may rise if a draining
// replica's missing receipts are later timeout-refunded.
func RequireGatewaySessionStable(t *testing.T, before, after GatewaySessionSnapshot) {
	t.Helper()
	require.Equal(t, before.EscrowID, after.EscrowID, "escrow id changed across restart")
	require.Equal(t, before.Phase, after.Phase, "phase changed across restart")
	require.GreaterOrEqual(t, after.SessionNonce, before.SessionNonce,
		"session nonce regressed across restart: before=%d after=%d", before.SessionNonce, after.SessionNonce)
	require.GreaterOrEqual(t, after.LatestNonce, before.LatestNonce,
		"latest nonce regressed across restart: before=%d after=%d", before.LatestNonce, after.LatestNonce)
	if after.SessionNonce > before.SessionNonce || after.LatestNonce > before.LatestNonce || after.Balance != before.Balance {
		t.Logf("citest: session moved during restart wait (height-sync heartbeats): nonce %d→%d latest %d→%d balance %d→%d",
			before.SessionNonce, after.SessionNonce, before.LatestNonce, after.LatestNonce, before.Balance, after.Balance)
	}
}
