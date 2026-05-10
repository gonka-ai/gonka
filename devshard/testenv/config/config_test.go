package config_test

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"devshard/testenv/config"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func TestConfig_LoadAppliesDefaults(t *testing.T) {
	path := writeConfig(t, `
hosts:
  - id: h0
escrow:
  slots: 4
`)
	cfg, err := config.Load(path)
	require.NoError(t, err)

	// Defaults kicked in.
	require.Equal(t, config.DefaultChainID, cfg.Chain.ID)
	require.Equal(t, config.DefaultBlockTime, cfg.Chain.BlockTime)
	require.Equal(t, config.DefaultMockChainPort, cfg.MockChain.Port)
	require.Equal(t, config.DefaultHeightSyncBlockIntervalDelta, cfg.HeightSync.BlockIntervalDelta)
	require.Equal(t, config.DefaultEscrowID, cfg.Escrow.ID)
	require.Equal(t, config.DefaultEscrowVersion, cfg.Escrow.Version)
	require.Equal(t, config.DefaultAppHash, cfg.Escrow.AppHash)
	require.Equal(t, 4, cfg.Escrow.Slots)
	require.Equal(t, 1, cfg.Devshard.GroupSize) // defaults to len(Hosts)
	require.Equal(t, config.DefaultHostPort, cfg.Hosts[0].Port)

	require.NoError(t, cfg.Validate())
}

func TestConfig_ValidateRejectsEmptyHosts(t *testing.T) {
	path := writeConfig(t, `
chain: {id: "gonka-testenv-1"}
escrow: {slots: 4}
hosts: []
`)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	require.Error(t, cfg.Validate())
}

func TestConfig_ValidateRejectsTooFewSlots(t *testing.T) {
	path := writeConfig(t, `
hosts:
  - {id: h0}
  - {id: h1}
  - {id: h2}
escrow:
  slots: 2
`)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	require.Error(t, cfg.Validate())
}

func TestConfig_ValidateRejectsSlotsOverLimit(t *testing.T) {
	path := writeConfig(t, `
hosts:
  - {id: h0}
escrow:
  slots: 200
`)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	require.Error(t, cfg.Validate())
}

func TestConfig_EffectiveCreatorAddressPrefersExplicit(t *testing.T) {
	path := writeConfig(t, `
hosts:
  - {id: h0}
escrow:
  slots: 4
  creator_address: "gonka1explicit"
user:
  address: "gonka1operator"
`)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	require.Equal(t, "gonka1explicit", cfg.EffectiveCreatorAddress())
}

func TestConfig_EffectiveCreatorAddressFallsBackToUser(t *testing.T) {
	path := writeConfig(t, `
hosts:
  - {id: h0}
escrow:
  slots: 4
user:
  address: "gonka1operator"
`)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	require.Equal(t, "gonka1operator", cfg.EffectiveCreatorAddress())
}

func TestConfig_SlotsArray(t *testing.T) {
	path := writeConfig(t, `
hosts:
  - id: h0
    address: "gonka1a"
    slot_ids: [0, 2]
  - id: h1
    address: "gonka1b"
    slot_ids: [1, 3]
escrow:
  slots: 4
`)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	require.Equal(t,
		[]string{"gonka1a", "gonka1b", "gonka1a", "gonka1b"},
		cfg.SlotsArray(),
	)
}

func TestConfig_HostURL_DefaultsToIDPort(t *testing.T) {
	path := writeConfig(t, `
hosts:
  - id: h7
    address: "gonka1x"
    port: 9000
escrow:
  slots: 4
`)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	require.Equal(t, "http://h7:9000", cfg.Hosts[0].HostURL())
}

func TestConfig_MustAppHashPanicsOnInvalidHex(t *testing.T) {
	path := writeConfig(t, `
hosts:
  - {id: h0}
escrow:
  slots: 4
  app_hash: "not-hex"
`)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	require.Panics(t, func() { cfg.MustAppHash() })
}

// TestRepoConfigLoads ensures the checked-in devshard/testenv/config.yaml
// parses cleanly and passes validation. Regressions here mean the
// committed example drifted from the schema.
func TestRepoConfigLoads(t *testing.T) {
	// Resolve relative to the testenv tree regardless of where `go test`
	// is invoked from.
	cfg, err := config.Load(filepath.Join("..", "config.yaml"))
	require.NoError(t, err)
	require.NoError(t, cfg.Validate())
	// HeightSync defaults kicked in.
	require.Equal(t, config.DefaultHeightSyncPort, cfg.HeightSync.Port)
	require.EqualValues(t, 1, cfg.HeightSync.InitialHeight)
	require.Equal(t, config.DefaultAnchorPeriodNonces, cfg.HeightSync.AnchorPeriodNonces)
	require.Equal(t, cfg.Devshard.GroupSize, cfg.HeightSync.SyncTurnSlots)
	require.GreaterOrEqual(t, cfg.HeightSync.AnchorPeriodNonces, cfg.HeightSync.SyncTurnSlots)
}

