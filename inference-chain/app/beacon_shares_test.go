package app

import (
	"errors"
	"path/filepath"
	"testing"

	"cosmossdk.io/log"
	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cosmos/cosmos-sdk/client/flags"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"
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

func TestEpochKeysFromData(t *testing.T) {
	data := completedEpochData()
	keys, err := epochKeysFromData(data, 3, 7, noPrecompute(t))
	require.NoError(t, err)
	require.True(t, keys.Ready)
	require.Equal(t, []byte("gpk"), keys.GroupPublicKey)
	require.Equal(t, "gonka1b", keys.OwnerAddress)
	require.Equal(t, [][]byte{{3}, {4}, {5}, {6}, {7}}, keys.SlotPublicKeys)

	// SIGNED is final too.
	data.DkgPhase = blstypes.DKGPhase_DKG_PHASE_SIGNED
	keys, err = epochKeysFromData(data, 0, 2, noPrecompute(t))
	require.NoError(t, err)
	require.Equal(t, "gonka1a", keys.OwnerAddress)

	// A range that is not exactly one participant's has no owner.
	keys, err = epochKeysFromData(data, 0, 3, noPrecompute(t))
	require.NoError(t, err)
	require.Empty(t, keys.OwnerAddress)

	// Out of bounds.
	_, err = epochKeysFromData(data, 6, 8, noPrecompute(t))
	require.Error(t, err)
	_, err = epochKeysFromData(data, 5, 4, noPrecompute(t))
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
		keys, err := epochKeysFromData(data, 0, 2, noPrecompute(t))
		require.NoError(t, err, phase)
		require.False(t, keys.Ready, phase)
	}
	data := completedEpochData()
	data.DkgPhase = blstypes.DKGPhase_DKG_PHASE_FAILED
	_, err := epochKeysFromData(data, 0, 2, noPrecompute(t))
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
	})
	require.NoError(t, err)
	require.Equal(t, 1, called)
	require.Equal(t, [][]byte{{3}, {4}}, keys.SlotPublicKeys)

	_, err = epochKeysFromData(data, 3, 4, func(*blstypes.EpochBLSData) ([][]byte, error) {
		return nil, errors.New("boom")
	})
	require.ErrorContains(t, err, "boom")
}

type recordingProvider struct {
	epochs []uint64
	panic  bool
}

func (r *recordingProvider) Get(_ sdk.Context, epochID uint64) (*beacon.EpochShares, bool) {
	if r.panic {
		panic("provider exploded")
	}
	r.epochs = append(r.epochs, epochID)
	return &beacon.EpochShares{EpochID: epochID}, true
}

func TestBeaconShareProbe(t *testing.T) {
	rec := &recordingProvider{}
	signing := func(sdk.Context) (uint64, bool) { return 9, true }
	h := newBeaconExtendVoteHandler(log.NewNopLogger(), beaconShareProbe(func() beacon.ShareProvider { return rec }, signing))

	resp, err := h(sdk.Context{}, &abci.RequestExtendVote{Height: 10})
	require.NoError(t, err)
	require.Empty(t, resp.VoteExtension, "Step 2 never puts shares or partials in the extension")
	require.Equal(t, []uint64{9}, rec.epochs)

	// No signing epoch yet: provider not asked.
	h = newBeaconExtendVoteHandler(log.NewNopLogger(), beaconShareProbe(func() beacon.ShareProvider { return rec },
		func(sdk.Context) (uint64, bool) { return 0, false }))
	_, err = h(sdk.Context{}, &abci.RequestExtendVote{Height: 11})
	require.NoError(t, err)
	require.Equal(t, []uint64{9}, rec.epochs)

	// Provider disabled (nil).
	h = newBeaconExtendVoteHandler(log.NewNopLogger(), beaconShareProbe(func() beacon.ShareProvider { return nil }, signing))
	resp, err = h(sdk.Context{}, &abci.RequestExtendVote{Height: 12})
	require.NoError(t, err)
	require.Empty(t, resp.VoteExtension)

	// A panicking provider still yields an empty extension and no error (R1).
	h = newBeaconExtendVoteHandler(log.NewNopLogger(), beaconShareProbe(func() beacon.ShareProvider { return &recordingProvider{panic: true} }, signing))
	resp, err = h(sdk.Context{}, &abci.RequestExtendVote{Height: 13})
	require.NoError(t, err)
	require.Empty(t, resp.VoteExtension)
}
