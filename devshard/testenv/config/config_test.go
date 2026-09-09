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
		User:   config.UserCfg{Address: "gonka1u", PrivateKeyHex: "cc"},
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
