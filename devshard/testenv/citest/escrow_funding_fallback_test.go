//go:build testenvci

package citest

import (
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"

	"github.com/stretchr/testify/require"
)

const (
	// A reservation costs (normalized body bytes + max_tokens) * token price, so the balances below sit
	// either side of oversizedReservation with room for the body.
	oversizedMaxTokens   = 2048
	oversizedReservation = oversizedMaxTokens * config.DefaultTokenPrice
	fittingMaxTokens     = 128

	unfundableEscrowBalance       = oversizedReservation / 2
	secondUnfundableEscrowBalance = oversizedReservation * 3 / 4
	fundedEscrowBalance           = oversizedReservation * 2

	settledEscrowsTimeout  = 2 * time.Minute
	settledEscrowsInterval = time.Second
)

type fundingFallbackEnv struct {
	stack          *harness.Stack
	client         *http.Client
	gatewayURL     string
	model          string
	firstEscrowID  string
	secondEscrowID string
}

type fundingFallbackStatus struct {
	Devshards []fundingRuntimeStatus `json:"devshards"`
}

type fundingRuntimeStatus struct {
	ID                 string `json:"id"`
	Active             bool   `json:"active"`
	Nonce              uint64 `json:"nonce"`
	Balance            uint64 `json:"balance"`
	ActiveRequests     int64  `json:"active_requests"`
	PendingRaceCleanup int64  `json:"pending_race_cleanup"`
	ReservedTokens     int64  `json:"reserved_tokens"`
}

// Test flow:
//  1. Boot the stack with escrow 1 too small for an oversized request, then register escrow 2 that can pay for it.
//  2. Wait until both escrows are idle and snapshot escrow 1's nonce and balance.
//  3. Send a streaming request whose reservation only escrow 2 covers.
//  4. Assert escrow 2 answered it and the gateway log shows escrow 1 refused to fund it first.
//  5. Assert escrow 1 kept its nonce, its balance and its place in service.
//  6. Send a small request straight to escrow 1 and assert it is still served.
func TestGatewayMovesOversizedRequestToAnotherEscrowWithoutRetiringTheFirst(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)
	env := bootFundingFallbackEnv(t, "citest-escrow-funding-fallback-*", unfundableEscrowBalance, fundedEscrowBalance)

	before := waitForSettledEscrows(t, env, 2)
	firstBefore := requireFundingRuntime(t, before, env.firstEscrowID)
	require.NotZero(t, firstBefore.Balance, "the status carries no balance, so comparing it would prove nothing")

	harness.Step(t, "an oversized request must move from escrow %s to escrow %s", env.firstEscrowID, env.secondEscrowID)
	result := harness.PostGatewayChatHTTP(t, env.client, env.gatewayURL, harness.TestenvAdminAPIKey, harness.ChatCompletionRequest{
		Model:     env.model,
		Messages:  []harness.ChatMessage{{Role: "user", Content: "funding fallback streaming request"}},
		MaxTokens: oversizedMaxTokens,
		Stream:    true,
	})
	require.Equal(t, http.StatusOK, result.Status, "body=%s", result.Body)
	require.Equal(t, env.secondEscrowID, result.Header.Get("X-Devshard-ID"))
	chunks, sawDone := harness.ParseSSEDataChunks(result.Body)
	require.True(t, sawDone, "fallback stream did not finish: %s", string(result.Body))
	require.NotEmpty(t, harness.AssembleSSEContent(chunks))
	requireEscrowRefusedFunding(t, env, env.firstEscrowID)

	afterFallback := waitForSettledEscrows(t, env, 2)
	requireEscrowUntouched(t, firstBefore, requireFundingRuntime(t, afterFallback, env.firstEscrowID))

	harness.Step(t, "escrow %s must still serve work that fits its balance", env.firstEscrowID)
	small := harness.PostGatewayChatCompletion(t, env.client, env.gatewayURL+"/devshard/"+env.firstEscrowID, harness.TestenvAdminAPIKey, harness.ChatCompletionRequest{
		Model:     env.model,
		Messages:  []harness.ChatMessage{{Role: "user", Content: "small request for the original escrow"}},
		MaxTokens: fittingMaxTokens,
	})
	harness.RequireMockOpenAIContent(t, small.Choices[0].Message.Content)
}

