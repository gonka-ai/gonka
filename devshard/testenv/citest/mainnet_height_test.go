//go:build testenvci

package citest

import (
	"os"
	"strings"
	"testing"
	"time"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"
	"devshard/types"
	"encoding/hex"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// TestMainnetHeightProofV7Stand exercises chain-pinned bootstrap, signed diff
// production, real Connect delivery and persisted recovery in separate processes.
func TestMainnetHeightProofV7Stand(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	stack := harness.NewStack(t, "citest-mainnet-height-v7-*")
	harness.RequireLinuxDevshardd(t, stack.TestenvDir)
	harness.WriteMultiConfig(t, stack.WorkDir, harness.MultiConfigOpts{Hosts: 3, EscrowSlots: 2, MaxNonce: 2000, EscrowAmount: 10_000_000})
	body, err := os.ReadFile(stack.ConfigPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(stack.ConfigPath, []byte(strings.Replace(string(body), "version_name: v2", "version_name: v7", 1)), 0o644))
	stack.RunGencompose(t)
	cfg := stack.LoadConfig(t)
	harness.PatchComposeInsertEnvAfter(t, stack.ComposePath, "MOCK_DAPI_BINARY_DIR",
		`MOCK_DAPI_VERSION_NAME: "v7"`,
		`MOCK_DAPI_VERSION_BINARY: "file:///opt/devshard/devshardd"`,
		`MOCK_DAPI_VERSION_SHA256: "`+strings.Repeat("0", 64)+`"`)
	stack.UpBuild(t)
	eps := stack.Endpoints(t, cfg)
	client := harness.HTTPClient()
	chatClient := harness.GatewayChatClient()
	harness.WaitStackHealthy(t, stack, eps)
	harness.WaitGatewayChatReady(t, chatClient, eps.GatewayHTTP, 3*time.Minute, stack)
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "versiond-0", "versiond-1", "versiond-2", "mock-chain", "mock-dapi")
		}
	})
	chat := func(text string) {
		harness.PostGatewayChatCompletion(t, chatClient, eps.GatewayHTTP, harness.TestenvAdminAPIKey, harness.ChatCompletionRequest{Model: config.PrimaryModelID(cfg), Messages: []harness.ChatMessage{{Role: "user", Content: text}}, MaxTokens: 16})
	}
	chat("authenticate the first v7 mainnet checkpoint")
	initial := harness.GetGatewaySessionSnapshot(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey)
	require.NotZero(t, initial.LatestNonce)
	// The durable journal, unlike a height-sync envelope, survives restart.
	raw := stack.ComposeExec(t, "devshard-postgres", "env", "PGPASSWORD="+cfg.Postgres.Password, "psql", "-U", cfg.Postgres.User, "-d", cfg.Postgres.Database, "-At", "-c",
		"SELECT encode(txs_proto, 'hex') FROM devshard_diffs WHERE escrow_id = '"+initial.EscrowID+"' ORDER BY nonce LIMIT 1")
	encoded, err := hex.DecodeString(strings.TrimSpace(raw))
	require.NoError(t, err)
	var content types.DiffContent
	require.NoError(t, proto.Unmarshal(encoded, &content))
	require.NotEmpty(t, content.Txs)
	checkpoint := content.Txs[0].GetMainnetHeight()
	require.NotNil(t, checkpoint)
	require.Positive(t, checkpoint.Height)
	require.NotEmpty(t, checkpoint.LightBlocks)
	harness.RestartService(t, stack, "devshardctl")
	eps = stack.Endpoints(t, cfg)
	harness.WaitGatewayChatReady(t, chatClient, eps.GatewayHTTP, 3*time.Minute, stack)
	chat("replay the authenticated v7 checkpoint after restart")
	final := harness.GetGatewaySessionSnapshot(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey)
	require.Equal(t, initial.EscrowID, final.EscrowID)
	require.Greater(t, final.LatestNonce, initial.LatestNonce)
}
