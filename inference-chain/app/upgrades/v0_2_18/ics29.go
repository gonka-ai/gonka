package v0_2_18

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	channeltypes "github.com/cosmos/ibc-go/v11/modules/core/04-channel/types"

	"github.com/productscience/inference/x/inference/types"
)

// feeIBCModuleName is the ICS-29 module account name from ibc-go v8.
// The v0.2.18 binary no longer registers that module, but coins escrowed
// before the upgrade remain at its derived address.
const feeIBCModuleName = "feeibc"

// ics29FeeVersion is the only fee version this chain ever negotiated.
const ics29FeeVersion = "ics29-1"

// ChannelVersionKeeper reads and writes IBC channel ends.
type ChannelVersionKeeper interface {
	GetAllChannels(ctx sdk.Context) []channeltypes.IdentifiedChannel
	SetChannel(ctx sdk.Context, portID, channelID string, channel channeltypes.Channel)
}

// BalanceKeeper reads an account balance.
type BalanceKeeper interface {
	GetAllBalances(ctx context.Context, addr sdk.AccAddress) sdk.Coins
}

// CommunityPoolFunder moves coins from an account into the community pool.
type CommunityPoolFunder interface {
	FundCommunityPool(ctx context.Context, amount sdk.Coins, sender sdk.AccAddress) error
}

type upgradeLogger interface {
	LogInfo(msg string, subSystem types.SubSystem, keyvals ...interface{})
}

// unwrapICS29ChannelVersions replaces a stored ICS-29 wrapper with the inner
// application version. The fee middleware used to unwrap this on every read.
// Without it, ICA parses the wrapper as ICS-27 metadata and the packet fails.
// The scan runs at upgrade height, so a channel opened by the old binary in
// the block before the halt is included.
func unwrapICS29ChannelVersions(ctx sdk.Context, keeper ChannelVersionKeeper, logger upgradeLogger) error {
	rewritten := 0
	for _, identified := range keeper.GetAllChannels(ctx) {
		appVersion, wrapped, err := unwrapICS29Version(identified.Version)
		if err != nil {
			return fmt.Errorf("channel %s/%s: %w", identified.PortId, identified.ChannelId, err)
		}
		if !wrapped {
			continue
		}
		keeper.SetChannel(ctx, identified.PortId, identified.ChannelId, channeltypes.NewChannel(
			identified.State,
			identified.Ordering,
			identified.Counterparty,
			identified.ConnectionHops,
			appVersion,
		))
		rewritten++
		logger.LogInfo("unwrapped ICS-29 channel version", types.Upgrades,
			"port", identified.PortId,
			"channel", identified.ChannelId,
			"app_version", appVersion,
		)
	}
	logger.LogInfo("ICS-29 channel version scan complete", types.Upgrades, "rewritten", rewritten)
	return nil
}

// unwrapICS29Version reports the inner app version when version is ICS-29
// metadata. A plain ics20-1 or ics27-1 version is left unchanged. Metadata
// that claims to be ICS-29 but is not valid fails the upgrade.
func unwrapICS29Version(version string) (string, bool, error) {
	trimmed := strings.TrimSpace(version)
	if !strings.HasPrefix(trimmed, "{") {
		return "", false, nil
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		return "", false, nil
	}
	feeRaw, ok := raw["fee_version"]
	if !ok {
		return "", false, nil
	}

	var feeVersion string
	if err := json.Unmarshal(feeRaw, &feeVersion); err != nil {
		return "", false, fmt.Errorf("fee_version is not a string")
	}
	if feeVersion != ics29FeeVersion {
		return "", false, fmt.Errorf("unsupported ICS-29 fee_version %q", feeVersion)
	}

	appRaw, ok := raw["app_version"]
	if !ok {
		return "", false, fmt.Errorf("ICS-29 metadata is missing app_version")
	}
	var appVersion string
	if err := json.Unmarshal(appRaw, &appVersion); err != nil {
		return "", false, fmt.Errorf("app_version is not a string")
	}
	if appVersion == "" {
		return "", false, fmt.Errorf("ICS-29 metadata has an empty app_version")
	}
	return appVersion, true, nil
}

// sweepFeeIBCBalance sends whatever is still on the ICS-29 module account to
// the community pool. The fee store is not consulted: those records are dropped
// with the module, and the bank balance is the only value that can still be paid out.
func sweepFeeIBCBalance(ctx context.Context, bank BalanceKeeper, distr CommunityPoolFunder, logger upgradeLogger) error {
	addr := authtypes.NewModuleAddress(feeIBCModuleName)
	balance := bank.GetAllBalances(ctx, addr)
	if balance.IsZero() {
		logger.LogInfo("ICS-29 module account has no balance", types.Upgrades, "address", addr.String())
		return nil
	}
	if err := distr.FundCommunityPool(ctx, balance, addr); err != nil {
		return fmt.Errorf("fund community pool from %s: %w", feeIBCModuleName, err)
	}
	logger.LogInfo("swept ICS-29 module account to the community pool", types.Upgrades,
		"address", addr.String(),
		"amount", balance.String(),
	)
	return nil
}
