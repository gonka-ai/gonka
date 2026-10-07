package app_test

import (
	"context"
	"testing"

	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"
	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	mintkeeper "github.com/cosmos/cosmos-sdk/x/mint/keeper"
	minttypes "github.com/cosmos/cosmos-sdk/x/mint/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/app"
	inferencetypes "github.com/productscience/inference/x/inference/types"
)

type mintRun struct {
	gas    storetypes.Gas
	minter minttypes.Minter
	supply math.Int
}

func runMint(t *testing.T, ctx sdk.Context, k *mintkeeper.Keeper, fn mintkeeper.MintFn, bank interface {
	GetSupply(ctx context.Context, denom string) sdk.Coin
}, denom string) mintRun {
	t.Helper()
	cctx, _ := ctx.CacheContext()
	cctx = cctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	require.NoError(t, fn(cctx, k))
	minter, err := k.Minter.Get(cctx)
	require.NoError(t, err)
	return mintRun{gas: cctx.GasMeter().GasConsumed(), minter: minter, supply: bank.GetSupply(cctx, denom).Amount}
}

// The app's MintFn writes the same Minter and supply as the SDK default; with
// InflationMin == InflationMax it skips the bonded-ratio scan of all validators.
func TestGonkaMintFn_MatchesDefault(t *testing.T) {
	testApp := createTestApp(t)
	ctx := testApp.BaseApp.NewUncachedContext(false, cmtproto.Header{ChainID: TallyTestChainID, Height: 2})
	k := &testApp.MintKeeper
	defaultFn := mintkeeper.DefaultMintFn(minttypes.DefaultInflationCalculationFn)

	base, err := k.Params.Get(ctx)
	require.NoError(t, err)
	base.MintDenom = inferencetypes.BaseCoin

	cases := []struct {
		name          string
		max, min, cur math.LegacyDec
		skipsScan     bool
	}{
		{"zeroed since v0.2.14", math.LegacyZeroDec(), math.LegacyZeroDec(), math.LegacyZeroDec(), true},
		{"pinned non-zero", math.LegacyNewDecWithPrec(5, 2), math.LegacyNewDecWithPrec(5, 2), math.LegacyNewDecWithPrec(9, 2), true},
		{"range", math.LegacyNewDecWithPrec(20, 2), math.LegacyNewDecWithPrec(7, 2), math.LegacyNewDecWithPrec(13, 2), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			params := base
			params.InflationMax, params.InflationMin = tc.max, tc.min
			require.NoError(t, k.Params.Set(ctx, params))
			require.NoError(t, k.Minter.Set(ctx, minttypes.NewMinter(tc.cur, math.LegacyZeroDec())))

			want := runMint(t, ctx, k, defaultFn, testApp.BankKeeper, params.MintDenom)
			gotDirect := runMint(t, ctx, k, app.GonkaMintFn(), testApp.BankKeeper, params.MintDenom)
			gotWired := runMint(t, ctx, k, func(ctx sdk.Context, k *mintkeeper.Keeper) error { return k.MintFn(ctx) }, testApp.BankKeeper, params.MintDenom)

			for _, got := range []mintRun{gotDirect, gotWired} {
				require.True(t, want.minter.Inflation.Equal(got.minter.Inflation), "inflation %s vs %s", want.minter.Inflation, got.minter.Inflation)
				require.True(t, want.minter.AnnualProvisions.Equal(got.minter.AnnualProvisions))
				require.True(t, want.supply.Equal(got.supply))
				if tc.skipsScan {
					require.Less(t, got.gas, want.gas)
				} else {
					require.Equal(t, want.gas, got.gas)
				}
			}
		})
	}
}

// With inflation zeroed (mainnet since v0.2.14) an empty block leaves the mint store untouched.
func TestGonkaMintFn_ZeroInflationSkipsMinterWrite(t *testing.T) {
	testApp := createTestApp(t)
	k := &testApp.MintKeeper
	ctx := testApp.BaseApp.NewUncachedContext(false, cmtproto.Header{ChainID: TallyTestChainID, Height: 2})
	params, err := k.Params.Get(ctx)
	require.NoError(t, err)
	params.InflationMax, params.InflationMin, params.InflationRateChange = math.LegacyZeroDec(), math.LegacyZeroDec(), math.LegacyZeroDec()
	require.NoError(t, k.Params.Set(ctx, params))
	require.NoError(t, k.Minter.Set(ctx, minttypes.NewMinter(math.LegacyZeroDec(), math.LegacyZeroDec())))

	mintHash := func() []byte {
		return testApp.CommitMultiStore().GetCommitKVStore(testApp.GetKey(minttypes.StoreKey)).LastCommitID().Hash
	}
	commit := func(h int64) {
		_, err := testApp.FinalizeBlock(&abci.RequestFinalizeBlock{Height: h})
		require.NoError(t, err)
		_, err = testApp.Commit()
		require.NoError(t, err)
	}
	commit(2) // commits the zeroed params
	prev := mintHash()
	for h := int64(3); h <= 5; h++ {
		commit(h)
		require.Equal(t, prev, mintHash(), "mint store written at %d", h)
	}
	minter, err := k.Minter.Get(testApp.BaseApp.NewUncachedContext(false, cmtproto.Header{}))
	require.NoError(t, err)
	require.True(t, minter.Inflation.IsZero())
}
