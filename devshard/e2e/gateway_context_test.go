package e2e

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"gopkg.in/yaml.v3"

	"devshard/e2e/testutil"
	testenvconfig "devshard/testenv/config"
)

const (
	stubHubHost         = "huggingface.co"
	stubTokenizerRepo   = "e2e-org/stub-tokenizer"
	stubTokenizerCommit = "0123456789abcdef0123456789abcdef01234567"
	stubContextLength   = 4096
)

func pinnedTokenizerEnvOptions(t *testing.T) e2eEnvOptions {
	t.Helper()
	certPEM, keyPEM := stubHubCertificate(t)
	return e2eEnvOptions{
		mockChainAliases: []string{stubHubHost},
		mockChainEnv: map[string]string{
			"MOCK_CHAIN_CONFIG":   "/app/mock-chain-context-config.yaml",
			"MOCK_CHAIN_HUB_ADDR": ":443",
			"MOCK_CHAIN_HUB_DIR":  "/hub",
			"MOCK_CHAIN_HUB_CERT": "/hub-tls/cert.pem",
			"MOCK_CHAIN_HUB_KEY":  "/hub-tls/key.pem",
		},
		mockChainFiles: []testcontainers.ContainerFile{
			{Reader: bytes.NewReader(pinnedMockChainConfig(t)), ContainerFilePath: "/app/mock-chain-context-config.yaml", FileMode: 0o644},
			{Reader: bytes.NewReader(certPEM), ContainerFilePath: "/hub-tls/cert.pem", FileMode: 0o644},
			{Reader: bytes.NewReader(keyPEM), ContainerFilePath: "/hub-tls/key.pem", FileMode: 0o600},
			{
				HostFilePath:      "testdata/stub-tokenizer.json",
				ContainerFilePath: path.Join("/hub", stubTokenizerRepo, "resolve", stubTokenizerCommit, "tokenizer.json"),
				FileMode:          0o644,
			},
		},
		gatewayEnvOverrides: map[string]string{"SSL_CERT_FILE": "/hub-tls/cert.pem"},
		gatewayFiles: []testcontainers.ContainerFile{
			{Reader: bytes.NewReader(certPEM), ContainerFilePath: "/hub-tls/cert.pem", FileMode: 0o644},
		},
	}
}

func pinnedMockChainConfig(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("mock-chain-config.yaml")
	require.NoError(t, err)
	var file testenvconfig.File
	require.NoError(t, yaml.Unmarshal(raw, &file))
	pinned := false
	for i := range file.EpochGroups {
		if file.EpochGroups[i].ModelID == defaultStandModel {
			file.EpochGroups[i].HfRepo = stubTokenizerRepo
			file.EpochGroups[i].HfCommit = stubTokenizerCommit
			pinned = true
		}
	}
	require.True(t, pinned, "mock-chain-config.yaml has no epoch group for %s", defaultStandModel)
	out, err := yaml.Marshal(&file)
	require.NoError(t, err)
	return out
}

func stubHubCertificate(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: stubHubHost},
		DNSNames:              []string{stubHubHost},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}

func awaitTokenizerLoaded(t *testing.T, env *e2eEnv, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		logged := gatewayLogLines(t, env, "tokenizer")
		for _, line := range logged {
			if strings.Contains(line, "prompt tokenizer loaded") {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the gateway did not load the pinned tokenizer within %s; its tokenizer log lines: %q", within, logged)
		}
		time.Sleep(time.Second)
	}
}

func gatewayLogLines(t *testing.T, env *e2eEnv, substring string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	logs, err := env.gateway.Logs(ctx)
	require.NoError(t, err)
	defer func() { _ = logs.Close() }()
	body, err := io.ReadAll(logs)
	require.NoError(t, err)
	var matched []string
	for _, line := range strings.Split(string(body), "\n") {
		if strings.Contains(line, substring) {
			matched = append(matched, line)
		}
	}
	return matched
}

func contextCheckBody(content string, maxTokens int) map[string]any {
	body := testutil.ChatCompletionBody(content, false)
	body["max_tokens"] = maxTokens
	return body
}

// Test flow:
//  1. Start the three-host gateway environment with the mock chain pinning the stand model to a tokenizer a stub hub serves as huggingface.co.
//  2. Give the model a context length through the settings route.
//  3. Send a request that fits, so the gateway starts loading the pinned tokenizer, and assert it is served.
//  4. Wait for the gateway to log that the tokenizer loaded.
//  5. Read the session nonce and the ledger's assigned total.
//  6. Send a request whose max_tokens alone fills the context and assert a 400 context_length_exceeded in OpenAI shape.
//  7. Assert the session nonce and the ledger's assigned total did not move.
//  8. Send a request that fits and assert it is served on a new nonce.
func TestE2E_GatewayRefusesAPromptOverTheContextBeforeANonce(t *testing.T) {
	env, client := startGatewayEnv(t, pinnedTokenizerEnvOptions(t))

	putSettings(t, client, env.clientURL, map[string]any{"model_limits": map[string]any{
		defaultStandModel: map[string]any{
			"default_max_tokens": 256,
			"max_tokens_cap":     stubContextLength,
			"max_model_len":      stubContextLength,
		},
	}})

	completions := env.clientURL + "/v1/chat/completions"
	testutil.RequireOpenAINonStreamingCompletion(t,
		testutil.PostJSONRaw(t, client, completions, contextCheckBody("starts the tokenizer load", 16), testutil.AdminAPIKey))
	awaitTokenizerLoaded(t, env, time.Minute)

	nonceBefore := gatewaySessionNonce(t, client, env.clientURL)
	assignedBefore := testutil.AccountingAssignedTotal(gatewayLedger(t, client, env.statsURL))

	refused := testutil.PostJSONRaw(t, client, completions,
		contextCheckBody("this prompt and the max_tokens it asks for do not fit the context", stubContextLength), testutil.AdminAPIKey)
	if refused.StatusCode != http.StatusBadRequest {
		t.Fatalf("a request over the context was answered %d %s, want 400", refused.StatusCode, refused.Body)
	}
	var payload struct {
		Error struct {
			Code    string `json:"code"`
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal([]byte(refused.Body), &payload), "refusal is not JSON: %s", refused.Body)
	if payload.Error.Code != "context_length_exceeded" || payload.Error.Type != "invalid_request_error" {
		t.Errorf("refusal carries code %q type %q, want context_length_exceeded invalid_request_error: %s",
			payload.Error.Code, payload.Error.Type, refused.Body)
	}
	if !strings.Contains(payload.Error.Message, "maximum context length is 4096 tokens") {
		t.Errorf("refusal does not name the context length: %s", payload.Error.Message)
	}

	if nonceAfter := gatewaySessionNonce(t, client, env.clientURL); nonceAfter != nonceBefore {
		t.Errorf("session nonce moved %d -> %d on a request refused for its context", nonceBefore, nonceAfter)
	}
	if assignedAfter := testutil.AccountingAssignedTotal(gatewayLedger(t, client, env.statsURL)); assignedAfter != assignedBefore {
		t.Errorf("ledger assigned total moved %d -> %d on a request refused for its context", assignedBefore, assignedAfter)
	}

	testutil.RequireOpenAINonStreamingCompletion(t,
		testutil.PostJSONRaw(t, client, completions, contextCheckBody("fits the context", 16), testutil.AdminAPIKey))
	if nonceServed := gatewaySessionNonce(t, client, env.clientURL); nonceServed <= nonceBefore {
		t.Errorf("session nonce stayed at %d after a request that fits was served", nonceServed)
	}
}
