package keeper_test

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"testing"

	cosmossdk_math "cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

const (
	maintClaimEpoch     = uint64(100)
	maintClaimEpochPoc  = int64(1000)
	maintClaimClaimSeed = uint64(12345)
	// PoC start of epoch 101 with the mainnet epoch length.
	maintClaimNextPoc = maintClaimEpochPoc + 15391
)

type maintClaimFixture struct {
	k       keeper.Keeper
	ms      types.MsgServer
	ctx     sdk.Context
	addr    sdk.AccAddress
	nextPoc int64
}

// setupMaintenanceClaim prepares epoch 100 starting at block 1000 with ten
// inferences created at the given heights, none of them validated by the
// claimant. Without any exemption the claim is withheld for missed validations.
func setupMaintenanceClaim(t *testing.T, heights []int64) maintClaimFixture {
	t.Helper()
	require.Len(t, heights, 10)
	nextPoc := maintClaimNextPoc
	k, ms, ctx, mocks := setupKeeperWithMocks(t)

	privKey := secp256k1.GenPrivKey()
	seedBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(seedBytes, maintClaimClaimSeed)
	signature, err := privKey.Sign(seedBytes)
	require.NoError(t, err)

	k.SetEpoch(ctx, &types.Epoch{Index: maintClaimEpoch, PocStartBlockHeight: maintClaimEpochPoc})
	k.SetEpoch(ctx, &types.Epoch{Index: maintClaimEpoch + 1, PocStartBlockHeight: nextPoc})
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, maintClaimEpoch+1))

	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:          maintClaimEpoch + 1,
		EpochGroupId:        101,
		PocStartBlockHeight: maintClaimEpoch + 1,
		ValidationWeights:   []*types.ValidationWeight{{MemberAddress: testutil.Creator, Weight: 50}},
	})
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:          maintClaimEpoch,
		EpochGroupId:        9000,
		PocStartBlockHeight: maintClaimEpoch,
		ValidationWeights: []*types.ValidationWeight{
			{MemberAddress: testutil.Creator, Weight: 80},
			{MemberAddress: testutil.Executor, Weight: 10},
			{MemberAddress: testutil.Executor2, Weight: 10},
		},
	})
	require.NoError(t, k.SetSettleAmount(ctx, types.SettleAmount{
		Participant:   testutil.Creator,
		EpochIndex:    maintClaimEpoch,
		WorkCoins:     1000,
		RewardCoins:   500,
		SeedSignature: hex.EncodeToString(signature),
	}))

	addr, err := sdk.AccAddressFromBech32(testutil.Creator)
	require.NoError(t, err)
	require.NoError(t, k.Participants.Set(ctx, addr, types.Participant{
		Index:   testutil.Creator,
		Address: testutil.Creator,
		Status:  types.ParticipantStatus_ACTIVE,
	}))
	for _, epochID := range []uint64{maintClaimEpoch, maintClaimEpoch + 1} {
		k.SetActiveParticipants(ctx, types.ActiveParticipants{
			EpochId:      epochID,
			Participants: []*types.ActiveParticipant{{Index: testutil.Creator}},
		})
	}
	k.SetEpochPerformanceSummary(ctx, types.EpochPerformanceSummary{
		EpochIndex:    maintClaimEpoch,
		ParticipantId: testutil.Creator,
	})

	for i, height := range heights {
		executor := testutil.Executor
		if i%2 == 1 {
			executor = testutil.Executor2
		}
		k.SetInferenceValidationDetails(ctx, types.InferenceValidationDetails{
			EpochId:              maintClaimEpoch,
			InferenceId:          fmt.Sprintf("inference%d", i+1),
			ExecutorId:           executor,
			ExecutorReputation:   int32(i * 10),
			TrafficBasis:         1000,
			CreatedAtBlockHeight: height,
		})
	}

	params := types.DefaultParams()
	params.ValidationParams.MinValidationAverage = types.DecimalFromFloat(0.1)
	params.ValidationParams.MaxValidationAverage = types.DecimalFromFloat(1.0)
	params.ValidationParams.ClaimValidationEnabled = true
	// Mainnet epoch params (GET /productscience/inference/inference/params, 03.11.2026).
	params.EpochParams.EpochLength = 15391
	params.EpochParams.PocStageDuration = 35
	params.EpochParams.PocExchangeDuration = 0
	params.EpochParams.PocValidationDelay = 5
	params.EpochParams.PocValidationDuration = 240
	params.EpochParams.SetNewValidatorsDelay = 120
	params.MaintenanceParams = &types.MaintenanceParams{
		MaintenanceEnabled:                            true,
		MaintenanceMinScheduleLeadBlocks:              100,
		MaintenanceMaxWindowBlocks:                    200,
		MaintenanceMaxConcurrentValidators:            3,
		MaintenanceMaxConcurrentPowerBps:              1000,
		MaintenanceCreditCapBlocks:                    400,
		MaintenanceCreditEarnPerSuccessfulEpochBlocks: 20,
	}
	require.NoError(t, k.SetParams(ctx, params))

	mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), addr).
		Return(authtypes.NewBaseAccount(addr, privKey.PubKey(), 0, 0)).AnyTimes()
	mocks.AccountKeeper.EXPECT().HasAccount(gomock.Any(), gomock.Any()).Return(true).AnyTimes()
	mocks.AuthzKeeper.EXPECT().GranterGrants(gomock.Any(), gomock.Any()).
		Return(&authztypes.QueryGranterGrantsResponse{Grants: []*authztypes.GrantAuthorization{}}, nil).AnyTimes()
	mocks.StakingKeeper.EXPECT().GetValidator(gomock.Any(), gomock.Any()).
		Return(stakingtypes.Validator{}, stakingtypes.ErrNoValidatorFound).AnyTimes()
	mocks.StakingKeeper.EXPECT().GetLastTotalPower(gomock.Any()).
		Return(cosmossdk_math.ZeroInt(), nil).AnyTimes()
	mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, addr, gomock.Any(), gomock.Any()).
		Return(nil).AnyTimes()

	return maintClaimFixture{k: k, ms: ms, ctx: ctx, addr: addr, nextPoc: nextPoc}
}

