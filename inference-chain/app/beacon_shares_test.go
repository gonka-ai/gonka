package app

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"

	"cosmossdk.io/log"
	"github.com/cosmos/cosmos-sdk/client/flags"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/app/beacon"
	blstypes "github.com/productscience/inference/x/bls/types"
)

func TestBeaconConfigFromAppOpts(t *testing.T) {
	// app.toml without a [bls-beacon] section (nodes upgraded in place).
	cfg, dir := beaconConfigFromAppOpts(simtestutil.AppOptionsMap{flags.FlagHome: "/root/.inference"})
	require.True(t, cfg.Enabled, "missing section keeps the default")
	require.Equal(t, "/root/.inference/bls-beacon", dir)

	cfg, _ = beaconConfigFromAppOpts(simtestutil.AppOptionsMap{flags.FlagHome: "/h", FlagBeaconEnabled: false})
	require.False(t, cfg.Enabled)
	cfg, _ = beaconConfigFromAppOpts(simtestutil.AppOptionsMap{flags.FlagHome: "/h", FlagBeaconEnabled: "false"})
	require.False(t, cfg.Enabled, "string values from env/flags")

	_, dir = beaconConfigFromAppOpts(simtestutil.AppOptionsMap{flags.FlagHome: "/h", FlagBeaconShareDir: "/shares"})
	require.Equal(t, "/shares", dir)

	_, dir = beaconConfigFromAppOpts(simtestutil.AppOptionsMap{})
	require.Empty(t, dir, "no home and no dir: provider stays off")
}

func TestSetupBeaconSharesDisabled(t *testing.T) {
	a := &App{}
	a.setupBeaconShares(simtestutil.AppOptionsMap{flags.FlagHome: t.TempDir(), FlagBeaconEnabled: false}, log.NewNopLogger())
	require.Nil(t, a.BeaconShares)

	a.setupBeaconShares(simtestutil.AppOptionsMap{}, log.NewNopLogger())
	require.Nil(t, a.BeaconShares)
}

func TestSetupBeaconSharesEnabled(t *testing.T) {
	home := t.TempDir()
	a := &App{}
	a.setupBeaconShares(simtestutil.AppOptionsMap{flags.FlagHome: home}, log.NewNopLogger())
	require.NotNil(t, a.BeaconShares)
	p := a.BeaconShares.(*beacon.FileShareProvider)
	require.Equal(t, filepath.Join(home, "bls-beacon"), p.Dir())
	require.NoError(t, p.Close())
}

func completedEpochData() *blstypes.EpochBLSData {
	slotPKs := make([][]byte, 8)
	for i := range slotPKs {
		slotPKs[i] = []byte{byte(i)}
	}
	return &blstypes.EpochBLSData{
		EpochId:        4,
		ITotalSlots:    8,
		DkgPhase:       blstypes.DKGPhase_DKG_PHASE_COMPLETED,
		GroupPublicKey: []byte("gpk"),
		SlotPublicKeys: slotPKs,
		Participants: []blstypes.BLSParticipantInfo{
			{Address: "gonka1a", SlotStartIndex: 0, SlotEndIndex: 2},
			{Address: "gonka1b", SlotStartIndex: 3, SlotEndIndex: 7},
		},
	}
}

func noPrecompute(t *testing.T) func(*blstypes.EpochBLSData) ([][]byte, error) {
	return func(*blstypes.EpochBLSData) ([][]byte, error) {
		t.Fatal("precompute must not run when slot public keys are stored")
		return nil, nil
	}
}

// consOf resolves participants from a map (missing → no validator).
func consOf(m map[string][]byte) func(string) ([]byte, error) {
	return func(p string) ([]byte, error) { return m[p], nil }
}

func TestEpochKeysFromDataOwnerConsAddress(t *testing.T) {
	data := completedEpochData()
	cons := bytes.Repeat([]byte{7}, 20)
	var asked []string
	resolve := func(p string) ([]byte, error) {
		asked = append(asked, p)
		return map[string][]byte{"gonka1b": cons}[p], nil
	}
	keys, err := epochKeysFromData(data, 3, 7, noPrecompute(t), resolve)
	require.NoError(t, err)
	require.Equal(t, cons, keys.OwnerConsAddress)
	require.Equal(t, []string{"gonka1b"}, asked)

	// Owner without a validator: nil, not an error.
	keys, err = epochKeysFromData(data, 0, 2, noPrecompute(t), resolve)
	require.NoError(t, err)
	require.Nil(t, keys.OwnerConsAddress)

	// No exact owner: nothing to resolve.
	asked = nil
	keys, err = epochKeysFromData(data, 0, 3, noPrecompute(t), resolve)
	require.NoError(t, err)
	require.Nil(t, keys.OwnerConsAddress)
	require.Empty(t, asked)

	// Lookup errors surface.
	_, err = epochKeysFromData(data, 3, 7, noPrecompute(t), func(string) ([]byte, error) { return nil, errors.New("store") })
	require.ErrorContains(t, err, "store")

	// Too few stored slot public keys after precompute: error, not a panic.
	data.SlotPublicKeys = nil
	_, err = epochKeysFromData(data, 3, 7, func(*blstypes.EpochBLSData) ([][]byte, error) { return [][]byte{{0}}, nil }, consOf(nil))
	require.ErrorContains(t, err, "slot public keys")
}

