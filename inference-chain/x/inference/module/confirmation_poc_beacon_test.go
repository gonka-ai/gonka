package inference

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math"
	"math/rand"
	"testing"

	"cosmossdk.io/core/header"
	upgradetypes "cosmossdk.io/x/upgrade/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"

	blskeeper "github.com/productscience/inference/x/bls/keeper"
	blstypes "github.com/productscience/inference/x/bls/types"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// Block BLS beacon, Step 6: the CPoC trigger draw reads σ of round H-1
// (proposals/block-bls-beacon/block-bls-beacon-vote-extensions-plan.md).

// beaconStubBls is the real x/bls keeper with GetBlockBeacon served from a map.
type beaconStubBls struct {
	types.BlsKeeper
	records map[uint64]blstypes.BeaconRecord
	calls   int
}

func (b *beaconStubBls) GetBlockBeacon(_ sdk.Context, round uint64) (blstypes.BeaconRecord, bool) {
	b.calls++
	r, ok := b.records[round]
	return r, ok
}

type noUpgradePlanned struct{}

func (noUpgradePlanned) GetUpgradePlan(context.Context) (upgradetypes.Plan, error) {
	return upgradetypes.Plan{}, nil
}

func newBeaconDrawKeeper(t *testing.T) (keeper.Keeper, sdk.Context, *beaconStubBls) {
	t.Helper()
	stub := &beaconStubBls{records: map[uint64]blstypes.BeaconRecord{}}
	k, ctx, _ := newMinimalInferenceKeeperWithOpts(t, noopCollateralKeeper{}, minimalKeeperOpts{
		wrapBls: func(real blskeeper.Keeper) types.BlsKeeper {
			stub.BlsKeeper = real
			return stub
		},
		upgrade: noUpgradePlanned{},
	})
	return k, ctx, stub
}

func testSigma(seed byte) []byte {
	s := make([]byte, 48)
	for i := range s {
		s[i] = seed + byte(i)*7
	}
	return s
}

func okRecord(round uint64, sigma []byte) blstypes.BeaconRecord {
	return blstypes.BeaconRecord{Round: round, KeyEpoch: 2, Status: blstypes.BeaconStatus_BEACON_STATUS_OK, Sigma: sigma}
}

func withHash(ctx sdk.Context, hash []byte) sdk.Context {
	return ctx.WithHeaderInfo(header.Info{Hash: hash})
}

// Flag off: exactly the legacy value from the first 8 bytes of HeaderInfo().Hash,
// and the beacon is never read.
func TestCPoCDraw_FlagOffIsLegacyBlockHash(t *testing.T) {
	k, ctx, stub := newBeaconDrawKeeper(t)
	am := AppModule{keeper: k}
	hash := []byte{9, 8, 7, 6, 5, 4, 3, 2, 1, 0}
	ctx = withHash(ctx, hash)

	draw, ok, err := am.confirmationPoCDraw(ctx, 1234, false)
	require.NoError(t, err)
	require.True(t, ok)
	want := calculations.DeterministicFloat(int64(binary.BigEndian.Uint64(hash[:8])), "confirmation_poc_trigger_1234")
	require.True(t, want.Equal(draw.value), "got %s want %s", draw.value, want)
	require.Zero(t, draw.beaconRound)
	require.Empty(t, draw.sigmaSHA256)
	require.Zero(t, stub.calls, "flag off must not read the beacon")

	_, _, err = am.confirmationPoCDraw(withHash(ctx, []byte{1, 2, 3}), 1234, false)
	require.ErrorContains(t, err, "block hash too short")
}

// Flag on, round H-1 OK: the draw is a pure function of σ and H.
func TestCPoCDraw_BeaconOK(t *testing.T) {
	k, ctx, stub := newBeaconDrawKeeper(t)
	am := AppModule{keeper: k}
	const height = int64(1000)
	sigma := testSigma(3)
	stub.records[uint64(height-1)] = okRecord(uint64(height-1), sigma)

	draw, ok, err := am.confirmationPoCDraw(ctx, height, true)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, uint64(height-1), draw.beaconRound)
	sum := sha256.Sum256(sigma)
	require.Equal(t, hex.EncodeToString(sum[:]), draw.sigmaSHA256)

	// Independent recomputation of the documented formula.
	tagged := sha256.Sum256(append([]byte("GONKA_CPOC_DRAW_V1"), sigma...))
	want := calculations.DeterministicFloat(int64(binary.BigEndian.Uint64(tagged[:8])), "confirmation_poc_trigger_1000")
	require.True(t, want.Equal(draw.value), "got %s want %s", draw.value, want)

	// Golden value: changing the derivation is a consensus change.
	require.Equal(t, "0.162828266634471", draw.value.String())

	// Same σ, same height → same value; the height is part of the identifier.
	again, _, _ := am.confirmationPoCDraw(ctx, height, true)
	require.True(t, draw.value.Equal(again.value))
	require.False(t, cpocBeaconDrawValue(sigma, height).Equal(cpocBeaconDrawValue(sigma, height+1)))
	// Domain separation: the draw is not the untagged hash of σ.
	untagged := calculations.DeterministicFloat(int64(binary.BigEndian.Uint64(sum[:8])), "confirmation_poc_trigger_1000")
	require.False(t, untagged.Equal(draw.value))
}