func TestConfig_ValidateRejectsAnchorPeriodLessThanSyncTurnSlots(t *testing.T) {
	path := writeConfig(t, `
hosts:
  - {id: h0}
escrow:
  slots: 4
height_sync:
  anchor_period_nonces: 3
  sync_turn_slots: 8
`)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	valErr := cfg.Validate()
	require.Error(t, valErr)
	require.Contains(t, valErr.Error(), "anchor_period_nonces")
}

func TestConfig_HeightSyncBlockInterval(t *testing.T) {
	// Explicit height_sync.block_interval wins.
	path := writeConfig(t, `
hosts: [{id: h0}]
escrow: {slots: 4}
chain: {block_time: "500ms"}
height_sync: {block_interval: "2s"}
`)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	require.Equal(t, 2*1_000_000_000, int(cfg.HeightSyncBlockInterval().Nanoseconds()))

	// Falls back to chain.block_time when height_sync.block_interval is empty.
	path = writeConfig(t, `
hosts: [{id: h0}]
escrow: {slots: 4}
chain: {block_time: "500ms"}
`)
	cfg, err = config.Load(path)
	require.NoError(t, err)
	require.Equal(t, 500, int(cfg.HeightSyncBlockInterval().Milliseconds()))

	// Falls back to 1s on malformed values.
	path = writeConfig(t, `
hosts: [{id: h0}]
escrow: {slots: 4}
chain: {block_time: "not-a-duration"}
`)
	cfg, err = config.Load(path)
	require.NoError(t, err)
	require.Equal(t, 1000, int(cfg.HeightSyncBlockInterval().Milliseconds()))
}

func TestConfig_HeightSyncBlockIntervalDelta(t *testing.T) {
	path := writeConfig(t, `
hosts: [{id: h0}]
escrow: {slots: 4}
height_sync: {block_interval_delta: "250ms"}
`)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	require.Equal(t, 250*time.Millisecond, cfg.HeightSyncBlockIntervalDelta())

	path = writeConfig(t, `
hosts: [{id: h0}]
escrow: {slots: 4}
height_sync: {block_interval_delta: "bad"}
`)
	cfg, err = config.Load(path)
	require.NoError(t, err)
	require.Equal(t, time.Duration(0), cfg.HeightSyncBlockIntervalDelta())
}

func TestConfig_HeightSyncURLDefaultsFromPort(t *testing.T) {
	path := writeConfig(t, `
hosts: [{id: h0}]
escrow: {slots: 4}
height_sync: {port: 9123}
`)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	require.Equal(t, "http://height-sync:9123", cfg.HeightSync.URL)
}

// genHexKey returns a fresh hex-encoded secp256k1 private key in the
// plain-hex format crypto.HexToECDSA accepts.
func genHexKey(t *testing.T) string {
	t.Helper()
	k, err := crypto.GenerateKey()
	require.NoError(t, err)
	return hex.EncodeToString(crypto.FromECDSA(k))
}

func TestConfig_HeightSyncValidators_ParsesAll(t *testing.T) {
	k1 := genHexKey(t)
	k2 := genHexKey(t)
	path := writeConfig(t, `
hosts: [{id: h0}]
escrow: {slots: 4}
height_sync:
  validators:
    - private_key_hex: "`+k1+`"
      power: 3
    - private_key_hex: "`+k2+`"
`)
	cfg, err := config.Load(path)
	require.NoError(t, err)

	// Power defaults kicked in for the second validator.
	require.Equal(t, config.DefaultValidatorPower, cfg.HeightSync.Validators[1].Power)

	resolved, err := cfg.HeightSyncValidators()
	require.NoError(t, err)
	require.Len(t, resolved, 2)
	require.Equal(t, int64(3), resolved[0].Power)
	require.Equal(t, config.DefaultValidatorPower, resolved[1].Power)
	require.Len(t, resolved[0].Address, 20)
	require.Len(t, resolved[1].Address, 20)
	require.NotEqual(t, resolved[0].Address, resolved[1].Address)
}

func TestConfig_HeightSyncValidators_RejectsEmptyList(t *testing.T) {
	path := writeConfig(t, `
hosts: [{id: h0}]
escrow: {slots: 4}
`)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	_, err = cfg.HeightSyncValidators()
	require.Error(t, err)
}

func TestConfig_HeightSyncValidators_RejectsTODO(t *testing.T) {
	path := writeConfig(t, `
hosts: [{id: h0}]
escrow: {slots: 4}
height_sync:
  validators:
    - private_key_hex: "TODO(phase-10): validator 0"
      power: 1
`)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	_, err = cfg.HeightSyncValidators()
	require.Error(t, err)
	require.Contains(t, err.Error(), "TODO placeholder")
}

func TestConfig_HeightSyncValidators_RejectsDuplicates(t *testing.T) {
	k := genHexKey(t)
	path := writeConfig(t, `
hosts: [{id: h0}]
escrow: {slots: 4}
height_sync:
  validators:
    - private_key_hex: "`+k+`"
    - private_key_hex: "`+k+`"
`)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	_, err = cfg.HeightSyncValidators()
	require.Error(t, err)
	require.Contains(t, err.Error(), "duplicates")
}
