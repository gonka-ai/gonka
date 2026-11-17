package harness

import (
	"fmt"
	"net/http"
	"sort"
	"testing"
	"time"

	"devshard/testenv/config"

	"github.com/stretchr/testify/require"
)

// chatReadyTimeout is the gateway transport.WaitReady budget for Chat. A dead
// origin only records ObserveTransportFailure when this elapses while the
// attempt parent context is still live (see transport.waitChatReady).
const chatReadyTimeout = 10 * time.Second

// ForceOneProbeQuarantine leaves exactly one participant in probe quarantine
// (IsBlocked / ghostThrottled path) and every other participant healthy.
// preferKeep is used when it is among the probe-quarantined keys.
//
// A stopped host alone does not burn ghosts: the multi-host race cancels
// WaitReady before chatReadyTimeout, so transport failures are not observed.
// Recipe: raise receipt_timeout above chatReadyTimeout, stop every versiond
// host, drive chat until WaitReady budgets expire and probe-quarantine at
// least one participant, bring hosts back, clear all but one.
func ForceOneProbeQuarantine(
	t *testing.T,
	client *http.Client,
	stack *Stack,
	cfg *config.File,
	eps Endpoints,
	devshardID, model, preferKeep string,
	timeout time.Duration,
) string {
	t.Helper()
	if timeout == 0 {
		timeout = 3 * time.Minute
	}
	if client == nil {
		client = GatewayChatClient()
	}
	require.NotNil(t, stack)
	require.NotNil(t, cfg)

	// Parent attempt must outlive WaitReady so observeTransportFailure runs.
	receiptMS := int64((chatReadyTimeout + 10*time.Second) / time.Millisecond)
	PatchGatewayAdminSettings(t, client, eps.GatewayHTTP, map[string]any{
		"redundancy": map[string]any{
			"receipt_timeout_ms": receiptMS,
		},
	})

	hostIDs := make([]string, 0, len(cfg.Hosts))
	for _, h := range cfg.Hosts {
		require.NotEmpty(t, h.ID)
		hostIDs = append(hostIDs, h.ID)
	}
	require.NotEmpty(t, hostIDs)

	Step(t, "stop all versiond hosts for WaitReady transport quarantine")
	for _, id := range hostIDs {
		stack.StopService(t, id)
	}
	t.Cleanup(func() {
		for _, id := range hostIDs {
			if running, err := stack.ServiceRunning(id); err == nil && running {
				continue
			}
			stack.StartService(t, id)
		}
	})

	var probe []string
	deadline := time.Now().Add(timeout)
	for round := 1; time.Now().Before(deadline); round++ {
		req := ChatCompletionRequest{
			Model: model,
			Messages: []ChatMessage{
				{Role: "user", Content: fmt.Sprintf("citest probe-quarantine drive %d", round)},
			},
			MaxTokens: 16,
		}
		_, _ = PostGatewayChatSoft(t, client, eps.GatewayHTTP, TestenvAdminAPIKey, req)

		probe = ProbeQuarantinedParticipants(
			GetParticipantThrottles(t, client, eps.GatewayHTTP, TestenvAdminAPIKey, devshardID))
		if len(probe) > 0 {
			Step(t, "probe quarantine active after %d failed chat(s): %v", round, probe)
			break
		}
	}
	require.NotEmpty(t, probe, "no participant entered probe quarantine within %s", timeout)

	Step(t, "restart versiond hosts after probe quarantine")
	for _, id := range hostIDs {
		stack.StartService(t, id)
	}
	WaitStackHealthy(t, stack, eps)
	WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)

	keep := preferKeep
	if keep == "" || !containsString(probe, keep) {
		keep = probe[0]
	}
	for _, key := range probe {
		if key == keep {
			continue
		}
		ClearParticipantQuarantine(t, client, eps.GatewayHTTP, TestenvAdminAPIKey, key)
	}
	views := GetParticipantThrottles(t, client, eps.GatewayHTTP, TestenvAdminAPIKey, devshardID)
	for _, v := range views {
		if v.ParticipantKey != keep && (v.Quarantined || v.ShadowQuarantined || v.ProbeQuarantined) {
			ClearParticipantQuarantine(t, client, eps.GatewayHTTP, TestenvAdminAPIKey, v.ParticipantKey)
		}
	}

	views = GetParticipantThrottles(t, client, eps.GatewayHTTP, TestenvAdminAPIKey, devshardID)
	require.Equal(t, []string{keep}, ProbeQuarantinedParticipants(views),
		"exactly one participant must stay probe-quarantined")
	Step(t, "single probe-quarantined participant: %s", keep)
	return keep
}

// ProbeQuarantinedParticipants returns keys currently in probe quarantine.
func ProbeQuarantinedParticipants(views []ParticipantThrottleView) []string {
	var out []string
	for _, v := range views {
		if v.ProbeQuarantined || v.QuarantineMode == "probe" {
			out = append(out, v.ParticipantKey)
		}
	}
	sort.Strings(out)
	return out
}

func containsString(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
