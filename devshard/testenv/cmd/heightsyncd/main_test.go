package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"

	blockoracle "devshard/blockoracle"
	"devshard/blockoracle/client"
	"devshard/blockoracle/standalone"
	"devshard/blockoracle/verifier"
	"devshard/signing"
	"devshard/testenv/config"
)

// writeYAML stamps a yaml blob and returns its path.
func writeYAML(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

// genHexKey returns a fresh hex-encoded secp256k1 private key.
func genHexKey(t *testing.T) string {
	t.Helper()
	k, err := crypto.GenerateKey()
	require.NoError(t, err)
	return hex.EncodeToString(crypto.FromECDSA(k))
}

// addressFor parses the configured hex key and returns the 20-byte
// validator address derived from its pubkey — the same address the
// observer stamps into Commit.Signatures[].ValidatorAddress.
func addressFor(t *testing.T, hexKey string) []byte {
	t.Helper()
	signer, err := signing.SignerFromHex(hexKey)
	require.NoError(t, err)
	addr, err := blockoracle.AddressBytes(signer.PublicKeyBytes())
	require.NoError(t, err)
	return addr
}

// TestBuildStandaloneConfig_MapsValidatorsFromYAML asserts the full
// producer-side validator set plumbed through the yaml → standalone
// conversion preserves count, addresses, and power verbatim. This is
// the single structural test for the Phase 3 contract: the mock-mainnet
// validator set is declared once in testenv/config.yaml and nowhere
// else.
func TestBuildStandaloneConfig_MapsValidatorsFromYAML(t *testing.T) {
	k1, k2, k3 := genHexKey(t), genHexKey(t), genHexKey(t)
	path := writeYAML(t, fmt.Sprintf(`
hosts:
  - {id: h0}
escrow:
  slots: 4
chain:
  id: "gonka-from-yaml"
  block_time: "250ms"
height_sync:
  port: 9123
  initial_height: 42
  seed: 7
  block_interval: "125ms"
  validators:
    - {private_key_hex: "%s", power: 5}
    - {private_key_hex: "%s", power: 1}
    - {private_key_hex: "%s"}
`, k1, k2, k3))

	cfg, err := config.Load(path)
	require.NoError(t, err)
	require.NoError(t, cfg.Validate())

	sc, err := buildStandaloneConfig(cfg)
	require.NoError(t, err)

	require.Equal(t, "gonka-from-yaml", sc.ChainID)
	require.Equal(t, ":9123", sc.Addr)
	require.EqualValues(t, 42, sc.InitialHeight)
	require.EqualValues(t, 7, sc.Seed)
	require.Equal(t, 125*time.Millisecond, sc.BlockInterval)

	require.Len(t, sc.Validators, 3)

	wantAddrs := [][]byte{
		addressFor(t, k1),
		addressFor(t, k2),
		addressFor(t, k3),
	}
	wantPower := []int64{5, 1, config.DefaultValidatorPower}
	for i, v := range sc.Validators {
		require.NotNil(t, v.Signer, "validator %d signer", i)
		require.Equalf(t, wantAddrs[i], v.Address, "validator %d address", i)
		require.Equalf(t, wantPower[i], v.Power, "validator %d power", i)
	}
}

// TestBuildStandaloneConfig_ErrorsOnMissingOrBadValidators covers the
// fail-loud paths: empty list, TODO placeholder, duplicate key.
// Regressions would let heightsyncd boot with a broken validator set.
func TestBuildStandaloneConfig_ErrorsOnMissingOrBadValidators(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "empty list",
			body: `
hosts: [{id: h0}]
escrow: {slots: 4}
`,
			want: "validators must declare",
		},
		{
			name: "todo placeholder",
			body: `
hosts: [{id: h0}]
escrow: {slots: 4}
height_sync:
  validators:
    - {private_key_hex: "TODO(phase-10): mock validator"}
`,
			want: "TODO placeholder",
		},
		{
			name: "duplicate key",
			body: func() string {
				k := genHexKey(t)
				return fmt.Sprintf(`
hosts: [{id: h0}]
escrow: {slots: 4}
height_sync:
  validators:
    - {private_key_hex: "%s"}
    - {private_key_hex: "%s"}
`, k, k)
			}(),
			want: "duplicates",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeYAML(t, tc.body)
			cfg, err := config.Load(path)
			require.NoError(t, err)
			_, err = buildStandaloneConfig(cfg)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.want)
		})
	}
}

// TestBuildStandaloneConfig_ShippedConfigYAML guards the committed
// testenv/config.yaml: parsing + plumbing must succeed end-to-end
// against the skeleton the repo ships today (TODO placeholders are
// rewritten by gencompose before the stack boots, so we round-trip
// through gencompose-style key filling first).
func TestBuildStandaloneConfig_ShippedConfigYAML(t *testing.T) {
	// Load the shipped skeleton from testenv/.
	dir := t.TempDir()
	srcPath := filepath.Join("..", "..", "config.yaml")
	data, err := os.ReadFile(srcPath)
	require.NoError(t, err)
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, data, 0o600))

	cfg, err := config.Load(path)
	require.NoError(t, err)

	// Fill TODO placeholders the way gencompose does.
	for i := range cfg.HeightSync.Validators {
		v := &cfg.HeightSync.Validators[i]
		if v.PrivateKeyHex == "" ||
			bytes.HasPrefix([]byte(v.PrivateKeyHex), []byte("TODO")) {
			v.PrivateKeyHex = genHexKey(t)
		}
	}
	for i := range cfg.Hosts {
		if cfg.Hosts[i].PrivateKeyHex == "" ||
			bytes.HasPrefix([]byte(cfg.Hosts[i].PrivateKeyHex), []byte("TODO")) {
			cfg.Hosts[i].PrivateKeyHex = genHexKey(t)
		}
	}
	if cfg.User.PrivateKeyHex == "" ||
		bytes.HasPrefix([]byte(cfg.User.PrivateKeyHex), []byte("TODO")) {
		cfg.User.PrivateKeyHex = genHexKey(t)
	}

	sc, err := buildStandaloneConfig(cfg)
	require.NoError(t, err)
	// Shipped skeleton declares the default 10-validator set.
	require.Len(t, sc.Validators, config.DefaultHeightSyncValidators)
	require.Equal(t, cfg.Chain.ID, sc.ChainID)
}