// Test flow:
//  1. Boot the stack with two escrows, neither able to fund an oversized request.
//  2. Serve one ordinary request on each escrow, then wait until both are idle and snapshot them.
//  3. Send a request no escrow can fund.
//  4. Assert a retryable 503 that counts both refusals, names no escrow and carries no SSE content type.
//  5. Assert both escrows kept their nonce, balance and place in service.
//  6. Send a small request through the pool and assert the refusal was per-request, not sticky.
func TestGatewayReturnsRetryable503WhenNoEscrowCanFundRequest(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)
	env := bootFundingFallbackEnv(t, "citest-all-escrows-refuse-*", unfundableEscrowBalance, secondUnfundableEscrowBalance)

	harness.Step(t, "both escrows must serve ordinary work before the oversized request")
	for _, escrowID := range []string{env.firstEscrowID, env.secondEscrowID} {
		response := harness.PostGatewayChatCompletion(t, env.client, env.gatewayURL+"/devshard/"+escrowID, harness.TestenvAdminAPIKey, harness.ChatCompletionRequest{
			Model:     env.model,
			Messages:  []harness.ChatMessage{{Role: "user", Content: "baseline request for escrow " + escrowID}},
			MaxTokens: fittingMaxTokens,
		})
		harness.RequireMockOpenAIContent(t, response.Choices[0].Message.Content)
	}
	before := waitForSettledEscrows(t, env, 2)

	harness.Step(t, "an oversized request no escrow can fund must answer a retryable 503")
	tooLarge := harness.PostGatewayChatHTTP(t, env.client, env.gatewayURL, harness.TestenvAdminAPIKey, harness.ChatCompletionRequest{
		Model:     env.model,
		Messages:  []harness.ChatMessage{{Role: "user", Content: "request too large for every escrow"}},
		MaxTokens: oversizedMaxTokens,
		Stream:    true,
	})
	require.Equal(t, http.StatusServiceUnavailable, tooLarge.Status, "body=%s", tooLarge.Body)
	require.NotEmpty(t, tooLarge.Header.Get("Retry-After"))
	require.Empty(t, tooLarge.Header.Get("X-Devshard-ID"))
	require.NotContains(t, tooLarge.ContentType, "text/event-stream")
	// The count is the only evidence in the response that both escrows were offered the request.
	require.Contains(t, string(tooLarge.Body), "no escrow can fund this request (2 refused)")

	finalStatus := waitForSettledEscrows(t, env, 2)
	for _, escrowID := range []string{env.firstEscrowID, env.secondEscrowID} {
		requireEscrowUntouched(t, requireFundingRuntime(t, before, escrowID), requireFundingRuntime(t, finalStatus, escrowID))
	}

	harness.Step(t, "a smaller request still succeeds after the retryable 503")
	recovery := harness.PostGatewayChatCompletion(t, env.client, env.gatewayURL, harness.TestenvAdminAPIKey, harness.ChatCompletionRequest{
		Model:     env.model,
		Messages:  []harness.ChatMessage{{Role: "user", Content: "small request after all escrows refused"}},
		MaxTokens: fittingMaxTokens,
	})
	harness.RequireMockOpenAIContent(t, recovery.Choices[0].Message.Content)
}

func bootFundingFallbackEnv(t *testing.T, prefix string, firstBalance, secondBalance uint64) fundingFallbackEnv {
	t.Helper()
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
	harness.WaitGETOK(t, client, eps.RouterHTTP+"/"+cfg.Versiond.VersionName+"/healthz", 5*time.Minute, "devshardd health via router", stack)

	model := config.PrimaryModelID(cfg)
	var created struct {
		EscrowID uint64 `json:"escrow_id"`
	}
	require.NoError(t, harness.PostGatewayJSON(client, eps.GatewayHTTP+"/v1/admin/escrows", harness.TestenvAdminAPIKey, map[string]any{
		"amount":   secondBalance,
		"model_id": model,
		"register": true,
	}, &created))
	env := fundingFallbackEnv{
		stack:          stack,
		client:         client,
		gatewayURL:     eps.GatewayHTTP,
		model:          model,
		firstEscrowID:  fmt.Sprint(cfg.Escrows[0].ID),
		secondEscrowID: fmt.Sprint(created.EscrowID),
	}
	require.NotEqual(t, env.firstEscrowID, env.secondEscrowID)
	waitForSettledEscrows(t, env, 2)
	return env
}

// A fresh escrow warms its hosts in the background, which moves a nonce and a balance on its own.
func waitForSettledEscrows(t *testing.T, env fundingFallbackEnv, expectedEscrows int) fundingFallbackStatus {
	t.Helper()
	var previous, status fundingFallbackStatus
	detail := "no status read"
	settled := harness.AssertEventually(t, settledEscrowsTimeout, settledEscrowsInterval, func() bool {
		previous, status = status, fundingFallbackStatus{}
		if err := harness.GetJSON(env.client, env.gatewayURL+"/v1/status", &status); err != nil {
			detail = err.Error()
			return false
		}
		if len(status.Devshards) != expectedEscrows {
			detail = fmt.Sprintf("escrows in service: %+v", status.Devshards)
			return false
		}
		for _, runtime := range status.Devshards {
			if runtime.ActiveRequests != 0 || runtime.ReservedTokens != 0 || runtime.PendingRaceCleanup != 0 {
				detail = fmt.Sprintf("escrow %s still busy: %+v", runtime.ID, runtime)
				return false
			}
		}
		detail = fmt.Sprintf("escrows still moving: %+v", status.Devshards)
		return slices.Equal(previous.Devshards, status.Devshards)
	})
	require.True(t, settled, "gateway did not settle into %d idle escrows: %s", expectedEscrows, detail)
	return status
}

func requireEscrowUntouched(t *testing.T, before, after fundingRuntimeStatus) {
	t.Helper()
	require.True(t, after.Active, "escrow %s was retired for refusing one oversized request", after.ID)
	require.Equal(t, before.Nonce, after.Nonce, "escrow %s consumed a nonce for a refused reservation", after.ID)
	require.Equal(t, before.Balance, after.Balance, "escrow %s consumed balance for a refused reservation", after.ID)
}

// Only the log proves the escrow was asked: every other post-condition also holds for one never offered the request.
func requireEscrowRefusedFunding(t *testing.T, env fundingFallbackEnv, escrowID string) {
	t.Helper()
	logs, err := env.stack.ComposeLogsTail(400, "devshardctl")
	require.NoError(t, err)
	require.Contains(t, logs, "stage=gateway_escrow_refused_funding escrow="+escrowID,
		"the gateway never offered the oversized request to escrow %s", escrowID)
}

func requireFundingRuntime(t *testing.T, status fundingFallbackStatus, escrowID string) fundingRuntimeStatus {
	t.Helper()
	for _, runtime := range status.Devshards {
		if runtime.ID == escrowID {
			return runtime
		}
	}
	t.Fatalf("escrow %s missing from gateway status: %+v", escrowID, status)
	return fundingRuntimeStatus{}
}