func TestEpochKeysFromData(t *testing.T) {
	data := completedEpochData()
	keys, err := epochKeysFromData(data, 3, 7, noPrecompute(t), consOf(nil))
	require.NoError(t, err)
	require.True(t, keys.Ready)
	require.Equal(t, []byte("gpk"), keys.GroupPublicKey)
	require.Equal(t, "gonka1b", keys.OwnerAddress)
	require.Equal(t, [][]byte{{3}, {4}, {5}, {6}, {7}}, keys.SlotPublicKeys)

	// SIGNED is final too.
	data.DkgPhase = blstypes.DKGPhase_DKG_PHASE_SIGNED
	keys, err = epochKeysFromData(data, 0, 2, noPrecompute(t), consOf(nil))
	require.NoError(t, err)
	require.Equal(t, "gonka1a", keys.OwnerAddress)

	// A range that is not exactly one participant's has no owner.
	keys, err = epochKeysFromData(data, 0, 3, noPrecompute(t), consOf(nil))
	require.NoError(t, err)
	require.Empty(t, keys.OwnerAddress)

	// Out of bounds.
	_, err = epochKeysFromData(data, 6, 8, noPrecompute(t), consOf(nil))
	require.Error(t, err)
	_, err = epochKeysFromData(data, 5, 4, noPrecompute(t), consOf(nil))
	require.Error(t, err)
}

func TestEpochKeysFromDataPhases(t *testing.T) {
	for _, phase := range []blstypes.DKGPhase{
		blstypes.DKGPhase_DKG_PHASE_UNDEFINED,
		blstypes.DKGPhase_DKG_PHASE_DEALING,
		blstypes.DKGPhase_DKG_PHASE_VERIFYING,
		blstypes.DKGPhase_DKG_PHASE_DISPUTING,
	} {
		data := completedEpochData()
		data.DkgPhase = phase
		keys, err := epochKeysFromData(data, 0, 2, noPrecompute(t), consOf(nil))
		require.NoError(t, err, phase)
		require.False(t, keys.Ready, phase)
	}
	data := completedEpochData()
	data.DkgPhase = blstypes.DKGPhase_DKG_PHASE_FAILED
	_, err := epochKeysFromData(data, 0, 2, noPrecompute(t), consOf(nil))
	require.ErrorContains(t, err, "failed")
}

func TestEpochKeysFromDataPrecomputesLegacyEpochs(t *testing.T) {
	data := completedEpochData()
	all := data.SlotPublicKeys
	data.SlotPublicKeys = nil
	called := 0
	keys, err := epochKeysFromData(data, 3, 4, func(*blstypes.EpochBLSData) ([][]byte, error) {
		called++
		return all, nil
	}, consOf(nil))
	require.NoError(t, err)
	require.Equal(t, 1, called)
	require.Equal(t, [][]byte{{3}, {4}}, keys.SlotPublicKeys)

	_, err = epochKeysFromData(data, 3, 4, func(*blstypes.EpochBLSData) ([][]byte, error) {
		return nil, errors.New("boom")
	}, consOf(nil))
	require.ErrorContains(t, err, "boom")
}

// L1: only deterministic verdicts are ErrKeysInvalid; lookup failures are not.
func TestEpochKeysFromDataErrorClasses(t *testing.T) {
	data := completedEpochData()
	data.DkgPhase = blstypes.DKGPhase_DKG_PHASE_FAILED
	_, err := epochKeysFromData(data, 0, 2, noPrecompute(t), consOf(nil))
	require.ErrorIs(t, err, beacon.ErrKeysInvalid, "failed DKG")

	data = completedEpochData()
	_, err = epochKeysFromData(data, 6, 8, noPrecompute(t), consOf(nil))
	require.ErrorIs(t, err, beacon.ErrKeysInvalid, "range outside the epoch")

	_, err = epochKeysFromData(data, 3, 7, noPrecompute(t), func(string) ([]byte, error) { return nil, errors.New("store") })
	require.Error(t, err)
	require.NotErrorIs(t, err, beacon.ErrKeysInvalid, "validator lookup failure is transient")

	data.SlotPublicKeys = nil
	_, err = epochKeysFromData(data, 3, 4, func(*blstypes.EpochBLSData) ([][]byte, error) { return nil, errors.New("boom") }, consOf(nil))
	require.Error(t, err)
	require.NotErrorIs(t, err, beacon.ErrKeysInvalid, "precompute failure is transient")
}
