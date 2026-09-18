//go:build testenvci

package citest

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"

	"github.com/stretchr/testify/require"
)

const (
	undersizedEscrowBalance  = uint64(100_000)
	fallbackEscrowBalance    = uint64(300_000)
	secondSmallEscrowBalance = uint64(150_000)
)

type fundingFallbackEnv struct {
	client         *http.Client
	gatewayURL     string
	model          string
	firstEscrowID  string
	secondEscrowID string
}

type fundingFallbackStatus struct {
	Runtimes  int                    `json:"runtimes"`
	Devshards []fundingRuntimeStatus `json:"devshards"`
}

type fundingRuntimeStatus struct {
	ID               string `json:"id"`
	Active           bool   `json:"active"`
	Nonce            uint64 `json:"nonce"`
	Balance          uint64 `json:"balance"`
	ActiveRequests   int64  `json:"active_requests"`
	ReservedTokens   int64  `json:"reserved_tokens"`
	PendingRaceClean int64  `json:"pending_race_cleanup"`
}

// TestGatewayMovesOversizedRequestToAnotherEscrowWithoutRetiringTheFirst exercises the
// full HTTP gateway -> proxy -> state machine -> versiond -> mock-openai path. One request
// may be too expensive for an escrow without making that escrow unusable for smaller work.
func TestGatewayMovesOversizedRequestToAnotherEscrowWithoutRetiringTheFirst(t *testing.T) {
	env := bootFundingFallbackEnv(t, "citest-escrow-funding-fallback-*", undersizedEscrowBalance, fallbackEscrowBalance)

	before := waitForFundingFallbackRuntimes(t, env.client, env.gatewayURL, 2)
	firstBefore := requireFundingRuntime(t, before, env.firstEscrowID)

	// With the testenv token price (100), this reservation cannot fit in escrow 1 but does fit in escrow 2.
	result := harness.PostGatewayChatHTTP(t, env.client, env.gatewayURL, harness.TestenvAdminAPIKey, harness.ChatCompletionRequest{
		Model:     env.model,
		Messages:  []harness.ChatMessage{{Role: "user", Content: "funding fallback streaming request"}},
		MaxTokens: 2048,
		Stream:    true,
	})
	require.Equal(t, http.StatusOK, result.Status, string(result.Body))
	require.Equal(t, env.secondEscrowID, result.Header.Get("X-Devshard-ID"))
	chunks, sawDone := harness.ParseSSEDataChunks(result.Body)
	require.True(t, sawDone, "fallback stream did not finish: %s", string(result.Body))
	require.NotEmpty(t, harness.AssembleSSEContent(chunks))

	afterFallback := waitForFundingFallbackRuntimes(t, env.client, env.gatewayURL, 2)
	firstAfter := requireFundingRuntime(t, afterFallback, env.firstEscrowID)
	require.True(t, firstAfter.Active, "an oversized request retired the first escrow")
	require.Equal(t, firstBefore.Nonce, firstAfter.Nonce, "a refused reservation consumed a nonce")
	require.Equal(t, firstBefore.Balance, firstAfter.Balance, "a refused reservation consumed balance")
	require.Zero(t, firstAfter.ActiveRequests)
	require.Zero(t, firstAfter.ReservedTokens)
	require.Zero(t, firstAfter.PendingRaceClean)

	// The escrow that refused the large reservation must still serve work that fits its balance.
	small := harness.PostGatewayChatCompletion(t, env.client, env.gatewayURL+"/devshard/"+env.firstEscrowID, harness.TestenvAdminAPIKey, harness.ChatCompletionRequest{
		Model:     env.model,
		Messages:  []harness.ChatMessage{{Role: "user", Content: "small request for the original escrow"}},
		MaxTokens: 128,
	})
	harness.RequireMockOpenAIContent(t, small.Choices[0].Message.Content)
}

