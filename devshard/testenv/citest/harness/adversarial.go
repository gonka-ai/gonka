package harness

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"common/chain"
	"devshard/signing"
	"devshard/testenv/config"
	"devshard/testenv/mockchain/adminface"
	"devshard/testenv/mockopenai"
	"devshard/transport"

	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// BootAdversarialStack boots the 2× versiond S1 stack and waits for gateway chat readiness.
func BootAdversarialStack(t *testing.T, prefix string) (*Stack, *config.File, Endpoints) {
	t.Helper()
	stack, cfg, eps := BootS1Stack(t, prefix)
	client := GatewayChatClient()
	WaitS1Healthy(t, stack, eps)
	WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)
	WaitGETOK(t, client, eps.RouterHTTP+"/"+cfg.Versiond.VersionName+"/healthz", 5*time.Minute, "devshardd health via router", stack)
	return stack, cfg, eps
}

// MockOpenAIFromConfig returns the host-published mock-openai base URL.
func MockOpenAIFromConfig(cfg *config.File) string {
	return fmt.Sprintf("http://127.0.0.1:%d", cfg.MockOpenAI.HTTPPort)
}

// PatchMockOpenAIFault posts runtime fault knobs to mock-openai /testenv/fault.
func PatchMockOpenAIFault(t *testing.T, client *http.Client, mockOpenAIURL string, patch mockopenai.FaultPatch) {
	t.Helper()
	if client == nil {
		client = HTTPClient()
	}
	data, err := json.Marshal(patch)
	require.NoError(t, err)
	resp, err := client.Post(mockOpenAIURL+"/testenv/fault", "application/json", bytes.NewReader(data))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "POST mock-openai /testenv/fault")
}

// ResetMockOpenAIFault clears mock-openai fault injection knobs.
func ResetMockOpenAIFault(t *testing.T, client *http.Client, mockOpenAIURL string) {
	t.Helper()
	zero := 0
	f := false
	PatchMockOpenAIFault(t, client, mockOpenAIURL, mockopenai.FaultPatch{
		LatencyMs:        &zero,
		HTTPStatus:       &zero,
		DropFirstChunk:   &f,
		PartialStream:    &f,
		StreamChunkDelay: &zero,
	})
}

// PatchTestenvGrantees replaces warm-key grantees for a validator via mock-dapi.
func PatchTestenvGrantees(t *testing.T, client *http.Client, mockDapiHTTP string, req adminface.GranteesRequest) {
	t.Helper()
	if client == nil {
		client = HTTPClient()
	}
	data, err := json.Marshal(req)
	require.NoError(t, err)
	resp, err := client.Post(mockDapiHTTP+"/testenv/grantees", "application/json", bytes.NewReader(data))
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode, "POST /testenv/grantees: %s", string(body))
}

// RequireWarmKeyRevoked asserts the warm grantee is no longer authorized for a validator granter.
func RequireWarmKeyRevoked(t *testing.T, cfg *config.File, granter, warmAddress string) {
	t.Helper()
	conn := dialMockChainGRPC(t, MockChainGRPCFromConfig(cfg))
	c := chain.NewFromConn(conn)
	resp, err := c.InferenceQueryClient().GranteesByMessageType(context.Background(), &inferencetypes.QueryGranteesByMessageTypeRequest{
		GranterAddress: granter,
		MessageTypeUrl: "/inference.inference.MsgStartInference",
	})
	require.NoError(t, err)
	for _, g := range resp.GetGrantees() {
		require.NotEqual(t, warmAddress, g.GetAddress(), "warm key still authorized for %s", granter)
	}
}

