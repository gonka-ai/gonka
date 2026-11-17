package harness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"testing"
	"time"

	"devshard/testenv/mockopenai"

	"github.com/stretchr/testify/require"
)

// QuarantineModeShadow is the limiter mode that keeps sending real traffic to a
// host but refuses to crown it winner. Probe mode, by contrast, stops sending —
// which would hide the multi-host attempt shape these citests exist to prove.
const QuarantineModeShadow = "shadow"

// ParticipantThrottleView is the part of the admin participants snapshot the
// quarantine citests read (see ParticipantThrottleSnapshot in devshardctl).
type ParticipantThrottleView struct {
	ParticipantKey    string `json:"participant_key"`
	Quarantined       bool   `json:"quarantined"`
	Blocked           bool   `json:"blocked"`
	RequestAllowed    bool   `json:"request_allowed"`
	QuarantineMode    string `json:"quarantine_mode"`
	ShadowQuarantined bool   `json:"shadow_quarantined"`
	ProbeQuarantined  bool   `json:"probe_quarantined"`
}

// GetParticipantThrottles reads GET /v1/admin/devshards/{id}/participants.
func GetParticipantThrottles(t *testing.T, client *http.Client, gatewayURL, adminAPIKey, devshardID string) []ParticipantThrottleView {
	t.Helper()
	if client == nil {
		client = HTTPClient()
	}
	var body struct {
		Participants []ParticipantThrottleView `json:"participants"`
	}
	url := fmt.Sprintf("%s/v1/admin/devshards/%s/participants", gatewayURL, devshardID)
	require.NoError(t, getGatewayJSON(t, client, url, adminAPIKey, &body))
	return body.Participants
}

// ShadowQuarantinedParticipants returns the keys currently in shadow quarantine.
func ShadowQuarantinedParticipants(views []ParticipantThrottleView) []string {
	var out []string
	for _, v := range views {
		if v.ShadowQuarantined || v.QuarantineMode == QuarantineModeShadow {
			out = append(out, v.ParticipantKey)
		}
	}
	sort.Strings(out)
	return out
}

// ForceShadowQuarantine drives ML failures until at least one participant is
// shadow-quarantined, then clears the fault so later chats can succeed.
// Returns the shadow participant keys.
//
// Recipe: empty_stream_threshold=1 turns a single failed attempt into a
// quarantine transition, and a 503 from mock-openai makes every attempt fail.
func ForceShadowQuarantine(
	t *testing.T,
	client *http.Client,
	gatewayURL, mockOpenAIURL, devshardID, model string,
	timeout time.Duration,
) []string {
	t.Helper()
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	if client == nil {
		client = GatewayChatClient()
	}

	PatchGatewayAdminSettings(t, client, gatewayURL, map[string]any{
		"participant_throttle": map[string]any{
			"empty_stream_threshold": 1,
		},
	})
	status := http.StatusServiceUnavailable
	PatchMockOpenAIFault(t, client, mockOpenAIURL, mockopenai.FaultPatch{HTTPStatus: &status})
	defer ResetMockOpenAIFault(t, client, mockOpenAIURL)

	var shadow []string
	deadline := time.Now().Add(timeout)
	for round := 1; time.Now().Before(deadline); round++ {
		req := ChatCompletionRequest{
			Model: model,
			Messages: []ChatMessage{
				{Role: "user", Content: fmt.Sprintf("citest shadow-quarantine drive %d", round)},
			},
			MaxTokens: 16,
		}
		_, _ = PostGatewayChatSoft(t, client, gatewayURL, TestenvAdminAPIKey, req)

		shadow = ShadowQuarantinedParticipants(
			GetParticipantThrottles(t, client, gatewayURL, TestenvAdminAPIKey, devshardID))
		if len(shadow) > 0 {
			Step(t, "shadow quarantine active after %d failed chat(s): %v", round, shadow)
			return shadow
		}
		time.Sleep(2 * time.Second)
	}

	require.NotEmpty(t, shadow, "no participant entered shadow quarantine within %s", timeout)
	return shadow
}

// ClearParticipantQuarantine lifts quarantine for one participant via
// POST /v1/admin/participants/unquarantine.
func ClearParticipantQuarantine(t *testing.T, client *http.Client, gatewayURL, adminAPIKey, participantKey string) {
	t.Helper()
	if client == nil {
		client = HTTPClient()
	}
	body, err := json.Marshal(map[string]string{"participant_key": participantKey})
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, gatewayURL+"/v1/admin/participants/unquarantine", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	if adminAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+adminAPIKey)
	}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "POST /v1/admin/participants/unquarantine")
}

// ForceOneShadowQuarantine leaves exactly one participant in shadow quarantine
// and every other participant healthy, returning the shadow key.
//
// The drive faults ML for the whole stack, so every participant that takes an
// attempt is struck — with two participants both end up quarantined, and a chat
// then has no host allowed to win, which fails the client request. Clearing all
// but one restores the shape the multi-host attempt citest needs: a suspicious
// primary that forces the gateway onto a second, healthy host.
func ForceOneShadowQuarantine(
	t *testing.T,
	client *http.Client,
	gatewayURL, mockOpenAIURL, devshardID, model string,
	timeout time.Duration,
) string {
	t.Helper()
	shadow := ForceShadowQuarantine(t, client, gatewayURL, mockOpenAIURL, devshardID, model, timeout)
	require.NotEmpty(t, shadow)

	keep := shadow[0]
	for _, key := range shadow[1:] {
		ClearParticipantQuarantine(t, client, gatewayURL, TestenvAdminAPIKey, key)
	}
	// Also clear anything quarantined by a later strike (e.g. a probation
	// participant that tipped over on the last drive round).
	views := GetParticipantThrottles(t, client, gatewayURL, TestenvAdminAPIKey, devshardID)
	for _, v := range views {
		if v.ParticipantKey != keep && (v.Quarantined || v.ShadowQuarantined || v.ProbeQuarantined) {
			ClearParticipantQuarantine(t, client, gatewayURL, TestenvAdminAPIKey, v.ParticipantKey)
		}
	}

	views = GetParticipantThrottles(t, client, gatewayURL, TestenvAdminAPIKey, devshardID)
	require.Equal(t, []string{keep}, ShadowQuarantinedParticipants(views),
		"exactly one participant must stay shadow-quarantined")
	Step(t, "single shadow-quarantined participant: %s", keep)
	return keep
}

// RequireShadowQuarantineMode asserts the limiter reports shadow (not probe)
// for key, so the host still receives real attempts.
func RequireShadowQuarantineMode(t *testing.T, views []ParticipantThrottleView, key string) {
	t.Helper()
	for _, v := range views {
		if v.ParticipantKey != key {
			continue
		}
		require.True(t, v.ShadowQuarantined || v.QuarantineMode == QuarantineModeShadow,
			"participant %s is not shadow-quarantined: %+v", key, v)
		require.False(t, v.ProbeQuarantined, "participant %s is probe-quarantined; probes do not send real traffic", key)
		require.True(t, v.RequestAllowed, "shadow quarantine must keep the participant request-allowed: %+v", v)
		return
	}
	t.Fatalf("participant %s missing from throttle snapshot", key)
}
