package e2e

import (
	"net/http"
	"strings"
	"testing"

	"devshard/e2e/testutil"
)

func gatewayGet(t *testing.T, client *http.Client, url, bearer string) (int, string) {
	t.Helper()
	resp := testutil.GetRaw(t, client, url, bearer)
	return resp.StatusCode, resp.Body
}

// Test flow:
//  1. Start the three-host environment with the gateway disabled.
//  2. Assert a completion is answered 503.
//  3. Assert the scrape still answers, so the gateway can be diagnosed.
//  4. Assert admin state still answers, so the escrow stays settleable.
func TestE2E_GatewayKillSwitchLeavesTheOperatorSurfaceUp(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{
		gatewayEnvOverrides: map[string]string{"DEVSHARD_GATEWAY_DISABLED": "true"},
	})

	if resp := testutil.SendCompletionRaw(t, client, env.clientURL, "while disabled", testutil.AdminAPIKey); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("chat while disabled = %d %s, want 503", resp.StatusCode, resp.Body)
	}
	if status, _ := gatewayGet(t, client, env.clientURL+"/metrics", ""); status != http.StatusOK {
		t.Errorf("/metrics while disabled = %d, want 200: a gateway nobody can scrape cannot be diagnosed", status)
	}
	if status, _ := gatewayGet(t, client, env.clientURL+"/v1/admin/state", testutil.AdminAPIKey); status != http.StatusOK {
		t.Errorf("admin state while disabled = %d, want 200: the escrow still has to be settleable", status)
	}
}

// Test flow:
//  1. Start the three-host environment with no admin key configured.
//  2. Read an operator route and assert 404: the surface is absent, not merely closed.
func TestE2E_GatewayWithoutAnAdminKeyHidesTheOperatorRoutes(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{
		gatewayEnvOverrides: map[string]string{"DEVSHARD_ADMIN_API_KEY": ""},
	})

	if status, body := gatewayGet(t, client, env.clientURL+"/v1/admin/state", testutil.AdminAPIKey); status != http.StatusNotFound {
		t.Errorf("admin state with no key configured = %d %s, want 404", status, body)
	}
}

// Test flow:
//  1. Start the default three-host gateway environment.
//  2. Send a completion pinned to the escrow the stack runs and assert it is served.
//  3. Send one pinned to an escrow that does not exist and assert it is not.
func TestE2E_GatewayServesAPinnedEscrowAndRefusesAnUnknownPin(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	pinned := testutil.PostJSONRaw(t, client,
		env.clientURL+"/devshard/"+defaultEscrowID+"/v1/chat/completions",
		testutil.ChatCompletionBody("pinned", false), testutil.AdminAPIKey)
	testutil.RequireOpenAINonStreamingCompletion(t, pinned)

	unknown := testutil.PostJSONRaw(t, client,
		env.clientURL+"/devshard/999999/v1/chat/completions",
		testutil.ChatCompletionBody("unknown pin", false), testutil.AdminAPIKey)
	if unknown.StatusCode == http.StatusOK {
		t.Errorf("a pin to an escrow that does not exist was served anyway: %s", unknown.Body)
	}
}

// Test flow:
//  1. Start the default three-host gateway environment.
//  2. Serve a plain completion, an escrow-pinned one, and a path needing normalisation.
//  3. Assert the scrape labels them by route template rather than by literal path.
func TestE2E_GatewayScrapeCarriesTemplatedRouteLabels(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	testutil.RequireOpenAINonStreamingCompletion(t,
		testutil.SendCompletionRaw(t, client, env.clientURL, "labelled", testutil.AdminAPIKey))
	testutil.RequireOpenAINonStreamingCompletion(t, testutil.PostJSONRaw(t, client,
		env.clientURL+"/devshard/"+defaultEscrowID+"/v1/chat/completions",
		testutil.ChatCompletionBody("labelled pin", false), testutil.AdminAPIKey))
	gatewayGet(t, client, env.clientURL+"/v1/../v1/models", "")

	status, scrape := gatewayGet(t, client, env.clientURL+"/metrics", "")
	if status != http.StatusOK {
		t.Fatalf("/metrics = %d", status)
	}
	if !strings.Contains(scrape, `path="/v1/chat/completions"`) {
		t.Error("the scrape carries no route label for the chat route")
	}
	if !strings.Contains(scrape, `path="/devshard/{id}/v1/chat/completions"`) {
		t.Error("the pinned chat route raised no series, so the templating below is unchecked")
	}
	for line := range strings.SplitSeq(scrape, "\n") {
		if strings.Contains(line, `path="/devshard/`) && !strings.Contains(line, "{id}") {
			t.Errorf("a route label carries a concrete escrow id, which grows the series with the escrow set: %s", line)
		}
	}
}