// PostWarmKeySignedTransport posts a devshard transport request signed by a warm grantee key
// through versiond-router. Returns the HTTP status (body is discarded).
func PostWarmKeySignedTransport(t *testing.T, client *http.Client, routerHTTP, version, escrowID, pathSuffix, warmPrivateKeyHex string, body []byte) int {
	t.Helper()
	if client == nil {
		client = HTTPClient()
	}
	signer, err := signing.SignerFromHex(warmPrivateKeyHex)
	require.NoError(t, err)

	ts := time.Now().Unix()
	sig, err := transport.SignRequest(signer, escrowID, body, ts)
	require.NoError(t, err)

	url := RouterSessionURL(routerHTTP, version, escrowID, pathSuffix)
	httpReq, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	require.NoError(t, err)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set(transport.HeaderSignature, hex.EncodeToString(sig))
	httpReq.Header.Set(transport.HeaderTimestamp, fmt.Sprintf("%d", ts))

	resp, err := client.Do(httpReq)
	require.NoError(t, err)
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// RequireWarmKeyTransportRejected asserts a warm-key signed devshard transport call is
// rejected after grantee revocation (devshardd bridge + router auth path).
func RequireWarmKeyTransportRejected(t *testing.T, client *http.Client, cfg *config.File, eps Endpoints, warmPrivateKeyHex string) {
	t.Helper()
	escrowID := GetGatewayEscrowID(t, client, eps.GatewayHTTP)
	// Minimal gossip/nonce body: auth runs before handler validation.
	body := []byte(`{"nonce":1,"slot_id":0,"state_hash":"","state_sig":"00"}`)
	status := PostWarmKeySignedTransport(
		t, client, eps.RouterHTTP, cfg.Versiond.VersionName, escrowID, "/gossip/nonce", warmPrivateKeyHex, body,
	)
	require.Equal(t, http.StatusForbidden, status,
		"warm-key transport should be forbidden after grantee revocation (got HTTP %d)", status)
}

// DialMockChainGRPC dials the mock-chain inference gRPC port from a citest config.
func DialMockChainGRPC(t *testing.T, cfg *config.File) *grpc.ClientConn {
	t.Helper()
	return dialMockChainGRPC(t, MockChainGRPCFromConfig(cfg))
}

func dialMockChainGRPC(t *testing.T, addr string) *grpc.ClientConn {
	t.Helper()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// RequireEscrowSettledOnChain queries mock-chain gRPC and requires the escrow is marked settled.
func RequireEscrowSettledOnChain(t *testing.T, cfg *config.File, id uint64) {
	t.Helper()
	conn := dialMockChainGRPC(t, MockChainGRPCFromConfig(cfg))
	c := chain.NewFromConn(conn)
	resp, err := c.InferenceQueryClient().DevshardEscrow(context.Background(), &inferencetypes.QueryGetDevshardEscrowRequest{Id: id})
	require.NoError(t, err)
	require.True(t, resp.GetFound(), "escrow %d not found on mock-chain", id)
	require.True(t, resp.GetEscrow().GetSettled(), "escrow %d not settled on mock-chain", id)
}

// PatchTestenvEscrowSettle marks an escrow settled on mock-chain (via mock-dapi proxy).
func PatchTestenvEscrowSettle(t *testing.T, client *http.Client, mockDapiHTTP string, id uint64) {
	t.Helper()
	if client == nil {
		client = HTTPClient()
	}
	data, err := json.Marshal(adminface.EscrowRequest{ID: &id, Settle: true})
	require.NoError(t, err)
	resp, err := client.Post(mockDapiHTTP+"/testenv/escrow", "application/json", bytes.NewReader(data))
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode, "POST /testenv/escrow: %s", string(body))
}

// GetGatewayEscrowID reads the active escrow id from gateway /v1/status.
func GetGatewayEscrowID(t *testing.T, client *http.Client, gatewayURL string) string {
	t.Helper()
	if client == nil {
		client = HTTPClient()
	}
	var status map[string]any
	require.NoError(t, GetJSON(client, gatewayURL+"/v1/status", &status))
	id, ok := status["escrow_id"].(string)
	if !ok || id == "" {
		if n, ok := status["escrow_id"].(float64); ok && n > 0 {
			return fmt.Sprintf("%.0f", n)
		}
		t.Fatalf("gateway /v1/status missing escrow_id: %v", status)
	}
	return id
}

// PatchAdversarialFastTimeouts lowers refusal/execution timeouts so gateway adversarial paths fail quickly.
func PatchAdversarialFastTimeouts(t *testing.T, client *http.Client, mockDapiHTTP string) {
	t.Helper()
	refusal := int64(5)
	execution := int64(8)
	PatchTestenvParams(t, client, mockDapiHTTP, adminface.ParamsRequest{
		RefusalTimeout:   &refusal,
		ExecutionTimeout: &execution,
	})
	time.Sleep(8 * time.Second)
}

// PostGatewayChatStreamResult posts stream chat and returns status, assembled content, and whether [DONE] was seen.
func PostGatewayChatStreamResult(t *testing.T, client *http.Client, gatewayURL, adminAPIKey string, req ChatCompletionRequest) (status int, content string, sawDone bool) {
	t.Helper()
	if client == nil {
		client = GatewayChatClient()
	}
	req.Stream = true
	data, err := json.Marshal(req)
	require.NoError(t, err)

	httpReq, err := http.NewRequest(http.MethodPost, gatewayURL+"/v1/chat/completions", bytes.NewReader(data))
	require.NoError(t, err)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if adminAPIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+adminAPIKey)
	}

	resp, err := client.Do(httpReq)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	status = resp.StatusCode
	if status >= 300 {
		return status, string(body), false
	}

	var assembled strings.Builder
	scanner := bufio.NewScanner(bytes.NewReader(body))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "data: [DONE]" {
			sawDone = true
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		var chunk map[string]any
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		choices, _ := chunk["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		choice, _ := choices[0].(map[string]any)
		delta, _ := choice["delta"].(map[string]any)
		if delta == nil {
			continue
		}
		if s, ok := delta["content"].(string); ok {
			assembled.WriteString(s)
		}
	}
	require.NoError(t, scanner.Err())
	return status, assembled.String(), sawDone
}

