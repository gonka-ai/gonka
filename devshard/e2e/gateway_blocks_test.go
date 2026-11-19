package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"devshard/e2e/testutil"
)

const (
	cutOffBurnWindow   = 60 * time.Second
	cutOffPollInterval = time.Second
)

func gatewayParticipants(t *testing.T, client *http.Client, clientURL string) []string {
	t.Helper()
	status, body := gatewayGet(t, client, clientURL+"/v1/admin/devshards/"+defaultEscrowID+"/participants", testutil.AdminAPIKey)
	if status != http.StatusOK {
		t.Fatalf("participants = %d %s", status, body)
	}
	var listed struct {
		Participants []string `json:"participants"`
	}
	if err := json.Unmarshal([]byte(body), &listed); err != nil {
		t.Fatalf("participants are not JSON: %v (%s)", err, body)
	}
	if len(listed.Participants) < 3 {
		t.Fatalf("the escrow lists %d participants, want the three the stack runs", len(listed.Participants))
	}
	return listed.Participants
}

// Test flow:
//  1. Start the default three-host gateway environment.
//  2. Narrow the allowlist to two of the three participants.
//  3. Send nine completions and assert the allowed two served some of them.
//  4. Assert the excluded slot burned its nonces under participant_outside_allowlist rather than routing or losing them.
func TestE2E_GatewayBurnsTheNoncesOfAHostOutsideTheAllowlist(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	participants := gatewayParticipants(t, client, env.clientURL)
	putSettings(t, client, env.clientURL, map[string]any{"participant_allowlist": participants[:2]})

	served := 0
	for request := range 9 {
		if response := testutil.SendCompletionRaw(t, client, env.clientURL,
			fmt.Sprintf("allowlisted %d", request), testutil.AdminAPIKey); response.StatusCode == http.StatusOK {
			served++
		}
	}
	if served == 0 {
		t.Fatal("an allowlist of two of three hosts served nothing at all")
	}

	burned := awaitGhostBurns(t, client, env.statsURL, "participant_outside_allowlist", 60*time.Second)
	if burned == 0 {
		t.Error("no nonce was burned for the excluded host: its slot either kept routing or lost its nonces silently")
	}
}

// Test flow:
//  1. Start the default three-host gateway environment.
//  2. Narrow the allowlist to a single host, then clear it again.
//  3. Assert the group serves once more, without a restart.
func TestE2E_GatewayKeepsServingWhenTheAllowlistIsPutBack(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	participants := gatewayParticipants(t, client, env.clientURL)
	putSettings(t, client, env.clientURL, map[string]any{"participant_allowlist": participants[:1]})
	putSettings(t, client, env.clientURL, map[string]any{"participant_allowlist": []string{}})

	testutil.RequireOpenAINonStreamingCompletion(t,
		testutil.SendCompletionRaw(t, client, env.clientURL, "allowlist lifted", testutil.AdminAPIKey))
}

// Test flow:
//  1. Start the three-host environment with one host unreachable.
//  2. Set a cutoff that trips on the first transport fault.
//  3. Keep sending one completion per poll until a nonce is burned as cut off, since the dead host's
//     first send fails only once the gateway's connect wait runs out.
//  4. Assert the cut-off host's nonces are burned as cut off rather than offered again.
func TestE2E_GatewayBurnsTheNoncesOfACutOffHost(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	putSettings(t, client, env.clientURL, map[string]any{
		"host_cutoff_after_failures": 1,
		"host_cutoff_ms":             60000,
		"host_cutoff_max_ms":         60000,
	})

	ctx, cancel := context.WithTimeout(context.Background(), testutil.DefaultRequestTimeout)
	t.Cleanup(cancel)
	env.stopHost(ctx, t, 1)

	deadline := time.Now().Add(cutOffBurnWindow)
	var burned uint64
	for request := 0; burned == 0 && time.Now().Before(deadline); request++ {
		testutil.SendCompletionRaw(t, client, env.clientURL,
			fmt.Sprintf("cutting off %d", request), testutil.AdminAPIKey)
		burned = gatewayGhostBurns(t, client, env.statsURL, "participant_cut_off_no_send")
		time.Sleep(cutOffPollInterval)
	}

	if burned == 0 {
		t.Errorf("an unreachable host was still being offered work; the burns that did land were %v",
			gatewayGhostReasons(t, client, env.statsURL))
	}
	scrape := gatewayScrape(t, client, env.clientURL)
	if !strings.Contains(scrape, "devshard_gateway_participant_breaker_state") {
		t.Error("the breaker state is not published, so an operator cannot see which host is cut off")
	}
}