// TestHeightSync_SignsWithConfiguredValidators is the end-to-end
// proof-of-plumbing: boot the standalone server from the testenv
// config, subscribe an external (verifying) client, and assert every
// signature carried in Commit.Signatures resolves to a validator
// listed in the same yaml. This is what an external auditor would
// check — hosts trust the oracle and skip verification, but the set
// must still be auditable from the same config.
func TestHeightSync_SignsWithConfiguredValidators(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 5 validators with non-uniform power to also lock in the
	// observer's "signer set power > 3/4" invariant under the
	// yaml-configured set.
	keys := []string{
		genHexKey(t), genHexKey(t), genHexKey(t), genHexKey(t), genHexKey(t),
	}
	powers := []int64{10, 5, 3, 2, 1} // total 21; 3/4 floor = 15.75
	var validatorsYAML string
	for i, k := range keys {
		validatorsYAML += fmt.Sprintf(`
    - {private_key_hex: "%s", power: %d}`, k, powers[i])
	}

	path := writeYAML(t, `
hosts: [{id: h0}]
escrow: {slots: 4}
chain:
  id: "gonka-multi-validator"
  block_time: "50ms"
height_sync:
  port: 0
  initial_height: 1
  seed: 1
  validators:`+validatorsYAML+`
`)
	cfg, err := config.Load(path)
	require.NoError(t, err)

	sc, err := buildStandaloneConfig(cfg)
	require.NoError(t, err)

	// Replace the Addr with an ephemeral listener so two parallel
	// runs never fight over the same port.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	sc.Addr = ""
	sc.Listener = lis

	svc, err := standalone.New(sc)
	require.NoError(t, err)

	runErr := make(chan error, 1)
	go func() { runErr <- svc.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case err := <-runErr:
			require.NoError(t, err)
		case <-time.After(5 * time.Second):
			t.Fatal("standalone Run did not return")
		}
	}()

	// Tickle the observer so we have two blocks to inspect
	// deterministically, independent of wall-clock timing.
	_, err = svc.Observer().AdvanceOne()
	require.NoError(t, err)
	_, err = svc.Observer().AdvanceOne()
	require.NoError(t, err)

	// Wait for the HTTP listener to publish the blocks.
	baseURL := "http://" + lis.Addr().String()
	require.Eventually(t, func() bool {
		resp, err := http.Get(baseURL + "/block/latest")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode == http.StatusOK
	}, 3*time.Second, 20*time.Millisecond)

	// External-auditor path: verify-on-ingest with the pinned
	// validator set from the yaml. This proves the consumer-side
	// view of the validator set matches the producer-side view.
	pinnedValidators := make([]verifier.Validator, len(sc.Validators))
	for i, v := range sc.Validators {
		pinnedValidators[i] = verifier.Validator{
			Address: v.Address,
			Power:   v.Power,
		}
	}
	set, err := verifier.NewValidatorSet(cfg.Chain.ID, pinnedValidators)
	require.NoError(t, err)
	ver := verifier.New(set)

	c, err := client.NewHTTP(ctx, client.HTTPConfig{
		BaseURL:    baseURL,
		Verifier:   ver,
		StaleAfter: 10 * time.Second,
	})
	require.NoError(t, err)
	defer c.Close()

	var h *blockoracle.Header
	require.Eventually(t, func() bool {
		h, err = c.Latest(ctx)
		return err == nil && h != nil
	}, 3*time.Second, 20*time.Millisecond)

	// Every signature must resolve to a configured validator.
	configuredAddrs := make(map[string]int64, len(sc.Validators))
	for _, v := range sc.Validators {
		configuredAddrs[string(v.Address)] = v.Power
	}
	require.NotEmpty(t, h.Commit.Signatures)

	seen := make(map[string]struct{}, len(h.Commit.Signatures))
	var signerPower int64
	for _, s := range h.Commit.Signatures {
		power, ok := configuredAddrs[string(s.ValidatorAddress)]
		require.Truef(t, ok,
			"signature from unknown validator %x", s.ValidatorAddress)
		require.NotContainsf(t, seen, string(s.ValidatorAddress),
			"duplicate signature for %x", s.ValidatorAddress)
		seen[string(s.ValidatorAddress)] = struct{}{}
		signerPower += power
	}

	var total int64
	for _, p := range powers {
		total += p
	}
	// Observer contract: signer power is strictly > 3/4 of total.
	require.Greater(t, 4*signerPower, 3*total,
		"signer power %d should be > 3/4 of total %d", signerPower, total)

	// And the pinned Verifier accepting the live header is the final
	// seal: the produced + pinned sets agree on signatures, addresses,
	// and power (> 2/3 of pinned total).
	require.NoError(t, ver.Verify(h, 0))
}
