package config_test

import (
	"testing"

	"devshard/testenv/config"

	"github.com/stretchr/testify/require"
)

func TestApplyDefaults(t *testing.T) {
	cfg := &config.File{}
	cfg.ApplyDefaults()
	require.Equal(t, config.DefaultChainID, cfg.ChainID)
	require.Equal(t, config.DefaultMockChainGRPCPort, cfg.MockChain.GRPCPort)
	require.Equal(t, config.DefaultEscrowSlotURL, cfg.Escrow.SlotURL)
	require.Equal(t, "http://versiond-router:8080", cfg.Escrow.SlotURL)
	require.Equal(t, 8080, config.DefaultProxyPort)
	require.Equal(t, "http://proxy:8080", config.ProxyOverlayURL)
	require.Equal(t, "proxy", config.DefaultProxyService)
}

func TestValidate_RequiresFilledKeys(t *testing.T) {
	cfg := &config.File{
		Hosts: []config.HostCfg{{ID: "h0"}},
	}
	cfg.ApplyDefaults()
	require.Error(t, cfg.Validate())
}

func TestValidate_VersiondModePostgresRules(t *testing.T) {
	cfg := &config.File{
		Versiond: config.VersiondCfg{Mode: config.VersiondModeMulti},
		Hosts: []config.HostCfg{
			{ID: "versiond-0", Address: "gonka1a", PrivateKeyHex: "aa"},
			{ID: "versiond-1", Address: "gonka1b", PrivateKeyHex: "bb"},
		},
		User:     config.UserCfg{Address: "gonka1u", PrivateKeyHex: "cc"},
		Postgres: config.PostgresCfg{Enabled: false},
	}
	cfg.ApplyDefaults()
	require.ErrorContains(t, cfg.Validate(), "postgres.enabled")

	cfg.Postgres.Enabled = true
	require.NoError(t, cfg.Validate())

	cfg.Versiond.Mode = config.VersiondModeSingle
	cfg.Hosts = cfg.Hosts[:1]
	cfg.Postgres.Enabled = true
	require.ErrorContains(t, cfg.Validate(), "mode single")
}

func TestValidate_SlotsCountOnChainIdentitiesNotContainers(t *testing.T) {
	cfg := &config.File{
		Versiond: config.VersiondCfg{Mode: config.VersiondModeMulti},
		Escrow:   config.EscrowMeta{Slots: 3},
		Hosts: []config.HostCfg{
			{ID: "versiond-0", Address: "gonka1ha", PrivateKeyHex: "aa"},
			{ID: "versiond-1", Address: "gonka1replica", PrivateKeyHex: "bb"},
			{ID: "versiond-2", Address: "gonka1solo-a", PrivateKeyHex: "cc"},
			{ID: "versiond-3", Address: "gonka1solo-b", PrivateKeyHex: "dd"},
		},
		User:     config.UserCfg{Address: "gonka1u", PrivateKeyHex: "ee"},
		Postgres: config.PostgresCfg{Enabled: true},
	}
	cfg.ApplyDefaults()
	require.NoError(t, cfg.Validate(), "4 containers / 3 identities / 3 slots is valid")

	cfg.Escrow.Slots = 2
	require.ErrorContains(t, cfg.Validate(), "on-chain host identities")
}

func TestPostgresServices(t *testing.T) {
	cfg := &config.File{
		Versiond: config.VersiondCfg{Mode: config.VersiondModeMulti},
		Hosts: []config.HostCfg{
			{ID: "versiond-0"},
			{ID: "versiond-1"},
			{ID: "versiond-2"},
		},
		Postgres: config.PostgresCfg{Enabled: true},
	}
	cfg.ApplyDefaults()
	require.Equal(t, []string{"devshard-postgres"}, config.PostgresServices(cfg))
	require.Equal(t, "devshard-postgres", config.PostgresServiceFor(cfg, cfg.Hosts[2]))

	cfg.Postgres.PerParticipant = true
	require.Equal(t, []string{"devshard-postgres-versiond-0", "devshard-postgres-versiond-2"}, config.PostgresServices(cfg))
	require.Equal(t, "devshard-postgres-versiond-0", config.PostgresServiceFor(cfg, cfg.Hosts[1]))
	require.Equal(t, "devshard-postgres-versiond-2", config.PostgresServiceFor(cfg, cfg.Hosts[2]))

	cfg.Postgres.Enabled = false
	require.Empty(t, config.PostgresServices(cfg))
}

func TestValidate_MockNodeOracleKnobs(t *testing.T) {
	base := func() *config.File {
		cfg := &config.File{
			Versiond: config.VersiondCfg{Mode: config.VersiondModeMulti},
			Hosts:    []config.HostCfg{{ID: "versiond-0", Address: "gonka1a", PrivateKeyHex: "aa"}},
			User:     config.UserCfg{Address: "gonka1u", PrivateKeyHex: "cc"},
			Postgres: config.PostgresCfg{Enabled: true},
		}
		cfg.ApplyDefaults()
		cfg.MockOpenAI.Nodes = []config.MockOpenAINodeCfg{{Name: "mock-openai-0", Workers: 1}}
		return cfg
	}
	cfg := base()
	cfg.MockOpenAI.Nodes[0].RulesJSON = `[{"name":"x","seeds":[1],"http_status":503}]`
	require.NoError(t, cfg.Validate())

	for name, tune := range map[string]func(*config.MockOpenAINodeCfg){
		"negative chunk_tokens":     func(n *config.MockOpenAINodeCfg) { n.ChunkTokens = -1 },
		"negative first_content_ms": func(n *config.MockOpenAINodeCfg) { n.FirstContentMs = -1 },
		"negative exec_ms":          func(n *config.MockOpenAINodeCfg) { n.ExecMs = -1 },
		"negative replay_ms":        func(n *config.MockOpenAINodeCfg) { n.ReplayMs = -1 },
		"rules_json not a list":     func(n *config.MockOpenAINodeCfg) { n.RulesJSON = `{"name":"x"}` },
		"rules_json broken":         func(n *config.MockOpenAINodeCfg) { n.RulesJSON = `[{"name":` },
	} {
		cfg := base()
		tune(&cfg.MockOpenAI.Nodes[0])
		require.ErrorContains(t, cfg.Validate(), "mock_openai.nodes[0]", name)
	}
}