// runWindow schedules a window for the claimant and runs it to completion
// while epoch 100 is still the effective epoch.
func (f maintClaimFixture) runWindow(t *testing.T, start int64, duration uint64) {
	t.Helper()
	state := f.k.GetOrCreateMaintenanceState(f.ctx, f.addr)
	state.CreditBlocks += duration
	require.NoError(t, f.k.SetMaintenanceState(f.ctx, state))
	_, err := f.ms.ScheduleMaintenance(f.ctx.WithBlockHeight(start-100), &types.MsgScheduleMaintenance{
		Creator: testutil.Creator, Participant: testutil.Creator, StartHeight: start, DurationBlocks: duration,
	})
	require.NoError(t, err)
	require.NoError(t, f.k.SetEffectiveEpochIndex(f.ctx, maintClaimEpoch))
	require.NoError(t, f.k.ProcessMaintenanceTransitions(f.ctx.WithBlockHeight(start)))
	require.NoError(t, f.k.ProcessMaintenanceTransitions(f.ctx.WithBlockHeight(start+int64(duration))))
	require.NoError(t, f.k.SetEffectiveEpochIndex(f.ctx, maintClaimEpoch+1))
}

func (f maintClaimFixture) claim(t *testing.T) *types.MsgClaimRewardsResponse {
	t.Helper()
	resp, err := f.ms.ClaimRewards(f.ctx.WithBlockHeight(f.nextPoc+200), &types.MsgClaimRewards{
		Creator: testutil.Creator, EpochIndex: maintClaimEpoch, Seed: int64(maintClaimClaimSeed),
	})
	require.NoError(t, err)
	return resp
}

func heightsFrom(start, step int64) []int64 {
	heights := make([]int64, 10)
	for i := range heights {
		heights[i] = start + int64(i)*step
	}
	return heights
}

func TestClaimRewards_NoMaintenanceMissedValidationsWithheld(t *testing.T) {
	f := setupMaintenanceClaim(t, heightsFrom(2000, 1000))
	resp := f.claim(t)
	require.Equal(t, "Inference validation missed significantly", resp.Result)
	require.Zero(t, resp.Amount)
}

// A 1-block window on the epoch's last block must not waive the validations
// missed in the rest of the epoch.
func TestClaimRewards_OneBlockMaintenanceDoesNotWaiveEpoch(t *testing.T) {
	f := setupMaintenanceClaim(t, heightsFrom(2000, 1000))
	f.runWindow(t, maintClaimNextPoc-1, 1)
	resp := f.claim(t)
	require.Equal(t, "Inference validation missed significantly", resp.Result)
	require.Zero(t, resp.Amount)
}

// A window placed after the epoch's last block, while the epoch is still
// effective, must not waive the epoch's validations either.
func TestClaimRewards_MaintenanceAfterEpochEndDoesNotWaiveEpoch(t *testing.T) {
	f := setupMaintenanceClaim(t, heightsFrom(2000, 1000))
	f.runWindow(t, maintClaimNextPoc+281, 1)
	resp := f.claim(t)
	require.Equal(t, "Inference validation missed significantly", resp.Result)
	require.Zero(t, resp.Amount)
}

// Inferences created inside a window stay exempt.
func TestClaimRewards_MaintenanceWaivesInferencesInsideWindow(t *testing.T) {
	f := setupMaintenanceClaim(t, heightsFrom(5000, 10))
	f.runWindow(t, 5000, 100)
	resp := f.claim(t)
	require.Equal(t, "Rewards claimed successfully", resp.Result)
	require.Equal(t, uint64(1500), resp.Amount)
}

// Every window of the epoch counts, not only the most recent one.
func TestClaimRewards_MaintenanceWaivesInferencesOfEveryWindow(t *testing.T) {
	heights := append(heightsFrom(3000, 1)[:5], heightsFrom(6000, 1)[:5]...)
	f := setupMaintenanceClaim(t, heights)
	f.runWindow(t, 3000, 10)
	f.runWindow(t, 6000, 10)
	resp := f.claim(t)
	require.Equal(t, "Rewards claimed successfully", resp.Result)
	require.Equal(t, uint64(1500), resp.Amount)
}

// Inferences outside the window still count when another part of the epoch was covered.
func TestClaimRewards_MaintenanceExemptsOnlyWindowPart(t *testing.T) {
	heights := append([]int64{12000}, heightsFrom(2000, 1000)[:9]...)
	f := setupMaintenanceClaim(t, heights)
	f.runWindow(t, 12000, 10)
	resp := f.claim(t)
	require.Equal(t, "Inference validation missed significantly", resp.Result)
	require.Zero(t, resp.Amount)
}
