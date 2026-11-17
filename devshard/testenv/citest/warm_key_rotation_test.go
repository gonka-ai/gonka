//go:build testenvci

package citest

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devshard/host"
	"devshard/signing"
	"devshard/testenv/citest/harness"
	"devshard/testenv/config"
	"devshard/testenv/mockchain/adminface"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/stretchr/testify/require"
)

type rotationBinding struct {
	Address string `json:"address"`
	From    uint64 `json:"from_nonce"`
	Grant   uint64 `json:"grant_id"`
	Revoked uint64 `json:"revoked_at_nonce"`
}

type rotationView struct {
	Session struct {
		ID    string `json:"escrow_id"`
		Nonce uint64 `json:"latest_nonce"`
	} `json:"session"`
	Keys map[string][]rotationBinding `json:"warm_keys"`
}

// TestWarmKeyRotationAcrossHAReplayAndSnapshot runs actual versiond children,
// chain queries, the gateway sequencer, Connect admission, and Postgres recovery.
// The two HA replicas deliberately sign with different live keys during rotation.
func TestWarmKeyRotationAcrossHAReplayAndSnapshot(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)
	stack := harness.NewStack(t, "citest-warm-key-rotation-*")
	harness.RequireLinuxDevshardd(t, stack.TestenvDir)
	harness.WriteMultiConfig(t, stack.WorkDir, harness.MultiConfigOpts{Hosts: 3, EscrowSlots: 2, MaxNonce: 2000, EscrowAmount: 10_000_000, RefusalTimeout: 3, ExecutionTimeout: 15})
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
			logs, err := stack.ComposeLogsTail(5000, "devshardctl", "versiond-0", "versiond-1", "versiond-2", "mock-dapi")
			if err == nil {
				t.Logf("rotation stand logs:\n%s", logs)
			}
		}
	})
	oldKey := cfg.WarmGrantee
	// versiond-1's otherwise unused key is already in the test keyring. That
	// container normally uses versiond-0's cold key as its HA identity.
	newKey := cfg.Hosts[1]
	granter := cfg.Hosts[0].Address
	importRotationKey(t, stack.WorkDir, cfg, "rotation-old", oldKey.PrivateKeyHex)
	coldPrivateKey, err := hex.DecodeString(cfg.Hosts[0].PrivateKeyHex)
	require.NoError(t, err)
	coldPublicKey := base64.StdEncoding.EncodeToString((&secp256k1.PrivKey{Key: coldPrivateKey}).PubKey().Bytes())
	for _, replica := range cfg.Hosts[:2] {
		harness.PatchComposeServiceEnv(t, stack.ComposePath, replica.ID, "ACCOUNT_PUBKEY", coldPublicKey)
	}
	chatNumber := 0
	chat := func() {
		chatNumber++
		harness.PostGatewayChatCompletion(t, chatClient, eps.GatewayHTTP, harness.TestenvAdminAPIKey, harness.ChatCompletionRequest{
			Model: config.PrimaryModelID(cfg), Messages: []harness.ChatMessage{{Role: "user", Content: fmt.Sprintf("warm-key rotation regression %d", chatNumber)}}, MaxTokens: 16,
		})
	}
	chat()
	initial := readRotationView(t, client, eps.GatewayHTTP)
	require.NotEmpty(t, initial.Session.ID)
	require.True(t, rotationKeyLive(initial, oldKey.Address))
	harness.RequireWarmKeyTransportAccepted(t, stack, cfg, eps, oldKey.PrivateKeyHex)

	harness.Step(t, "grant the second key and run HA replicas with overlapping signers")
	harness.PatchTestenvGrantees(t, client, eps.MockDapiHTTP, adminface.GranteesRequest{GranterAddress: granter, Grantees: []string{oldKey.Address, newKey.Address}})
	waitRotationKeys(t, client, eps.GatewayHTTP, chat, oldKey.Address, newKey.Address, true)
	harness.WaitGatewaySessionSettled(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey)
	harness.PatchComposeServiceEnv(t, stack.ComposePath, cfg.Hosts[0].ID, "KEY_NAME", "rotation-old")
	harness.PatchComposeServiceEnv(t, stack.ComposePath, cfg.Hosts[1].ID, "KEY_NAME", cfg.Hosts[1].ID)
	stack.RecreateServices(t, cfg.Hosts[0].ID, cfg.Hosts[1].ID)
	harness.WaitVersiondSessionHealthy(t, stack, cfg, eps, initial.Session.ID)
	for _, replica := range cfg.Hosts[:2] {
		waitVersiondHostSessionMempool(t, stack, replica.ID, initial.Session.ID, 3*time.Minute)
	}
	chat()
	harness.RequireWarmKeyTransportAccepted(t, stack, cfg, eps, oldKey.PrivateKeyHex)
	harness.RequireWarmKeyTransportAccepted(t, stack, cfg, eps, newKey.PrivateKeyHex)
	overlap := readRotationView(t, client, eps.GatewayHTTP)
	require.Equal(t, initial.Session.ID, overlap.Session.ID)
	require.True(t, rotationKeyLive(overlap, oldKey.Address))
	require.True(t, rotationKeyLive(overlap, newKey.Address))

	// Drain messages signed by the old key before revoking its chain grant.
	harness.WaitGatewaySessionSettled(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey)
	harness.Step(t, "leave the old-key replica offline across revocation")
	stack.StopService(t, cfg.Hosts[0].ID)
	harness.PatchTestenvGrantees(t, client, eps.MockDapiHTTP, adminface.GranteesRequest{GranterAddress: granter, Grantees: []string{newKey.Address}})
	waitRotationKeys(t, client, eps.GatewayHTTP, chat, oldKey.Address, newKey.Address, false)
	harness.RequireWarmKeyRevoked(t, eps, granter, oldKey.Address)
	harness.RequireWarmKeyTransportRejected(t, stack, cfg, eps, oldKey.PrivateKeyHex)
	chat()

	harness.Step(t, "persist a live snapshot with both the historical grant and its revoke")
	current := harness.GetGatewaySessionSnapshot(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey)
	target := nextSnapshotNonce(current.LatestNonce)
	advanceGatewaySessionToNonce(t, chatClient, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey, config.PrimaryModelID(cfg), target)
	waitPostgresSnapshotNonce(t, stack, cfg, initial.Session.ID, target, 3*time.Minute)
	raw := stack.ComposeExec(t, "devshard-postgres", "env", "PGPASSWORD="+cfg.Postgres.Password,
		"psql", "-U", cfg.Postgres.User, "-d", cfg.Postgres.Database, "-At", "-c",
		"SELECT encode(state_data, 'hex') FROM devshard_snapshots WHERE escrow_id = '"+initial.Session.ID+"' ORDER BY nonce DESC LIMIT 1")
	encoded, err := hex.DecodeString(strings.TrimSpace(raw))
	require.NoError(t, err)
	snapshot, err := host.UnmarshalStateSnapshot(encoded)
	require.NoError(t, err)
	for slot, bindings := range readRotationView(t, client, eps.GatewayHTTP).Keys {
		var got []rotationBinding
		for key, history := range snapshot.WarmKeyHistory {
			if strings.TrimSpace(slot) != fmt.Sprint(key) {
				continue
			}
			for _, binding := range history {
				got = append(got, rotationBinding{binding.Addr, binding.FromNonce, binding.GrantID, binding.RevokedAtNonce})
			}
		}
		require.Equal(t, bindings, got, "snapshot history for slot %s", slot)
	}

	harness.Step(t, "recover the offline replica with its new key and replay after the snapshot")
	harness.PatchComposeServiceEnv(t, stack.ComposePath, cfg.Hosts[0].ID, "KEY_NAME", cfg.Hosts[1].ID)
	stack.RecreateServices(t, cfg.Hosts[0].ID)
	harness.WaitVersiondSessionHealthy(t, stack, cfg, eps, initial.Session.ID)
	waitVersiondHostSessionMempool(t, stack, cfg.Hosts[0].ID, initial.Session.ID, 3*time.Minute)
	chat()
	harness.RestartService(t, stack, "devshardctl")
	eps = stack.Endpoints(t, cfg)
	harness.WaitGatewayChatReady(t, chatClient, eps.GatewayHTTP, 3*time.Minute, stack)
	chat()
	final := readRotationView(t, client, eps.GatewayHTTP)
	require.Equal(t, initial.Session.ID, final.Session.ID)
	require.False(t, rotationKeyLive(final, oldKey.Address))
	require.True(t, rotationKeyLive(final, newKey.Address))
	logs, err := stack.ComposeLogsTail(5000, cfg.Hosts[0].ID)
	require.NoError(t, err)
	require.Contains(t, logs, "restored devshard snapshot")

	harness.Step(t, "retire the gateway runtime and hydrate its read-only warm-key state")
	harness.PostAdminDeactivateDevshard(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey, initial.Session.ID)
	var readOnly rotationView
	require.Eventually(t, func() bool {
		req, err := http.NewRequest(http.MethodGet, eps.GatewayHTTP+"/devshard/"+initial.Session.ID+"/v1/state", nil)
		if err != nil {
			return false
		}
		req.Header.Set("Authorization", "Bearer "+harness.TestenvAdminAPIKey)
		resp, err := client.Do(req)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == http.StatusOK && resp.Header.Get("X-Devshard-Readonly") == "1" && json.NewDecoder(resp.Body).Decode(&readOnly) == nil
	}, 30*time.Second, 200*time.Millisecond, "inactive warm-key escrow must hydrate through the admin read-only path")
	require.Equal(t, final.Session.ID, readOnly.Session.ID)
	require.Equal(t, final.Keys, readOnly.Keys)
}

