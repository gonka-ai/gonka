package keeper_test

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"cosmossdk.io/collections"
	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// seedSignedGroup is a model sub-group shaped like mainnet epoch 416
// (25 members, 64-byte seed signatures as lower-case hex).
func seedSignedGroup(members int) types.EpochGroupData {
	egd := types.EpochGroupData{EpochIndex: 416, ModelId: "MiniMaxAI/MiniMax-M2.7", TotalWeight: 40000}
	for i := 0; i < members; i++ {
		h := sha256.Sum256([]byte{byte(i)})
		addr := sdk.AccAddress(h[:20]).String()
		sig := sha256.Sum256(h[:])
		egd.MemberSeedSignatures = append(egd.MemberSeedSignatures, &types.SeedSignature{
			MemberAddress: addr,
			Signature:     hex.EncodeToString(append(sig[:], h[:]...)),
		})
		egd.ValidationWeights = append(egd.ValidationWeights, &types.ValidationWeight{MemberAddress: addr, Weight: 1600, Reputation: 100})
	}
	return egd
}

func cloneGroup(t *testing.T, egd types.EpochGroupData) types.EpochGroupData {
	bz, err := egd.Marshal()
	require.NoError(t, err)
	var out types.EpochGroupData
	require.NoError(t, out.Unmarshal(bz))
	return out
}

func TestEpochGroupData_SeedSignaturesStoredAsBytes(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	want := seedSignedGroup(25)
	key := collections.Join(want.EpochIndex, want.ModelId)

	legacyCtx := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	require.NoError(t, k.EpochGroupDataMap.Set(legacyCtx, key, want))
	legacyBytes, err := want.Marshal()
	require.NoError(t, err)
	readLegacy := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	_, found := k.GetEpochGroupData(readLegacy, want.EpochIndex, want.ModelId)
	require.True(t, found)

	given := cloneGroup(t, want)
	writeCtx := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	k.SetEpochGroupData(writeCtx, given)
	require.Equal(t, want, given, "caller's group data must not change")

	raw, err := k.EpochGroupDataMap.Get(ctx, key)
	require.NoError(t, err)
	for _, s := range raw.MemberSeedSignatures {
		require.Empty(t, s.MemberAddress)
		require.Empty(t, s.Signature)
		require.Len(t, s.MemberAddr, 20)
		require.Len(t, s.SignatureRaw, 64)
	}
	for _, w := range raw.ValidationWeights {
		require.Empty(t, w.MemberAddress)
		require.Len(t, w.MemberAddr, 20)
	}
	rawBytes, err := raw.Marshal()
	require.NoError(t, err)

	readCtx := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	got, found := k.GetEpochGroupData(readCtx, want.EpochIndex, want.ModelId)
	require.True(t, found)
	require.Equal(t, want, got)

	t.Logf("sub-group value %d -> %d B; write %d -> %d gas; read %d -> %d gas",
		len(legacyBytes), len(rawBytes), legacyCtx.GasMeter().GasConsumed(), writeCtx.GasMeter().GasConsumed(),
		readLegacy.GasMeter().GasConsumed(), readCtx.GasMeter().GasConsumed())
	require.Less(t, readCtx.GasMeter().GasConsumed(), readLegacy.GasMeter().GasConsumed())

	all, err := k.EpochGroupDataAll(ctx, &types.QueryAllEpochGroupDataRequest{})
	require.NoError(t, err)
	require.Equal(t, []types.EpochGroupData{want}, all.EpochGroupData)
	require.Equal(t, []types.EpochGroupData{want}, k.GetAllEpochGroupData(ctx))
}

func TestEpochGroupData_TxCachedReadRestoresSeedSignatures(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	want := seedSignedGroup(3)
	k.SetEpochGroupData(ctx, cloneGroup(t, want))
	cached := keeper.WithTxParamsCache(ctx)
	for i := 0; i < 2; i++ {
		got, found := k.GetEpochGroupData(cached, want.EpochIndex, want.ModelId)
		require.True(t, found)
		require.Equal(t, want, got)
	}
}

func TestEpochGroupData_NonCanonicalSeedStringsKept(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	want := seedSignedGroup(3)
	want.MemberSeedSignatures[0].MemberAddress = strings.ToUpper(want.MemberSeedSignatures[0].MemberAddress)
	want.MemberSeedSignatures[1].Signature = strings.ToUpper(want.MemberSeedSignatures[1].Signature)
	want.MemberSeedSignatures[2].Signature = "not-hex"
	want.ValidationWeights[0].MemberAddress = strings.ToUpper(want.ValidationWeights[0].MemberAddress)
	want.ValidationWeights[1].MemberAddress = "not-bech32"
	k.SetEpochGroupData(ctx, cloneGroup(t, want))

	raw, err := k.EpochGroupDataMap.Get(ctx, collections.Join(want.EpochIndex, want.ModelId))
	require.NoError(t, err)
	require.Equal(t, want.MemberSeedSignatures[0].MemberAddress, raw.MemberSeedSignatures[0].MemberAddress)
	require.Equal(t, want.MemberSeedSignatures[1].Signature, raw.MemberSeedSignatures[1].Signature)
	require.Equal(t, "not-hex", raw.MemberSeedSignatures[2].Signature)
	require.Equal(t, want.ValidationWeights[0].MemberAddress, raw.ValidationWeights[0].MemberAddress)
	require.Equal(t, "not-bech32", raw.ValidationWeights[1].MemberAddress)
	require.Empty(t, raw.ValidationWeights[0].MemberAddr)
	require.Len(t, raw.ValidationWeights[2].MemberAddr, 20)

	got, found := k.GetEpochGroupData(ctx, want.EpochIndex, want.ModelId)
	require.True(t, found)
	require.Equal(t, want, got)
}

func TestEpochGroupData_LegacyRecordReadsUnchanged(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	want := seedSignedGroup(4)
	require.NoError(t, k.EpochGroupDataMap.Set(ctx, collections.Join(want.EpochIndex, want.ModelId), want))
	got, found := k.GetEpochGroupData(ctx, want.EpochIndex, want.ModelId)
	require.True(t, found)
	require.Equal(t, want, got)
}

func TestSettleAmount_SeedSignatureStoredAsBytes(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	want := types.SettleAmount{
		Participant:   testutil.Executor,
		RewardCoins:   1000,
		WorkCoins:     500,
		EpochIndex:    416,
		SeedSignature: strings.Repeat("ab", 64),
	}
	writeCtx := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	require.NoError(t, k.SetSettleAmount(writeCtx, want))
	t.Logf("SetSettleAmount gas: %d", writeCtx.GasMeter().GasConsumed())

	raw, err := k.SettleAmounts.Get(ctx, sdk.MustAccAddressFromBech32(testutil.Executor))
	require.NoError(t, err)
	require.Empty(t, raw.SeedSignature)
	require.Equal(t, want.SeedSignature, hex.EncodeToString(raw.SeedSignatureRaw))

	got, found := k.GetSettleAmount(ctx, testutil.Executor)
	require.True(t, found)
	require.Equal(t, want, got)
	require.Equal(t, []types.SettleAmount{want}, k.GetAllSettleAmount(ctx))

	upper := want
	upper.SeedSignature = strings.ToUpper(upper.SeedSignature)
	require.NoError(t, k.SetSettleAmount(ctx, upper))
	got, found = k.GetSettleAmount(ctx, testutil.Executor)
	require.True(t, found)
	require.Equal(t, upper, got)
}