func PostGatewayChatExpectStatus(t *testing.T, client *http.Client, gatewayURL, adminAPIKey string, req ChatCompletionRequest, wantStatus int) {
	t.Helper()
	if client == nil {
		client = GatewayChatClient()
	}
	data, err := json.Marshal(req)
	require.NoError(t, err)
	httpReq, err := http.NewRequest(http.MethodPost, gatewayURL+"/v1/chat/completions", bytes.NewReader(data))
	require.NoError(t, err)
	httpReq.Header.Set("Content-Type", "application/json")
	if adminAPIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+adminAPIKey)
	}
	resp, err := client.Do(httpReq)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, wantStatus, resp.StatusCode, "POST /v1/chat/completions: %s", string(body))
}

func postGatewayChatHTTPStatus(client *http.Client, gatewayURL, adminAPIKey string, req ChatCompletionRequest) (status int, transportErr error, body string) {
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	data, err := json.Marshal(req)
	if err != nil {
		return 0, err, ""
	}
	httpReq, err := http.NewRequest(http.MethodPost, gatewayURL+"/v1/chat/completions", bytes.NewReader(data))
	if err != nil {
		return 0, err, ""
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if adminAPIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+adminAPIKey)
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return 0, err, ""
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, nil, string(raw)
}

// PostGatewayChatExpectFailure posts non-stream chat and requires HTTP status >= 400 or transport timeout.
func PostGatewayChatExpectFailure(t *testing.T, client *http.Client, gatewayURL, adminAPIKey string, req ChatCompletionRequest) int {
	t.Helper()
	status, transportErr, body := postGatewayChatHTTPStatus(client, gatewayURL, adminAPIKey, req)
	if transportErr != nil {
		t.Logf("citest: gateway chat failed with transport error: %v", transportErr)
		return 0
	}
	if status >= 400 {
		return status
	}
	t.Fatalf("expected gateway error, got %d: %s", status, body)
	return status
}

// WaitGatewayChatExpectFailure polls until gateway chat returns HTTP >= 400 or a transport error.
func WaitGatewayChatExpectFailure(t *testing.T, client *http.Client, gatewayURL, adminAPIKey string, req ChatCompletionRequest, wait time.Duration) int {
	t.Helper()
	if client == nil {
		client = GatewayChatClient()
	}
	var status int
	var lastBody string
	ok := AssertEventually(t, wait, 2*time.Second, func() bool {
		var transportErr error
		status, transportErr, lastBody = postGatewayChatHTTPStatus(client, gatewayURL, adminAPIKey, req)
		if transportErr != nil {
			lastBody = transportErr.Error()
			return true
		}
		return status >= 400
	})
	require.True(t, ok, "gateway chat did not fail within %s (last status=%d body=%s)", wait, status, lastBody)
	if status == 0 && lastBody != "" {
		t.Logf("citest: gateway chat failed with transport error: %s", lastBody)
		return 0
	}
	require.NotEqual(t, http.StatusOK, status, "gateway chat should not succeed on stale settled escrow: %s", lastBody)
	return status
}