func importRotationKey(t *testing.T, dir string, cfg *config.File, name, privateKey string) {
	t.Helper()
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	password := cfg.Versiond.KeyringPassword
	kr, err := keyring.New("inferenced", keyring.BackendFile, filepath.Join(dir, "keyring"), strings.NewReader(strings.Repeat(password+"\n", 32)), codec.NewProtoCodec(registry))
	require.NoError(t, err)
	require.NoError(t, kr.ImportPrivKeyHex(name, privateKey, "secp256k1"))
	_, err = signing.NewSignerFromKeyring(kr, name)
	require.NoError(t, err)
}

func readRotationView(t *testing.T, client *http.Client, url string) rotationView {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url+"/v1/state", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+harness.TestenvAdminAPIKey)
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
	var view rotationView
	require.NoError(t, json.Unmarshal(body, &view))
	return view
}

func rotationKeyLive(view rotationView, address string) bool {
	for _, bindings := range view.Keys {
		for _, b := range bindings {
			if b.Address == address && b.From <= view.Session.Nonce && (b.Revoked == 0 || b.Revoked > view.Session.Nonce) {
				return true
			}
		}
	}
	return false
}

func waitRotationKeys(t *testing.T, client *http.Client, url string, chat func(), oldKey, newKey string, oldLive bool) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		chat()
		view := readRotationView(t, client, url)
		if rotationKeyLive(view, oldKey) == oldLive && rotationKeyLive(view, newKey) {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatal("gateway did not sequence the expected warm-key transition")
}
