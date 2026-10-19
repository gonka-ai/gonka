package inference

import (
	"context"
	"math/rand"
	"testing"

	"cosmossdk.io/collections"
	"cosmossdk.io/core/header"
	"cosmossdk.io/math"
	upgradetypes "cosmossdk.io/x/upgrade/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

type quotaTestUpgradeKeeper struct{}

func (quotaTestUpgradeKeeper) GetUpgradePlan(context.Context) (upgradetypes.Plan, error) {
	return upgradetypes.Plan{}, nil
}

// quotaTestStaking serves consensus power by consensus address.
type quotaTestStaking struct {
	power map[string]int64
	total int64
}

func (s quotaTestStaking) SetComputeValidators(context.Context, []stakingkeeper.ComputeResult, bool) ([]stakingtypes.Validator, error) {
	return nil, nil
}

func (s quotaTestStaking) GetAllValidators(context.Context) ([]stakingtypes.Validator, error) {
	return nil, nil
}

func (s quotaTestStaking) GetValidatorByConsAddr(_ context.Context, consAddr sdk.ConsAddress) (stakingtypes.Validator, error) {
	p, ok := s.power[string(consAddr)]
	if !ok {
		return stakingtypes.Validator{}, stakingtypes.ErrNoValidatorFound
	}
	return stakingtypes.Validator{Tokens: sdk.TokensFromConsensusPower(p, sdk.DefaultPowerReduction)}, nil
}

func (s quotaTestStaking) GetValidator(context.Context, sdk.ValAddress) (stakingtypes.Validator, error) {
	return stakingtypes.Validator{}, stakingtypes.ErrNoValidatorFound
}

func (s quotaTestStaking) GetLastTotalPower(context.Context) (math.Int, error) {
	return math.NewInt(s.total), nil
}

// Mainnet validator power shares (17 validators, 18.10.2026), in basis points.
var mainnetPowerBps = []int64{2526, 2100, 833, 833, 833, 667, 647, 523, 475, 421, 36, 26, 25, 22, 13, 10, 10}

type quotaEpochRun struct {
	events int
	idle   int64
}

// runQuotaEpoch drives the real trigger over one mainnet-shaped epoch (epoch 426 params).
// Proposers are drawn by power; on the blocks of validator `attacker` (-1: none) the
// proposer tries up to `tries` block hashes, as a modified proposer could.
func runQuotaEpoch(t *testing.T, seed int64, attacker int, tries int, withStaking bool) quotaEpochRun {
	t.Helper()
	k, ctx, _ := newMinimalInferenceKeeperWithStub(t)
	k.UpgradeKeeper = quotaTestUpgradeKeeper{}
	addrs := make([][]byte, len(mainnetPowerBps))
	staking := quotaTestStaking{power: map[string]int64{}}
	for i, bps := range mainnetPowerBps {
		addrs[i] = []byte{0xC0, byte(i), 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18}
		staking.power[string(addrs[i])] = bps
		staking.total += bps
	}
	if withStaking {
		k.Staking = staking
	}

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	ep := params.EpochParams
	ep.EpochLength = 15391
	ep.PocStageDuration = 35
	ep.PocExchangeDuration = 0
	ep.PocValidationDelay = 5
	ep.PocValidationDuration = 240
	ep.SetNewValidatorsDelay = 120
	ep.InferenceValidationCutoff = 2
	ep.ConfirmationPocSafetyWindow = 500
	params.ConfirmationPocParams.ExpectedConfirmationsPerEpoch = 4
	params.ConfirmationPocParams.UpgradeProtectionWindow = 500
	require.NoError(t, k.SetParams(ctx, params))

	epoch := types.Epoch{Index: 426, PocStartBlockHeight: 6567921}
	require.NoError(t, k.SetEpoch(ctx, &epoch))
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 426))
	am := AppModule{keeper: k}
	ec := types.NewEpochContext(epoch, *ep)
	start, end := ec.SetNewValidators(), ec.NextPoCStart()

	rng := rand.New(rand.NewSource(seed))
	hash := make([]byte, 32)
	for h := start; h < end; h++ {
		if ev, active, _ := k.GetActiveConfirmationPoCEvent(ctx); active {
			// same clear rule as handleConfirmationPoCPhaseTransitions
			if h >= ev.GetValidationEnd(ep)+1+ep.SetNewValidatorsDelay {
				require.NoError(t, k.ClearActiveConfirmationPoCEvent(ctx))
			} else {
				continue
			}
		}
		pick := rng.Int63n(staking.total)
		proposer := 0
		for pick >= mainnetPowerBps[proposer] {
			pick -= mainnetPowerBps[proposer]
			proposer++
		}
		n := 1
		if proposer == attacker {
			n = tries
		}
		ectx, err := types.NewEpochContextFromEffectiveEpoch(epoch, *ep, h)
		require.NoError(t, err)
		for i := 0; i < n; i++ {
			rng.Read(hash)
			c := ctx.WithBlockHeader(cmtproto.Header{Height: h, ProposerAddress: addrs[proposer]}).
				WithHeaderInfo(header.Info{Height: h, Hash: append([]byte(nil), hash...)})
			require.NoError(t, am.checkConfirmationPoCTrigger(c, h, ectx, ep, params.ConfirmationPocParams, c))
			if _, active, _ := k.GetActiveConfirmationPoCEvent(ctx); active {
				break
			}
		}
	}
	evs, err := k.GetAllConfirmationPoCEventsForEpoch(ctx, 426)
	require.NoError(t, err)
	run := quotaEpochRun{events: len(evs)}
	for _, e := range evs {
		run.idle += e.GetValidationEnd(ep) - e.GenerationStartHeight + 1
	}
	return run
}