// Flag on, anything but an OK record for exactly H-1: no draw, no error.
func TestCPoCDraw_BeaconMissMeansNoDraw(t *testing.T) {
	const height = int64(500)
	round := uint64(height - 1)
	cases := []struct {
		name    string
		records map[uint64]blstypes.BeaconRecord
	}{
		{"no record", nil},
		{"miss threshold", map[uint64]blstypes.BeaconRecord{round: {Round: round, Status: blstypes.BeaconStatus_BEACON_STATUS_MISS_THRESHOLD, ValidSlots: 12}}},
		{"miss no key", map[uint64]blstypes.BeaconRecord{round: {Round: round, Status: blstypes.BeaconStatus_BEACON_STATUS_MISS_NO_KEY}}},
		{"miss no injection", map[uint64]blstypes.BeaconRecord{round: {Round: round, Status: blstypes.BeaconStatus_BEACON_STATUS_MISS_NO_INJECTION}}},
		{"unspecified status", map[uint64]blstypes.BeaconRecord{round: {Round: round, Sigma: testSigma(1)}}},
		{"ok without sigma", map[uint64]blstypes.BeaconRecord{round: {Round: round, Status: blstypes.BeaconStatus_BEACON_STATUS_OK}}},
		{"ok with short sigma", map[uint64]blstypes.BeaconRecord{round: okRecord(round, testSigma(1)[:47])}},
		{"ok only for H", map[uint64]blstypes.BeaconRecord{round + 1: okRecord(round+1, testSigma(1))}},
		{"ok only for H-2", map[uint64]blstypes.BeaconRecord{round - 1: okRecord(round-1, testSigma(1))}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			k, ctx, stub := newBeaconDrawKeeper(t)
			for r, rec := range tc.records {
				stub.records[r] = rec
			}
			am := AppModule{keeper: k}
			draw, ok, err := am.confirmationPoCDraw(ctx, height, true)
			require.NoError(t, err)
			require.False(t, ok)
			require.Equal(t, cpocDraw{}, draw)
			require.Equal(t, 1, stub.calls)
		})
	}

	t.Run("height below 2", func(t *testing.T) {
		k, ctx, stub := newBeaconDrawKeeper(t)
		am := AppModule{keeper: k}
		for _, h := range []int64{0, 1} {
			_, ok, err := am.confirmationPoCDraw(ctx, h, true)
			require.NoError(t, err)
			require.False(t, ok)
		}
		require.Zero(t, stub.calls)
	})
}

// Grind regression: with the flag on, the proposer of H changing its block
// (and so HeaderInfo().Hash) cannot change the draw. With the flag off it can.
func TestCPoCDraw_BlockHashCannotSteerBeaconDraw(t *testing.T) {
	k, ctx, stub := newBeaconDrawKeeper(t)
	am := AppModule{keeper: k}
	const height = int64(777)
	stub.records[uint64(height-1)] = okRecord(uint64(height-1), testSigma(42))

	rng := rand.New(rand.NewSource(1))
	var beaconValues, hashValues []decimal.Decimal
	for i := 0; i < 50; i++ {
		hash := make([]byte, 32)
		rng.Read(hash)
		c := withHash(ctx, hash)

		d, ok, err := am.confirmationPoCDraw(c, height, true)
		require.NoError(t, err)
		require.True(t, ok)
		beaconValues = append(beaconValues, d.value)

		h, ok, err := am.confirmationPoCDraw(c, height, false)
		require.NoError(t, err)
		require.True(t, ok)
		hashValues = append(hashValues, h.value)
	}
	for _, v := range beaconValues[1:] {
		require.True(t, beaconValues[0].Equal(v), "block hash changed the beacon draw")
	}
	distinct := map[string]struct{}{}
	for _, v := range hashValues {
		distinct[v.String()] = struct{}{}
	}
	require.Greater(t, len(distinct), 40, "sanity: the legacy draw follows the block hash")
}

