package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/e2e/testutil"
)

const wrongOwnerPrivateKey = "0000000000000000000000000000000000000000000000000000000000000022"

func participantAddresses(t *testing.T) []string {
	t.Helper()
	addrs := make([]string, 0, len(testutil.HostPrivateKeys))
	for _, key := range testutil.HostPrivateKeys {
		addrs = append(addrs, signerAddress(t, key))
	}
	return addrs
}

func participantThrottleViews(t *testing.T, client *http.Client, clientURL string) map[string]map[string]any {
	t.Helper()
	resp := testutil.GetJSON(t, client, clientURL+"/v1/admin/devshards/"+defaultEscrowID+"/participants")
	raw, ok := resp["participants"].([]any)
	require.True(t, ok, "participants payload: %v", resp)
	views := make(map[string]map[string]any, len(raw))
	for _, entry := range raw {
		view, ok := entry.(map[string]any)
		require.True(t, ok, "participant entry: %v", entry)
		key, _ := view["participant_key"].(string)
		require.NotEmpty(t, key, "participant entry missing key: %v", entry)
		views[key] = view
	}
	return views
}

func viewBool(view map[string]any, field string) bool {
	value, _ := view[field].(bool)
	return value
}

func pollParticipantsUntil(t *testing.T, client *http.Client, clientURL, condition string, timeout time.Duration, predicate func(map[string]map[string]any) bool) map[string]map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var views map[string]map[string]any
	for {
		views = participantThrottleViews(t, client, clientURL)
		if predicate(views) {
			return views
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for participants condition %q, last views: %v", condition, views)
		}
		time.Sleep(2 * time.Second)
	}
}

func postChatIgnoreOutcome(t *testing.T, clientURL, content string, timeout time.Duration) {
	t.Helper()
	body, err := json.Marshal(testutil.ChatCompletionBody(content, false))
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, clientURL+"/v1/chat/completions", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testutil.AdminAPIKey)
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		testutil.DebugLogf(t, "chat %q ended with client-side error (expected in failure scenarios): %v", content, err)
		return
	}
	defer resp.Body.Close()
	testutil.DebugLogf(t, "chat %q returned status=%d", content, resp.StatusCode)
}

func unquarantineParticipant(t *testing.T, client *http.Client, clientURL, participantKey string, full bool) bool {
	t.Helper()
	body := map[string]any{"participant_key": participantKey}
	if full {
		body["full"] = true
	}
	resp := testutil.PostJSON(t, client, clientURL+"/v1/admin/participants/unquarantine", body)
	cleared, _ := resp["cleared"].(bool)
	return cleared
}

func TestE2E_WrongOwner403DoesNotQuarantineHosts(t *testing.T) {
	requireE2EEnabled(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)

	images := requiredImages(t)
	env := startE2EEnv(ctx, t, images, e2eEnvOptions{ctlPrivateKey: wrongOwnerPrivateKey})
	client := &http.Client{Timeout: testutil.DefaultRequestTimeout}

	postChatIgnoreOutcome(t, env.clientURL, "wrong owner chat", 20*time.Second)

	deadline := time.Now().Add(60 * time.Second)
	var logs string
	for {
		logs = env.containerLogs(ctx, t, devshardCtlName)
		if strings.Contains(logs, "status 403") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("gateway never observed a wrong-owner 403 from hosts; logs:\n%s", logs)
		}
		time.Sleep(2 * time.Second)
	}

	require.NotContains(t, logs, "participant_limit_activated",
		"wrong-owner 403 must not activate participant quarantine")

	views := participantThrottleViews(t, client, env.clientURL)
	for _, addr := range participantAddresses(t) {
		view, ok := views[addr]
		require.True(t, ok, "participant %s missing from admin view: %v", addr, views)
		require.False(t, viewBool(view, "quarantined"), "participant %s must not be quarantined: %v", addr, view)
		require.False(t, viewBool(view, "tracked"), "participant %s must not be tracked by the limiter: %v", addr, view)
	}
}

func TestE2E_MassQuarantineSoftUnquarantineRecoversWithoutRestart(t *testing.T) {
	requireE2EEnabled(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)

	images := requiredImages(t)
	env := startE2EEnv(ctx, t, images, e2eEnvOptions{
		hostVolumeNames: sqliteHostVolumeNames(t),
	})
	client := &http.Client{Timeout: testutil.DefaultRequestTimeout}
	addrs := participantAddresses(t)

	testutil.SendCompletion(t, client, env.clientURL, "baseline before mass quarantine")

	for i := range env.hostURLs {
		env.stopHost(ctx, t, i)
	}
	postChatIgnoreOutcome(t, env.clientURL, "mass quarantine trigger", 20*time.Second)

	pollParticipantsUntil(t, client, env.clientURL, "all participants quarantined", 60*time.Second,
		func(views map[string]map[string]any) bool {
			for _, addr := range addrs {
				if !viewBool(views[addr], "quarantined") {
					return false
				}
			}
			return true
		})

	for i := range env.hostURLs {
		env.startHost(ctx, t, i)
	}
	for _, addr := range addrs {
		require.True(t, unquarantineParticipant(t, client, env.clientURL, addr, false),
			"soft unquarantine should clear quarantine state for %s", addr)
	}

	views := participantThrottleViews(t, client, env.clientURL)
	for _, addr := range addrs {
		view := views[addr]
		require.False(t, viewBool(view, "quarantined"), "participant %s should be out of quarantine: %v", addr, view)
		require.True(t, viewBool(view, "probationary"), "participant %s should be on probation: %v", addr, view)
	}

	resp := testutil.SendCompletionRaw(t, client, env.clientURL, "chat with whole escrow on probation", testutil.AdminAPIKey)
	testutil.LogRawResponse(t, "probation completion", resp)
	testutil.RequireOpenAINonStreamingCompletion(t, resp)

	for i := 0; i < 3; i++ {
		views = participantThrottleViews(t, client, env.clientURL)
		remaining := false
		for _, addr := range addrs {
			if viewBool(views[addr], "probationary") {
				remaining = true
			}
		}
		if !remaining {
			break
		}
		testutil.SendCompletion(t, client, env.clientURL, "probation recovery chat")
	}
	views = pollParticipantsUntil(t, client, env.clientURL, "probation fully drained", 30*time.Second,
		func(views map[string]map[string]any) bool {
			for _, addr := range addrs {
				if viewBool(views[addr], "probationary") || viewBool(views[addr], "tracked") {
					return false
				}
			}
			return true
		})
	testutil.DebugLogf(t, "final participant views: %v", views)

	logs := env.containerLogs(ctx, t, devshardCtlName)
	require.Contains(t, logs, "participant_limit_transport_failure", "mass quarantine should have been transport-triggered")
	require.Contains(t, logs, "participant_quarantine_cleared", "soft unquarantine should be logged")
}