// A proposer re-rolling its block hash forced an event on nearly every one of its blocks
// once the previous event cleared (~34 per epoch at 4% power); the quota caps that.
func TestConfirmationPoCTrigger_ProposerQuotaBoundsHashGrinding(t *testing.T) {
	for _, attacker := range []int{0, 9} { // 25.3% and 4.2% of power
		run := runQuotaEpoch(t, 7, attacker, 1000, true)
		t.Logf("attacker %d (%d bps): %d events, %d blocks in confirmation PoC", attacker, mainnetPowerBps[attacker], run.events, run.idle)
		require.LessOrEqual(t, run.events, 12)
	}
}

// Without a grinding proposer the quota leaves the epoch's events as they were.
func TestConfirmationPoCTrigger_ProposerQuotaKeepsHonestRate(t *testing.T) {
	with, without := 0, 0
	for seed := int64(0); seed < 20; seed++ {
		with += runQuotaEpoch(t, seed, -1, 1, true).events
		without += runQuotaEpoch(t, seed, -1, 1, false).events
	}
	t.Logf("20 epochs: %d events with the quota, %d without", with, without)
	require.Equal(t, without, with)
}

func TestConfirmationPoCTrigger_ProposerQuotaArithmetic(t *testing.T) {
	k, ctx, _ := newMinimalInferenceKeeperWithStub(t)
	big, small := []byte("big-validator-cons-a"), []byte("small-validator-con-")
	k.Staking = quotaTestStaking{power: map[string]int64{string(big): 2500, string(small): 10}, total: 10000}
	am := AppModule{keeper: k}
	for _, c := range []struct {
		addr  []byte
		quota uint32 // floor(3 * 4 * share) + 2
	}{{big, 5}, {small, 2}} {
		for count := uint32(0); count <= c.quota; count++ {
			if count > 0 {
				require.NoError(t, k.ConfirmationPoCProposerTriggers.Set(ctx, collections.Join(uint64(426), c.addr), count))
			}
			ok, err := am.proposerWithinTriggerQuota(ctx, 426, c.addr, 4)
			require.NoError(t, err)
			require.Equal(t, count < c.quota, ok, "count %d quota %d", count, c.quota)
		}
		// next epoch starts from zero
		ok, err := am.proposerWithinTriggerQuota(ctx, 427, c.addr, 4)
		require.NoError(t, err)
		require.True(t, ok)
	}
	// unknown validator keeps the trigger
	require.NoError(t, k.ConfirmationPoCProposerTriggers.Set(ctx, collections.Join(uint64(426), []byte("unknown")), 100))
	ok, err := am.proposerWithinTriggerQuota(ctx, 426, []byte("unknown"), 4)
	require.NoError(t, err)
	require.True(t, ok)
}