// Statistical: over 10k uniformly random σ the trigger frequency matches p.
func TestCPoCDraw_BeaconTriggerFrequency(t *testing.T) {
	const n = 10000
	rng := rand.New(rand.NewSource(20261111))
	for _, p := range []float64{0.01, 0.05, 0.3, 0.5} {
		prob := decimal.NewFromFloat(p)
		hits := 0
		for i := 0; i < n; i++ {
			sigma := make([]byte, 48)
			rng.Read(sigma)
			if cpocBeaconDrawValue(sigma, int64(1000+i)).LessThan(prob) {
				hits++
			}
		}
		// 5 standard deviations of the binomial: a false failure is ~1e-6.
		tol := 5 * math.Sqrt(n*p*(1-p))
		require.InDelta(t, n*p, float64(hits), tol, "p=%v hits=%d", p, hits)
	}
}

// End to end through checkConfirmationPoCTrigger: an OK round triggers (p ≥ 1)
// and the event records the beacon round and σ hash; a miss creates nothing.
func TestCPoCTrigger_BeaconModeEvent(t *testing.T) {
	setup := func(t *testing.T) (AppModule, sdk.Context, *beaconStubBls, *types.EpochContext, *types.EpochParams, *types.ConfirmationPoCParams, int64) {
		k, ctx, stub := newBeaconDrawKeeper(t)
		params, err := k.GetParams(ctx)
		require.NoError(t, err)
		params.EpochParams.EpochLength = 400
		params.EpochParams.ConfirmationPocSafetyWindow = 10
		params.ConfirmationPocParams.ExpectedConfirmationsPerEpoch = 100000 // p ≥ 1: the draw always triggers
		params.ConfirmationPocParams.UseBlockBeacon = true
		require.NoError(t, k.SetParams(ctx, params))

		epoch := types.Epoch{Index: 3, PocStartBlockHeight: 1000}
		height := int64(1100)
		ec, err := types.NewEpochContextFromEffectiveEpoch(epoch, *params.EpochParams, height)
		require.NoError(t, err)
		require.Equal(t, types.InferencePhase, ec.GetCurrentPhase(height))
		ctx = ctx.WithBlockHeight(height)
		return AppModule{keeper: k}, ctx, stub, ec, params.EpochParams, params.ConfirmationPocParams, height
	}

	t.Run("ok round triggers with beacon fields", func(t *testing.T) {
		am, ctx, stub, ec, ep, cp, height := setup(t)
		sigma := testSigma(9)
		stub.records[uint64(height-1)] = okRecord(uint64(height-1), sigma)

		require.NoError(t, am.checkConfirmationPoCTrigger(ctx, height, ec, ep, cp, ctx))
		ev, active, err := am.keeper.GetActiveConfirmationPoCEvent(ctx)
		require.NoError(t, err)
		require.True(t, active)
		require.Equal(t, height, ev.TriggerHeight)
		require.Equal(t, uint64(height-1), ev.TriggerBeaconRound)
		sum := sha256.Sum256(sigma)
		require.Equal(t, hex.EncodeToString(sum[:]), ev.TriggerSigmaSha256)
		require.Empty(t, ev.PocSeedBlockHash, "the work seed is still set at GENERATION")
	})

	t.Run("miss creates no event and no error", func(t *testing.T) {
		am, ctx, stub, ec, ep, cp, height := setup(t)
		stub.records[uint64(height-1)] = blstypes.BeaconRecord{Round: uint64(height - 1), Status: blstypes.BeaconStatus_BEACON_STATUS_MISS_THRESHOLD}

		require.NoError(t, am.checkConfirmationPoCTrigger(ctx, height, ec, ep, cp, ctx))
		_, active, err := am.keeper.GetActiveConfirmationPoCEvent(ctx)
		require.NoError(t, err)
		require.False(t, active)
		events, err := am.keeper.GetAllConfirmationPoCEventsForEpoch(ctx, ec.EpochIndex)
		require.NoError(t, err)
		require.Empty(t, events)
	})

	t.Run("flag off ignores the beacon", func(t *testing.T) {
		am, ctx, stub, ec, ep, cp, height := setup(t)
		cp.UseBlockBeacon = false
		require.NoError(t, am.checkConfirmationPoCTrigger(ctx, height, ec, ep, cp, ctx))
		ev, active, err := am.keeper.GetActiveConfirmationPoCEvent(ctx)
		require.NoError(t, err)
		require.True(t, active)
		require.Zero(t, ev.TriggerBeaconRound)
		require.Empty(t, ev.TriggerSigmaSha256)
		require.Zero(t, stub.calls)
	})
}
