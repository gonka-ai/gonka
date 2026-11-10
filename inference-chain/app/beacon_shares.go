package app

// Block BLS beacon, Step 2: the share bridge on the node side
// (devshard/docs/proposals/block-bls-beacon-vote-extensions-plan.md).
//
// The dAPI exports this validator's recovered BLS slot shares to
// `<home>/bls-beacon/epoch-<N>.json`; the FileShareProvider loads them into
// memory and checks them against committed state. Extensions stay empty in
// this step: ExtendVote only asks the provider for the current signing epoch,
// which runs the one-time verification and logs the result.

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"cosmossdk.io/log"
	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cosmos/cosmos-sdk/client/flags"
	servertypes "github.com/cosmos/cosmos-sdk/server/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/spf13/cast"

	"github.com/productscience/inference/app/beacon"
	"github.com/productscience/inference/app/beacon/sharefile"
	blstypes "github.com/productscience/inference/x/bls/types"
)

// app.toml keys, section [bls-beacon].
const (
	FlagBeaconEnabled  = "bls-beacon.enabled"
	FlagBeaconShareDir = "bls-beacon.share-dir"
)

// BeaconConfig is the [bls-beacon] section of app.toml.
type BeaconConfig struct {
	// Enabled loads the share files exported by the dAPI. Disabling it only
	// stops this node from contributing beacon partials.
	Enabled bool `mapstructure:"enabled"`
	// ShareDir defaults to <home>/bls-beacon when empty.
	ShareDir string `mapstructure:"share-dir"`
}

// DefaultBeaconConfig returns the defaults written to a new app.toml.
func DefaultBeaconConfig() BeaconConfig {
	return BeaconConfig{Enabled: true}
}

// BeaconConfigTemplate is appended to the app.toml template.
const BeaconConfigTemplate = `
###############################################################################
###                           Block BLS beacon                              ###
###############################################################################

[bls-beacon]

# Load the BLS slot shares the dAPI exports for this validator, so the node can
# sign per-block beacon partials in its vote extensions.
enabled = {{ .BlsBeacon.Enabled }}

# Directory of the exported share files (epoch-<N>.json). Empty means
# <home>/bls-beacon, which the dAPI writes by default.
share-dir = "{{ .BlsBeacon.ShareDir }}"
`

// beaconConfigFromAppOpts reads [bls-beacon]; missing keys (an app.toml from
// before this section existed) keep the defaults.
func beaconConfigFromAppOpts(appOpts servertypes.AppOptions) (BeaconConfig, string) {
	cfg := DefaultBeaconConfig()
	if v := appOpts.Get(FlagBeaconEnabled); v != nil {
		cfg.Enabled = cast.ToBool(v)
	}
	cfg.ShareDir = cast.ToString(appOpts.Get(FlagBeaconShareDir))
	dir := cfg.ShareDir
	if dir == "" {
		home := cast.ToString(appOpts.Get(flags.FlagHome))
		if home == "" {
			return cfg, ""
		}
		dir = filepath.Join(home, sharefile.DefaultDirName)
	}
	return cfg, dir
}

// setupBeaconShares creates and starts the share provider. A failure leaves
// the provider nil (no partials from this node), never fails startup.
func (app *App) setupBeaconShares(appOpts servertypes.AppOptions, logger log.Logger) {
	cfg, dir := beaconConfigFromAppOpts(appOpts)
	if !cfg.Enabled {
		logger.Info("beacon share provider disabled", "flag", FlagBeaconEnabled)
		return
	}
	if dir == "" {
		logger.Warn("beacon share provider disabled: no home or share dir configured")
		return
	}
	p, err := beacon.NewFileShareProvider(dir, blsCommittedKeys{app}, logger)
	if err != nil {
		logger.Error("beacon share provider disabled", "dir", dir, "err", err)
		return
	}
	p.Start(context.Background())
	app.BeaconShares = p
	logger.Info("beacon share provider started", "dir", dir)
}

// Close stops the beacon share watcher, then closes the BaseApp.
func (app *App) Close() error {
	if p, ok := app.BeaconShares.(interface{ Close() error }); ok && p != nil {
		_ = p.Close()
	}
	return app.App.Close()
}

// blsCommittedKeys implements beacon.CommittedKeys on the x/bls keeper.
type blsCommittedKeys struct{ app *App }

func (k blsCommittedKeys) EpochKeys(ctx sdk.Context, epochID uint64, slotStart, slotEnd uint32) (beacon.EpochKeys, error) {
	data, err := k.app.BlsKeeper.GetEpochBLSData(ctx, epochID)
	if err != nil {
		if errors.Is(err, blstypes.ErrEpochBLSDataNotFound) {
			return beacon.EpochKeys{}, fmt.Errorf("epoch %d has no BLS data", epochID)
		}
		return beacon.EpochKeys{}, err
	}
	return epochKeysFromData(&data, slotStart, slotEnd, func(d *blstypes.EpochBLSData) ([][]byte, error) {
		return k.app.BlsKeeper.PrecomputeSlotPublicKeysBlst(d)
	})
}

// epochKeysFromData extracts what the share provider checks. precompute is
// used only for epochs whose slot public keys were not stored at completion.
func epochKeysFromData(data *blstypes.EpochBLSData, slotStart, slotEnd uint32, precompute func(*blstypes.EpochBLSData) ([][]byte, error)) (beacon.EpochKeys, error) {
	switch data.DkgPhase {
	case blstypes.DKGPhase_DKG_PHASE_COMPLETED, blstypes.DKGPhase_DKG_PHASE_SIGNED:
	case blstypes.DKGPhase_DKG_PHASE_FAILED:
		return beacon.EpochKeys{}, fmt.Errorf("epoch %d DKG failed", data.EpochId)
	default:
		return beacon.EpochKeys{Ready: false}, nil
	}
	if slotEnd < slotStart || slotEnd >= data.ITotalSlots {
		return beacon.EpochKeys{}, fmt.Errorf("slot range [%d,%d] outside %d slots", slotStart, slotEnd, data.ITotalSlots)
	}

	owner := ""
	for i := range data.Participants {
		p := &data.Participants[i]
		if p.SlotStartIndex == slotStart && p.SlotEndIndex == slotEnd {
			owner = p.Address
			break
		}
	}

	slotPKs := data.SlotPublicKeys
	if len(slotPKs) < int(data.ITotalSlots) {
		var err error
		if slotPKs, err = precompute(data); err != nil {
			return beacon.EpochKeys{}, fmt.Errorf("precompute slot public keys: %w", err)
		}
	}
	return beacon.EpochKeys{
		Ready:          true,
		GroupPublicKey: data.GroupPublicKey,
		SlotPublicKeys: slotPKs[slotStart : slotEnd+1],
		OwnerAddress:   owner,
	}, nil
}

// beaconShareProbe is the Step 2 extension builder: it asks the provider for
// the current signing epoch's shares (triggering the one-time verification)
// and still returns an empty extension. Step 3 replaces it with signing.
func beaconShareProbe(provider func() beacon.ShareProvider, signingEpoch func(ctx sdk.Context) (uint64, bool)) extensionBuilder {
	return func(ctx sdk.Context, _ *abci.RequestExtendVote) ([]byte, error) {
		p := provider()
		if p == nil {
			return []byte{}, nil
		}
		if epochID, ok := signingEpoch(ctx); ok {
			p.Get(ctx, epochID)
		}
		return []byte{}, nil
	}
}
