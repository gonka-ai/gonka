package keeper_test

import (
	"fmt"
	"strings"
	"testing"

	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	dcrdsecp "github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// settleWithApprovedVersions runs one settlement tagged settlementVersion
// against the given allowlist and returns the gas it consumed.
func settleWithApprovedVersions(t *testing.T, approved []types.DevshardApprovedVersion) (storetypes.Gas, error) {
	t.Helper()
	k, ms, ctx, mocks := setupDevshardEscrowTest(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
	keys := make([]*dcrdsecp.PrivateKey, keeper.DevshardGroupSize)
	slots := make([]string, keeper.DevshardGroupSize)
	for i := range keys {
		key, err := dcrdsecp.GeneratePrivateKey()
		require.NoError(t, err)
		keys[i] = key
		slots[i] = cosmosAddressFromDcrdKey(key).String()
		setParticipantForDevshardTest(t, k, ctx, slots[i])
	}
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 5))
	setActiveParticipantsForDevshardTest(t, k, ctx, 5, slots...)
	for _, v := range approved {
		require.NoError(t, k.SetApprovedVersion(ctx, v))
	}

	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0xAC
	escrow := types.DevshardEscrow{Id: 1, Creator: creator.String(), Amount: 7_000_000_000, Slots: slots, EpochIndex: 5}
	_, err := k.StoreDevshardEscrow(ctx, &escrow, 1)
	require.NoError(t, err)
	msg := buildSettlementTestData(t, escrow, keys, makeHostStats(keeper.DevshardGroupSize, 100_000_000), 200_000_000)
	mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
	mocks.BankKeeper.EXPECT().LogSubAccountTransaction(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()

	c := keeper.WithTxParamsCache(ctx.WithGasMeter(storetypes.NewInfiniteGasMeter()))
	_, err = ms.SettleDevshardEscrow(c, msg)
	return c.GasMeter().GasConsumed(), err
}

// mainnetLikeVersion mirrors the shape of mainnet entries (binary URL ~90 chars, hex sha256).
func mainnetLikeVersion(name string) types.DevshardApprovedVersion {
	return types.DevshardApprovedVersion{
		Name:   name,
		Binary: "https://github.com/gonka-ai/gonka/releases/download/devshard-" + name + "/devshardd-amd64.zip",
		Sha256: strings.Repeat("ab", 32),
	}
}

// The allowlist check looks up the settlement's own version: other approved versions cost nothing.
func TestSettleDevshardEscrow_GasIndependentOfOtherApprovedVersions(t *testing.T) {
	gasWith := func(others int) storetypes.Gas {
		approved := []types.DevshardApprovedVersion{mainnetLikeVersion(settlementVersion)}
		for i := 0; i < others; i++ {
			approved = append(approved, mainnetLikeVersion(fmt.Sprintf("v%d", i+4)))
		}
		gas, err := settleWithApprovedVersions(t, approved)
		require.NoError(t, err)
		return gas
	}
	alone, mainnet, more := gasWith(0), gasWith(1), gasWith(5)
	t.Logf("settle gas: own version only %d, +1 other (mainnet) %d, +5 others %d", alone, mainnet, more)
	require.Equal(t, alone, mainnet)
	require.Equal(t, alone, more)
}

func TestSettleDevshardEscrow_VersionAllowlistSemantics(t *testing.T) {
	_, err := settleWithApprovedVersions(t, nil)
	require.NoError(t, err, "empty allowlist is permissive")

	_, err = settleWithApprovedVersions(t, []types.DevshardApprovedVersion{mainnetLikeVersion("v4.1"), mainnetLikeVersion("v5")})
	require.ErrorContains(t, err, "not listed in approved devshard versions")

	_, err = settleWithApprovedVersions(t, []types.DevshardApprovedVersion{mainnetLikeVersion("v5"), mainnetLikeVersion(settlementVersion)})
	require.NoError(t, err)
}
