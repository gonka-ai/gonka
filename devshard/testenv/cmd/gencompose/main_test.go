package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"common/chain"
	"devshard/signing"
	"devshard/testenv/config"
	"devshard/testenv/mockchain/grpcface"
	"devshard/testenv/mockchain/seed"

	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestIsPlaceholderKey(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", true},
		{"   ", true},
		{"TODO", true},
		{"CHANGEME", true},
		{"deadbeef", false},
	}
	for _, tc := range cases {
		if got := isPlaceholderKey(tc.in); got != tc.want {
			t.Errorf("isPlaceholderKey(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestGenerateHosts_FillsMissingFields(t *testing.T) {
	seedKey, err := signing.GenerateKey()
	require.NoError(t, err)

	cfg := &config.File{
		Hosts: []config.HostCfg{
			{},
			{ID: "custom-host-1"},
			{PrivateKeyHex: seedKey.PrivateKeyHex()},
		},
	}
	require.NoError(t, generateHosts(cfg))

	require.NotEmpty(t, cfg.Hosts[0].PrivateKeyHex)
	require.NotEmpty(t, cfg.Hosts[0].Address)
	require.Equal(t, "versiond-0", cfg.Hosts[0].ID)
	require.Equal(t, "custom-host-1", cfg.Hosts[1].ID)
	require.Equal(t, seedKey.PrivateKeyHex(), cfg.Hosts[2].PrivateKeyHex)
	require.Equal(t, seedKey.Address(), cfg.Hosts[2].Address)
}

func TestAssignSlots_MultiTwoHostsHAOwnsAllSlots(t *testing.T) {
	cfg := &config.File{
		Versiond: config.VersiondCfg{Mode: config.VersiondModeMulti},
		Escrow:   config.EscrowMeta{Slots: 4},
		Hosts: []config.HostCfg{
			{ID: "versiond-0", Address: "gonka1ha"},
			{ID: "versiond-1", Address: "gonka1replica"},
		},
	}
	assignSlots(cfg)
	require.Equal(t, []int{0, 1, 2, 3}, cfg.Hosts[0].SlotIDs)
	require.Empty(t, cfg.Hosts[1].SlotIDs)

	syncChainSeed(cfg)
	require.Equal(t, []string{"gonka1ha", "gonka1ha", "gonka1ha", "gonka1ha"}, cfg.Escrows[0].Slots)
	require.Len(t, cfg.Participants, 1)
	require.Equal(t, "gonka1ha", cfg.Participants[0].Address)
}

func TestAssignSlots_MultiThreeHostsHAPlusSolo(t *testing.T) {
	cfg := &config.File{
		Versiond:       config.VersiondCfg{Mode: config.VersiondModeMulti},
		Escrow:         config.EscrowMeta{Slots: 4, SlotURL: "http://router:8080"},
		VersiondRouter: config.VersiondRouterCfg{Host: "versiond-router"},
		Hosts: []config.HostCfg{
			{ID: "versiond-0", Address: "gonka1ha"},
			{ID: "versiond-1", Address: "gonka1replica"},
			{ID: "versiond-2", Address: "gonka1solo"},
		},
	}
	assignSlots(cfg)
	require.Equal(t, []int{0, 2}, cfg.Hosts[0].SlotIDs)
	require.Empty(t, cfg.Hosts[1].SlotIDs)
	require.Equal(t, []int{1, 3}, cfg.Hosts[2].SlotIDs)

	syncChainSeed(cfg)
	require.Equal(t, []string{"gonka1ha", "gonka1solo", "gonka1ha", "gonka1solo"}, cfg.Escrows[0].Slots)
	require.Len(t, cfg.Participants, 2)
	require.Equal(t, "gonka1ha", cfg.Participants[0].Address)
	require.Equal(t, "http://router:8080", cfg.Participants[0].InferenceURL)
	require.Equal(t, "gonka1solo", cfg.Participants[1].Address)
	require.Equal(t, "http://versiond-2:8080", cfg.Participants[1].InferenceURL)
	require.Equal(t, "versiond-0 versiond-1", versiondHosts(cfg))
}

func TestVersiondKeyName_MultiHAPairAndSolo(t *testing.T) {
	cfg := &config.File{
		Versiond: config.VersiondCfg{Mode: config.VersiondModeMulti},
		Hosts: []config.HostCfg{
			{ID: "versiond-0"},
			{ID: "versiond-1"},
			{ID: "versiond-2"},
		},
	}
	require.Equal(t, "versiond-0", versiondKeyName(cfg, cfg.Hosts[0]))
	require.Equal(t, "versiond-0", versiondKeyName(cfg, cfg.Hosts[1]))
	require.Equal(t, "versiond-2", versiondKeyName(cfg, cfg.Hosts[2]))

	cfg.Versiond.Mode = config.VersiondModeSingle
	require.Equal(t, "versiond-1", versiondKeyName(cfg, cfg.Hosts[1]))
}

func TestSyncChainSeed_FromHosts(t *testing.T) {
	hostKey, err := signing.GenerateKey()
	require.NoError(t, err)
	userKey, err := signing.GenerateKey()
	require.NoError(t, err)
	warmKey, err := signing.GenerateKey()
	require.NoError(t, err)

	cfg := &config.File{
		ChainID: "gonka-test",
		Escrow:  config.EscrowMeta{Slots: 2, SlotURL: "http://router:8080"},
		Hosts: []config.HostCfg{{
			ID:            "versiond-0",
			PrivateKeyHex: hostKey.PrivateKeyHex(),
			Address:       hostKey.Address(),
			URL:           "http://versiond-0:8080",
		}},
		User: config.UserCfg{
			PrivateKeyHex: userKey.PrivateKeyHex(),
			Address:       userKey.Address(),
		},
		WarmGrantee: config.WarmGranteeCfg{
			PrivateKeyHex: warmKey.PrivateKeyHex(),
			Address:       warmKey.Address(),
		},
		Escrows: []config.Escrow{{ID: 1}},
	}
	cfg.ApplyDefaults()
	syncChainSeed(cfg)

	require.Len(t, cfg.Participants, 1)
	require.Equal(t, hostKey.Address(), cfg.Participants[0].Address)
	require.Equal(t, userKey.Address(), cfg.Escrows[0].Creator)
	require.Len(t, cfg.Escrows[0].Slots, 2)
	require.Equal(t, hostKey.Address(), cfg.Escrows[0].Slots[0])
	require.Equal(t, hostKey.Address(), cfg.Escrows[0].Slots[1])
	require.Equal(t, "http://router:8080", cfg.Participants[0].InferenceURL)
	require.Equal(t, hostKey.Address(), cfg.Grantees[0].GranterAddress)
	require.Equal(t, []string{warmKey.Address()}, cfg.Grantees[0].Grantees)
}

func TestFillConfig_SeedMockChainGRPCQueries(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	skeleton := strings.TrimPrefix(`chain_id: gonka-roundtrip
block_height: 200
epoch:
  index: 2
  poc_start_block_height: 150
params:
  devshard_requests_enabled: true
mock_chain:
  grpc_port: 9090
escrow:
  slots: 2
  slot_url: http://versiond-router:8080/devshard/v1
hosts:
  - id: versiond-0
    private_key_hex: TODO
  - id: versiond-1
    private_key_hex: TODO
user:
  private_key_hex: TODO
warm_grantee:
  private_key_hex: TODO
escrows:
  - id: 1
    model_id: test-model
grantees:
  - granter_address: ""
    message_type_url: /inference.inference.MsgStartInference
    grantees: [""]
`, "\n")
	require.NoError(t, os.WriteFile(cfgPath, []byte(skeleton), 0o644))

	cfg, err := config.Load(cfgPath)
	require.NoError(t, err)
	require.NoError(t, fillConfig(cfg))
	require.NoError(t, cfg.Validate())

	seedPath := filepath.Join(dir, "seed.yaml")
	require.NoError(t, cfg.Save(seedPath))

	st, err := seed.Load(seedPath)
	require.NoError(t, err)
	require.Equal(t, "gonka-roundtrip", st.GetChainID())
	require.Equal(t, cfg.User.Address, st.Escrows[1].Creator)

	srv, lis, err := grpcface.NewInProcessServer(grpcface.Deps{Store: st})
	require.NoError(t, err)
	t.Cleanup(func() {
		srv.Stop()
		_ = lis.Close()
	})
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	client := chain.NewFromConn(conn)
	ctx := context.Background()

	// Two-host multi: only the HA participant is on-chain (hosts[1] is a replica).
	resp, err := client.InferenceQueryClient().Participant(ctx, &inferencetypes.QueryGetParticipantRequest{Index: cfg.Hosts[0].Address})
	require.NoError(t, err)
	require.Equal(t, cfg.Hosts[0].Address, resp.Participant.Address)

	escrowResp, err := client.InferenceQueryClient().DevshardEscrow(ctx, &inferencetypes.QueryGetDevshardEscrowRequest{Id: 1})
	require.NoError(t, err)
	require.True(t, escrowResp.Found)
	require.Equal(t, cfg.User.Address, escrowResp.Escrow.Creator)
	require.Len(t, escrowResp.Escrow.Slots, 2)
	// Two-host multi: every slot belongs to the HA participant.
	require.Equal(t, cfg.Hosts[0].Address, escrowResp.Escrow.Slots[0])
	require.Equal(t, cfg.Hosts[0].Address, escrowResp.Escrow.Slots[1])
	require.Equal(t, config.VersiondModeMulti, cfg.Versiond.Mode)

	grantResp, err := client.InferenceQueryClient().GranteesByMessageType(ctx, &inferencetypes.QueryGranteesByMessageTypeRequest{
		GranterAddress: cfg.Hosts[0].Address,
		MessageTypeUrl: "/inference.inference.MsgStartInference",
	})
	require.NoError(t, err)
	require.Len(t, grantResp.Grantees, 1)
	require.Equal(t, cfg.WarmGrantee.Address, grantResp.Grantees[0].Address)
}

func TestWriteCompose_MockChainService(t *testing.T) {
	dir := t.TempDir()
	cfg := defaultConfig()
	require.NoError(t, fillConfig(cfg))
	require.Equal(t, config.VersiondModeMulti, cfg.Versiond.Mode)
	require.True(t, cfg.Postgres.Enabled)

	outPath := filepath.Join(dir, "docker-compose.yml")
	require.NoError(t, writeCompose(cfg, outPath))

	body, err := os.ReadFile(outPath)
	require.NoError(t, err)
	text := string(body)
	require.Contains(t, text, "mock-chain:")
	require.Contains(t, text, "Dockerfile.mock-chain")
	require.Contains(t, text, "CONFIG_PATH")
	require.Contains(t, text, "mock-dapi:")
	require.Contains(t, text, "mock-openai:")
	require.Contains(t, text, "Dockerfile.mockopenai")
	require.Contains(t, text, "versiond-0:")
	require.Contains(t, text, "versiond-1:")
	require.Contains(t, text, "versiond-2:")
	require.Contains(t, text, "versiond-router:")
	require.Contains(t, text, `VERSIOND_PORT: "8080"`)
	require.Contains(t, text, `VERSIOND_LEGACY_HOST: "versiond-0"`)
	require.Contains(t, text, `VERSIOND_NON_HA_VERSIONS: "v1"`)
	require.NotContains(t, text, "versiond-router-state",
		"the router keeps no durable state: membership is DNS, health is measured")
	require.Contains(t, text, "stop_grace_period: 10s",
		"the stateless router replacement must not wait behind a long SSE stream")
	require.Contains(t, text, "VERSIOND_ORACLE_URL")
	require.Contains(t, text, "VERSIOND_OVERRIDE_v2")
	require.Contains(t, text, "NODE_MANAGER_ADDR")
	require.Contains(t, text, "DEVSHARD_STORAGE_MODE: postgres")
	require.Contains(t, text, "devshard-postgres:")
	require.Contains(t, text, "PGHOST:")
	// HA pair shares KEY_NAME=versiond-0; solo versiond-2 keeps its own key.
	require.Equal(t, 2, strings.Count(text, "KEY_NAME: versiond-0"))
	require.NotContains(t, text, "KEY_NAME: versiond-1")
	require.Contains(t, text, "KEY_NAME: versiond-2")
	require.Contains(t, text, `VERSIOND_POOL_HOST: "versiond-pool"`)
	require.Contains(t, text, `VERSIOND_ROUTING_CATALOG_URL: "http://mock-dapi:9100/versions"`)
	require.Contains(t, text, `VERSIOND_ROUTING_CATALOG_POLL_SECONDS: "1"`)
	require.Equal(t, 2, strings.Count(text, "- versiond-pool"),
		"only the sticky pair should resolve through the router pool")
	require.Equal(t, 2, strings.Count(text, "DEVSHARD_STORAGE_MODE: postgres"))
	require.Contains(t, text, "DEVSHARD_STORAGE_MODE: sqlite")
	require.Contains(t, text, "DEVSHARD_VALIDATION_LEASE_TTL")
	require.Contains(t, text, "DEVSHARD_VALIDATION_RETRY_INTERVAL")
	require.Contains(t, text, "devshardctl:")
	require.Contains(t, text, "DEVSHARD_PRIVATE_KEY")
	require.Contains(t, text, "DEVSHARD_ESCROW_ID")
	require.Contains(t, text, "DEVSHARD_PUBLIC_API")
	require.Contains(t, text, "DEVSHARD_CHAIN_GRPC")
	require.Contains(t, text, "DEVSHARD_NODE_MANAGER_ADDR")
	require.NotContains(t, text, "DEVSHARD_TX_QUERY_REST")
	require.NotContains(t, text, "DEVSHARD_CHAIN_REST:")
	require.NotContains(t, text, "MOCK_CHAIN_REST")
	require.NotContains(t, text, ":1317")
	require.Contains(t, text, "/health")
	require.Contains(t, text, "DEVSHARD_MODEL")
	require.Contains(t, text, "/v1/status")
}

func TestWriteCompose_SingleMode_FilePayloadFallback(t *testing.T) {
	dir := t.TempDir()
	cfg := defaultConfig()
	cfg.Versiond.Mode = config.VersiondModeSingle
	cfg.Hosts = cfg.Hosts[:1]
	require.NoError(t, fillConfig(cfg))
	require.Equal(t, config.VersiondModeSingle, cfg.Versiond.Mode)
	require.False(t, cfg.Postgres.Enabled)
	require.Len(t, cfg.Hosts, 1)

	outPath := filepath.Join(dir, "docker-compose.yml")
	require.NoError(t, writeCompose(cfg, outPath))

	body, err := os.ReadFile(outPath)
	require.NoError(t, err)
	text := string(body)
	require.Contains(t, text, "versiond-0:")
	require.NotContains(t, text, "versiond-1:")
	require.NotContains(t, text, "devshard-postgres:")
	require.NotContains(t, text, "DEVSHARD_STORAGE_MODE")
	require.NotContains(t, text, "PGHOST:")
}

func TestWriteCompose_EnvFile(t *testing.T) {
	dir := t.TempDir()
	cfg := defaultConfig()
	require.NoError(t, fillConfig(cfg))

	outPath := filepath.Join(dir, "docker-compose.yml")
	require.NoError(t, writeCompose(cfg, outPath))
	envPath := filepath.Join(dir, ".env")
	require.NoError(t, writeEnvFile(cfg, envPath))

	body, err := os.ReadFile(envPath)
	require.NoError(t, err)
	text := string(body)
	require.Contains(t, text, "TESTENV_USER_PRIVATE_KEY="+cfg.User.PrivateKeyHex)
	require.Contains(t, text, "TESTENV_CHAIN_ID="+cfg.ChainID)
}

func TestFillConfig_MaterializesKeyrings(t *testing.T) {
	dir := t.TempDir()
	cfg := defaultConfig()
	require.NoError(t, fillConfig(cfg))
	require.NoError(t, cfg.Validate())

	keyringBase := filepath.Join(dir, "keyring")
	require.NoError(t, materializeKeyrings(cfg, keyringBase))

	for _, h := range cfg.Hosts {
		hostDir := filepath.Join(keyringBase, h.ID)
		if _, err := os.Stat(hostDir); err == nil {
			t.Errorf("legacy per-host keyring dir %s should not exist (shared keyring)", hostDir)
		}
	}
	_, err := os.Stat(filepath.Join(keyringBase, "keyhash"))
	require.NoError(t, err)
}