// TestGatewayReturnsRetryable503WhenNoEscrowCanFundRequest proves that a request-specific
// funding refusal is non-destructive even when every escrow refuses the same request.
func TestGatewayReturnsRetryable503WhenNoEscrowCanFundRequest(t *testing.T) {
	env := bootFundingFallbackEnv(t, "citest-all-escrows-refuse-*", undersizedEscrowBalance, secondSmallEscrowBalance)

	// Establish that both escrows can serve ordinary work before asking for an oversized reservation.
	for _, id := range []string{env.firstEscrowID, env.secondEscrowID} {
		response := harness.PostGatewayChatCompletion(t, env.client, env.gatewayURL+"/devshard/"+id, harness.TestenvAdminAPIKey, harness.ChatCompletionRequest{
			Model:     env.model,
			Messages:  []harness.ChatMessage{{Role: "user", Content: "baseline request for escrow " + id}},
			MaxTokens: 32,
		})
		harness.RequireMockOpenAIContent(t, response.Choices[0].Message.Content)
	}
	before := waitForFundingFallbackRuntimes(t, env.client, env.gatewayURL, 2)

	tooLarge := harness.PostGatewayChatHTTP(t, env.client, env.gatewayURL, harness.TestenvAdminAPIKey, harness.ChatCompletionRequest{
		Model:     env.model,
		Messages:  []harness.ChatMessage{{Role: "user", Content: "request too large for every escrow"}},
		MaxTokens: 2048,
		Stream:    true,
	})
	require.Equal(t, http.StatusServiceUnavailable, tooLarge.Status, string(tooLarge.Body))
	require.NotEmpty(t, tooLarge.Header.Get("Retry-After"))
	require.Empty(t, tooLarge.Header.Get("X-Devshard-ID"))
	require.NotContains(t, tooLarge.ContentType, "text/event-stream")
	require.Contains(t, string(tooLarge.Body), "no escrow can fund this request (2 refused)")

	finalStatus := waitForFundingFallbackRuntimes(t, env.client, env.gatewayURL, 2)
	require.Len(t, finalStatus.Devshards, 2, "a funding refusal reminted an escrow")
	for _, id := range []string{env.firstEscrowID, env.secondEscrowID} {
		beforeRuntime := requireFundingRuntime(t, before, id)
		afterRuntime := requireFundingRuntime(t, finalStatus, id)
		require.True(t, afterRuntime.Active, "escrow %s was retired after refusing an oversized request", id)
		require.Equal(t, beforeRuntime.Nonce, afterRuntime.Nonce, "escrow %s consumed a nonce for a refused reservation", id)
		require.Equal(t, beforeRuntime.Balance, afterRuntime.Balance, "escrow %s consumed balance for a refused reservation", id)
		require.Zero(t, afterRuntime.ActiveRequests)
		require.Zero(t, afterRuntime.ReservedTokens)
		require.Zero(t, afterRuntime.PendingRaceClean)
	}

	// A smaller request still succeeds after the retryable 503.
	recovery := harness.PostGatewayChatCompletion(t, env.client, env.gatewayURL, harness.TestenvAdminAPIKey, harness.ChatCompletionRequest{
		Model:     env.model,
		Messages:  []harness.ChatMessage{{Role: "user", Content: "small request after all escrows refused"}},
		MaxTokens: 32,
	})
	harness.RequireMockOpenAIContent(t, recovery.Choices[0].Message.Content)
}

func bootFundingFallbackEnv(t *testing.T, prefix string, firstBalance, secondBalance uint64) fundingFallbackEnv {
	t.Helper()
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack := harness.NewStack(t, prefix)
	harness.RequireLinuxDevshardd(t, stack.TestenvDir)
	harness.WriteMultiConfig(t, stack.WorkDir, harness.MultiConfigOpts{
		Hosts:        2,
		EscrowSlots:  2,
		EscrowAmount: firstBalance,
	})
	stack.RunGencompose(t)
	cfg := stack.LoadConfig(t)
	stack.Up(t)
	eps := stack.Endpoints(t, cfg)
	client := harness.GatewayChatClient()
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "versiond-0", "versiond-1", "mock-openai", "mock-chain")
		}
	})

	harness.WaitStackHealthy(t, stack, eps)
	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)

	model := config.PrimaryModelID(cfg)
	firstEscrowID := fmt.Sprint(cfg.Escrows[0].ID)
	var created struct {
		EscrowID uint64 `json:"escrow_id"`
	}
	require.NoError(t, harness.PostGatewayAdminJSON(client, eps.GatewayHTTP+"/v1/admin/escrows", map[string]any{
		"amount":   secondBalance,
		"model_id": model,
		"register": true,
	}, &created))
	secondEscrowID := fmt.Sprint(created.EscrowID)
	require.NotEqual(t, firstEscrowID, secondEscrowID)
	waitForFundingFallbackRuntimes(t, client, eps.GatewayHTTP, 2)
	return fundingFallbackEnv{
		client:         client,
		gatewayURL:     eps.GatewayHTTP,
		model:          model,
		firstEscrowID:  firstEscrowID,
		secondEscrowID: secondEscrowID,
	}
}

func waitForFundingFallbackRuntimes(t *testing.T, client *http.Client, gatewayURL string, count int) fundingFallbackStatus {
	t.Helper()
	var status fundingFallbackStatus
	ok := harness.AssertEventually(t, 2*time.Minute, time.Second, func() bool {
		status = fundingFallbackStatus{}
		return harness.GetJSON(client, gatewayURL+"/v1/status", &status) == nil &&
			status.Runtimes == count && len(status.Devshards) == count
	})
	require.True(t, ok, "gateway did not expose %d runtimes: %+v", count, status)
	return status
}

func requireFundingRuntime(t *testing.T, status fundingFallbackStatus, id string) fundingRuntimeStatus {
	t.Helper()
	for _, runtime := range status.Devshards {
		if runtime.ID == id {
			return runtime
		}
	}
	t.Fatalf("runtime %s missing from gateway status: %+v", id, status)
	return fundingRuntimeStatus{}
}
