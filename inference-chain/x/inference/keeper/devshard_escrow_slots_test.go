package keeper_test

import (
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

// mainnetShapeEscrow is a mainnet-shaped escrow: 16 slots over `hosts` distinct
// bech32 hosts, 64-char app hash, model id.
func mainnetShapeEscrow(hosts []string) types.DevshardEscrow {
	slots := make([]string, keeper.DevshardGroupSize)
	for i := range slots {
		slots[i] = hosts[(i*7)%len(hosts)]
	}
	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0xAC
	return types.DevshardEscrow{
		Creator: creator.String(), Amount: 7_000_000_000, Slots: slots, EpochIndex: 5,
		AppHash: "3f1c0e2b9a4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7",
		ModelId: "MiniMaxAI/MiniMax-M2.7", CreateDevshardFee: 1_000_000, FeePerNonce: 1000,
		InferenceSealGraceNonces: 32, InferenceSealGraceSeconds: 60, AutoSealEveryNNonces: 100,
		ValidationRate: 500, VoteThresholdFactor: 2, RefusalTimeout: 60, ExecutionTimeout: 1920,
	}
}

func TestDevshardEscrow_SlotsStoredOncePerHost(t *testing.T) {
	k, _, ctx, _ := setupDevshardEscrowTest(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
	escrow := mainnetShapeEscrow(makeDevshardAddrs(1, 6))
	want := escrow

	legacy := escrow
	legacy.Id = 1
	legacyBytes, err := legacy.Marshal()
	require.NoError(t, err)
	legacyCtx := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	require.NoError(t, k.DevshardEscrows.Set(legacyCtx, 99, legacy))

	c := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	id, err := k.StoreDevshardEscrow(c, &escrow, 1)
	require.NoError(t, err)
	want.Id = id

	raw, err := k.DevshardEscrows.Get(ctx, id)
	require.NoError(t, err)
	require.Nil(t, raw.Slots)
	require.Len(t, raw.SlotHosts, 6)
	require.Len(t, raw.SlotHosts[0], 20)
	require.Len(t, raw.SlotIndex, keeper.DevshardGroupSize)
	require.Empty(t, raw.Creator)
	require.Len(t, raw.CreatorAddr, 20)
	require.Empty(t, raw.AppHash)
	require.Len(t, raw.AppHashRaw, 32)
	rawBytes, err := raw.Marshal()
	require.NoError(t, err)

	compactCtx := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	require.NoError(t, k.DevshardEscrows.Set(compactCtx, 98, raw))
	t.Logf("escrow value %d -> %d B; escrow write %d -> %d gas; StoreDevshardEscrow %d gas",
		len(legacyBytes), len(rawBytes), legacyCtx.GasMeter().GasConsumed(),
		compactCtx.GasMeter().GasConsumed(), c.GasMeter().GasConsumed())

	got, found := k.GetDevshardEscrow(ctx, id)
	require.True(t, found)
	require.Equal(t, want, got)

	resp, err := k.DevshardEscrow(ctx, &types.QueryGetDevshardEscrowRequest{Id: id})
	require.NoError(t, err)
	require.Equal(t, want, *resp.Escrow)
	require.Empty(t, resp.Escrow.SlotHosts)
	require.Empty(t, resp.Escrow.SlotIndex)
	require.Empty(t, resp.Escrow.CreatorAddr)
	require.Empty(t, resp.Escrow.AppHashRaw)
}

func TestDevshardEscrow_DistinctSlotsStoredAsBytes(t *testing.T) {
	k, _, ctx, _ := setupDevshardEscrowTest(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
	escrow := mainnetShapeEscrow(makeDevshardAddrs(1, keeper.DevshardGroupSize))
	escrow.Slots = makeDevshardAddrs(1, keeper.DevshardGroupSize)
	want := escrow
	_, err := k.StoreDevshardEscrow(ctx, &escrow, 1)
	require.NoError(t, err)
	want.Id = 1

	raw, err := k.DevshardEscrows.Get(ctx, 1)
	require.NoError(t, err)
	require.Nil(t, raw.Slots)
	require.Len(t, raw.SlotHosts, keeper.DevshardGroupSize)
	got, found := k.GetDevshardEscrow(ctx, 1)
	require.True(t, found)
	require.Equal(t, want, got)
}

// Strings that would not come back byte for byte stay as they are.
func TestDevshardEscrow_NonCanonicalStringsKept(t *testing.T) {
	k, _, ctx, _ := setupDevshardEscrowTest(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
	hosts := makeDevshardAddrs(1, 6)
	escrow := mainnetShapeEscrow(hosts)
	escrow.Slots[3] = strings.ToUpper(escrow.Slots[3])
	escrow.Creator = strings.ToUpper(escrow.Creator)
	escrow.AppHash = strings.ToUpper(escrow.AppHash)
	want := escrow
	_, err := k.StoreDevshardEscrow(ctx, &escrow, 1)
	require.NoError(t, err)
	want.Id = 1

	raw, err := k.DevshardEscrows.Get(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, want, raw)
	got, found := k.GetDevshardEscrow(ctx, 1)
	require.True(t, found)
	require.Equal(t, want, got)
}

func TestDevshardEscrow_LegacyRecordReadsUnchanged(t *testing.T) {
	k, _, ctx, _ := setupDevshardEscrowTest(t)
	escrow := mainnetShapeEscrow(makeDevshardAddrs(1, 6))
	escrow.Id = 7
	require.NoError(t, k.DevshardEscrows.Set(ctx, 7, escrow))
	got, found := k.GetDevshardEscrow(ctx, 7)
	require.True(t, found)
	require.Equal(t, escrow, got)
}

func TestDevshardEscrow_StorageFieldsRejected(t *testing.T) {
	k, _, ctx, _ := setupDevshardEscrowTest(t)
	escrow := mainnetShapeEscrow(makeDevshardAddrs(1, 6))
	escrow.Id = 1
	escrow.SlotHosts = [][]byte{make([]byte, 20)}
	require.ErrorContains(t, k.SetDevshardEscrow(ctx, escrow), "storage-only")
	escrow.SlotHosts, escrow.SlotIndex = nil, []byte{0}
	_, err := k.StoreDevshardEscrow(ctx, &escrow, 1)
	require.ErrorContains(t, err, "storage-only")
	escrow.SlotIndex, escrow.CreatorAddr = nil, make([]byte, 20)
	require.ErrorContains(t, k.SetDevshardEscrow(ctx, escrow), "storage-only")
	escrow.CreatorAddr, escrow.AppHashRaw = nil, []byte{1}
	require.ErrorContains(t, k.SetDevshardEscrow(ctx, escrow), "storage-only")
}

func TestDevshardEscrow_BadSlotIndexNotFound(t *testing.T) {
	k, _, ctx, _ := setupDevshardEscrowTest(t)
	require.NoError(t, k.DevshardEscrows.Set(ctx, 3, types.DevshardEscrow{
		Id: 3, SlotHosts: [][]byte{make([]byte, 20)}, SlotIndex: []byte{0, 1},
	}))
	_, found := k.GetDevshardEscrow(ctx, 3)
	require.False(t, found)
}

// Settle and pruning see the restored slots: a 6-host escrow settles and pays as before.
func TestSettleDevshardEscrow_CompactSlots(t *testing.T) {
	settle := func(compact bool) storetypes.Gas {
		k, ms, ctx, mocks := setupDevshardEscrowTest(t)
		sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
		hostKeys := make([]*dcrdsecp.PrivateKey, 6)
		hosts := make([]string, len(hostKeys))
		for i := range hostKeys {
			key, err := dcrdsecp.GeneratePrivateKey()
			require.NoError(t, err)
			hostKeys[i] = key
			hosts[i] = cosmosAddressFromDcrdKey(key).String()
			setParticipantForDevshardTest(t, k, ctx, hosts[i])
		}
		require.NoError(t, k.SetEffectiveEpochIndex(ctx, 5))
		setActiveParticipantsForDevshardTest(t, k, ctx, 5, hosts...)

		escrow := mainnetShapeEscrow(hosts)
		escrow.AppHash, escrow.ModelId = "", ""
		keys := make([]*dcrdsecp.PrivateKey, keeper.DevshardGroupSize)
		for i, addr := range escrow.Slots {
			for j, h := range hosts {
				if h == addr {
					keys[i] = hostKeys[j]
				}
			}
		}
		if compact {
			_, err := k.StoreDevshardEscrow(ctx, &escrow, 1)
			require.NoError(t, err)
		} else {
			escrow.Id = 1
			require.NoError(t, k.DevshardEscrows.Set(ctx, 1, escrow))
			require.NoError(t, k.DevshardEscrowCounter.Set(ctx, 1))
		}
		raw, err := k.DevshardEscrows.Get(ctx, 1)
		require.NoError(t, err)
		require.Equal(t, compact, raw.Slots == nil)

		msg := buildSettlementTestData(t, escrow, keys, makeHostStats(keeper.DevshardGroupSize, 100_000_000), 200_000_000)
		mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
		mocks.BankKeeper.EXPECT().LogSubAccountTransaction(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()

		c := keeper.WithTxParamsCache(ctx.WithGasMeter(storetypes.NewInfiniteGasMeter()))
		_, err = ms.SettleDevshardEscrow(c, msg)
		require.NoError(t, err)
		got, found := k.GetDevshardEscrow(ctx, 1)
		require.True(t, found)
		require.True(t, got.Settled)
		require.Equal(t, escrow.Slots, got.Slots)
		return c.GasMeter().GasConsumed()
	}
	legacy, compact := settle(false), settle(true)
	t.Logf("settle gas, 16 slots over 6 hosts: legacy record %d, compact record %d", legacy, compact)
	require.Less(t, compact, legacy)
}
