package app

// Block BLS beacon, Step 2: the share bridge on the node side
// (proposals/block-bls-beacon/block-bls-beacon-vote-extensions-plan.md).
//
// The dAPI exports this validator's recovered BLS slot shares to
// `<home>/bls-beacon/epoch-<N>.json`; the FileShareProvider loads them into
// memory and checks them against committed state in the background. Step 3's
// ExtendVote (beacon_extend.go) signs with them.

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"cosmossdk.io/log"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cosmos/cosmos-sdk/client/flags"
	servertypes "github.com/cosmos/cosmos-sdk/server/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/spf13/cast"

	"github.com/productscience/inference/app/beacon"
	"github.com/productscience/inference/app/beacon/sharefile"
	blstypes "github.com/productscience/inference/x/bls/types"

	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
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
	identity, err := newCometSelfIdentity(cast.ToString(appOpts.Get(FlagRPCListenAddr)), logger)
	if err != nil {
		logger.Error("beacon share provider disabled: cannot reach own validator identity", "err", err)
		return
	}
	p, err := beacon.NewFileShareProvider(dir, blsCommittedKeys{app}, identity, logger)
	if err != nil {
		logger.Error("beacon share provider disabled", "dir", dir, "err", err)
		return
	}
	// Verify files in the background on an immutable committed version, so
	// ExtendVote never reads committed state for it (review L5).
	p.SetVerifyContext(app.beaconVerifyContext)
	identity.start(context.Background())
	p.Start(context.Background())
	app.beaconIdentity = identity
	app.BeaconShares = p
	logger.Info("beacon share provider started", "dir", dir, "identity_url", identity.statusURL)
}

// Close stops the beacon share watcher and identity lookup, then closes the BaseApp.
func (app *App) Close() error {
	if p, ok := app.BeaconShares.(interface{ Close() error }); ok && p != nil {
		_ = p.Close()
	}
	if app.beaconIdentity != nil {
		app.beaconIdentity.close()
	}
	app.beaconCollector.close()
	return app.App.Close()
}

// beaconVerifyContext is a read-only context over the latest committed version
// recorded by the PreBlocker. It reads an immutable IAVL version (as gRPC
// queries do), so it is safe to use concurrently with block execution.
func (app *App) beaconVerifyContext() (sdk.Context, error) {
	h := app.beaconCommittedHeight.Load()
	if h <= 0 {
		return sdk.Context{}, errors.New("no committed block yet")
	}
	return app.beaconContextAt(h)
}

// beaconContextAt is a read-only context over committed version v (an
// immutable IAVL version, as gRPC queries use), safe off the ABCI goroutine.
func (app *App) beaconContextAt(v int64) (sdk.Context, error) {
	if v <= 0 {
		return sdk.Context{}, fmt.Errorf("no committed version %d", v)
	}
	cms, err := app.CommitMultiStore().CacheMultiStoreWithVersion(v)
	if err != nil {
		return sdk.Context{}, fmt.Errorf("committed version %d: %w", v, err)
	}
	return sdk.NewContext(cms, cmtproto.Header{ChainID: app.ChainID(), Height: v}, false, app.Logger()), nil
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
	}, func(participant string) ([]byte, error) {
		return k.consAddressOf(ctx, participant)
	})
}

// consAddressOf resolves a participant (account) to its validator's consensus
// address: participant account bytes are the validator operator bytes
// (epochgroup/epoch_group.go). Returns nil, nil when there is no validator.
func (k blsCommittedKeys) consAddressOf(ctx sdk.Context, participant string) ([]byte, error) {
	acc, err := sdk.AccAddressFromBech32(participant)
	if err != nil {
		return nil, fmt.Errorf("participant %q: %w", participant, err)
	}
	val, err := k.app.StakingKeeper.GetValidator(ctx, sdk.ValAddress(acc))
	if err != nil {
		if errors.Is(err, stakingtypes.ErrNoValidatorFound) {
			return nil, nil
		}
		return nil, err
	}
	return val.GetConsAddr()
}

// epochKeysFromData extracts what the share provider checks. precompute is
// used only for epochs whose slot public keys were not stored at completion.
//
// consAddrOf resolves the owner's validator consensus address (nil if none).
func epochKeysFromData(data *blstypes.EpochBLSData, slotStart, slotEnd uint32,
	precompute func(*blstypes.EpochBLSData) ([][]byte, error),
	consAddrOf func(participant string) ([]byte, error),
) (beacon.EpochKeys, error) {
	switch data.DkgPhase {
	case blstypes.DKGPhase_DKG_PHASE_COMPLETED, blstypes.DKGPhase_DKG_PHASE_SIGNED:
	case blstypes.DKGPhase_DKG_PHASE_FAILED:
		return beacon.EpochKeys{}, fmt.Errorf("%w: epoch %d DKG failed", beacon.ErrKeysInvalid, data.EpochId)
	default:
		return beacon.EpochKeys{Ready: false}, nil
	}
	if slotEnd < slotStart || slotEnd >= data.ITotalSlots {
		return beacon.EpochKeys{}, fmt.Errorf("%w: slot range [%d,%d] outside %d slots", beacon.ErrKeysInvalid, slotStart, slotEnd, data.ITotalSlots)
	}

	owner := ""
	for i := range data.Participants {
		p := &data.Participants[i]
		if p.SlotStartIndex == slotStart && p.SlotEndIndex == slotEnd {
			owner = p.Address
			break
		}
	}

	var ownerCons []byte
	if owner != "" {
		var err error
		if ownerCons, err = consAddrOf(owner); err != nil {
			return beacon.EpochKeys{}, fmt.Errorf("slot owner validator: %w", err)
		}
	}

	slotPKs := data.SlotPublicKeys
	if len(slotPKs) < int(data.ITotalSlots) {
		var err error
		if slotPKs, err = precompute(data); err != nil {
			return beacon.EpochKeys{}, fmt.Errorf("precompute slot public keys: %w", err)
		}
	}
	if int(slotEnd) >= len(slotPKs) {
		return beacon.EpochKeys{}, fmt.Errorf("%d slot public keys, need slot %d", len(slotPKs), slotEnd)
	}
	return beacon.EpochKeys{
		Ready:            true,
		GroupPublicKey:   data.GroupPublicKey,
		SlotPublicKeys:   slotPKs[slotStart : slotEnd+1],
		OwnerAddress:     owner,
		OwnerConsAddress: ownerCons,
	}, nil
}