// Test flow:
//  1. Start the three-host environment with one host answering HTTP 503.
//  2. Set a cutoff that trips on the first counted fault.
//  3. Send nine sequential completions, so the refusing host carries none of this gateway's other work.
//  4. Assert the host is cut off and its nonces are burned as cut off.
func TestE2E_GatewayCutsOffAHostRefusingWhileIdle(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{
		hostEnvOverrides: map[int]map[string]string{1: brokenHost("503", "busy")},
	})

	putSettings(t, client, env.clientURL, map[string]any{
		"host_cutoff_after_failures": 1,
		"host_cutoff_ms":             60000,
		"host_cutoff_max_ms":         60000,
	})

	for request := range 9 {
		testutil.SendCompletionRaw(t, client, env.clientURL,
			fmt.Sprintf("refusing host %d", request), testutil.AdminAPIKey)
	}

	if burned := awaitGhostBurns(t, client, env.statsURL, "participant_cut_off_no_send", 60*time.Second); burned == 0 {
		t.Errorf("a host refusing with 503 while idle kept being offered work; the burns were %v",
			gatewayGhostReasons(t, client, env.statsURL))
	}
}

// Test flow:
//  1. Start the three-host environment with one host answering HTTP 500.
//  2. Set a half-second cutoff that trips after three failures, leaving the longer ejection behind it,
//     and let the ejection take a host of three by dropping the available-host floor.
//  3. Send ten completions to eject the host, then wait past the cutoff and send six more.
//  4. Assert the ejection is what blocks it now, burning its nonces under participant_ejected_no_send.
func TestE2E_GatewayBurnsTheNoncesOfAnEjectedHostOnceItsCutoffLapses(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{
		hostEnvOverrides: map[int]map[string]string{1: brokenHost("500", "host is broken")},
	})

	putSettings(t, client, env.clientURL, map[string]any{
		"host_cutoff_after_failures": 3,
		"host_cutoff_ms":             500,
		"host_cutoff_max_ms":         500,
		"perf_min_available_hosts":   0,
	})

	for request := range 10 {
		testutil.SendCompletionRaw(t, client, env.clientURL,
			fmt.Sprintf("ejecting %d", request), testutil.AdminAPIKey)
	}
	time.Sleep(3 * time.Second)
	for request := range 6 {
		testutil.SendCompletionRaw(t, client, env.clientURL,
			fmt.Sprintf("after the cutoff %d", request), testutil.AdminAPIKey)
	}

	if burned := awaitGhostBurns(t, client, env.statsURL, "participant_ejected_no_send", 60*time.Second); burned == 0 {
		t.Errorf("an ejected host was still being offered work once its cutoff lapsed; the burns were %v",
			gatewayGhostReasons(t, client, env.statsURL))
	}
}

// gatewayGhostReasons is every burn reason the ledger holds, so a scenario that expected one can say
// which arrived instead of only that its own did not.
func gatewayGhostReasons(t *testing.T, client *http.Client, statsURL string) map[string]uint64 {
	t.Helper()
	body := testutil.GetJSON(t, client, statsURL+"/api/v1/epochs/current/participants")
	participants, _ := body["participants"].([]any)
	reasons := map[string]uint64{}
	for _, rawParticipant := range participants {
		participant, ok := rawParticipant.(map[string]any)
		if !ok {
			continue
		}
		counters, _ := participant["counters"].([]any)
		for _, rawCounter := range counters {
			counter, ok := rawCounter.(map[string]any)
			if !ok || counter["disposition"] != "ghost" {
				continue
			}
			reason, _ := counter["ghost_reason"].(string)
			reasons[reason] += testutil.NumericField(t, counter, "count")
		}
	}
	return reasons
}

func awaitGhostBurns(t *testing.T, client *http.Client, statsURL, reason string, within time.Duration) uint64 {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		if burned := gatewayGhostBurns(t, client, statsURL, reason); burned > 0 {
			return burned
		}
		if time.Now().After(deadline) {
			return 0
		}
		time.Sleep(time.Second)
	}
}
