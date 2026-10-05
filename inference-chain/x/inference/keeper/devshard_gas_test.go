package keeper_test

import (
	"math"
	"testing"

	"cosmossdk.io/collections"

	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	dcrdsecp "github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// readCountMeter counts flat KV read charges (one per Get, hit or miss).
type readCountMeter struct {
	storetypes.GasMeter
	reads int
}

func (m *readCountMeter) ConsumeGas(a storetypes.Gas, d string) {
	if d == storetypes.GasReadCostFlatDesc {
		m.reads++
	}
	m.GasMeter.ConsumeGas(a, d)
}

func TestChallengedAddresses_MatchesIsUnderChallenge(t *testing.T) {
	k, ctx, _ := setupChallengeCreate(t, 500)
	open := types.PoCChallengeState_POC_CHALLENGE_STATE_OPEN
	require.NoError(t, k.SetPoCChallenge(ctx, types.PoCChallenge{EpochIndex: 2, Target: testutil.Executor, State: open}))
	require.NoError(t, k.SetPoCChallenge(ctx, types.PoCChallenge{EpochIndex: 2, Target: testutil.Validator,
		State: types.PoCChallengeState_POC_CHALLENGE_STATE_CHALLENGE_FAILED}))
	// epoch 9 does not exist: no safety window, so not under challenge
	require.NoError(t, k.SetPoCChallenge(ctx, types.PoCChallenge{EpochIndex: 9, Target: testutil.Executor2, State: open}))

	for _, addr := range []string{testutil.Executor, testutil.Validator, testutil.Executor2, testutil.Validator2, "not-an-address"} {
		require.Equal(t, k.IsUnderChallenge(ctx, addr), k.IsChallengedForTesting(ctx, addr), addr)
	}
	require.True(t, k.IsChallengedForTesting(ctx, testutil.Executor))

	late := ctx.WithBlockHeight(1960) // inside the safety window
	require.False(t, k.IsUnderChallenge(late, testutil.Executor))
	require.False(t, k.IsChallengedForTesting(late, testutil.Executor))
}

func createEscrowForGasTest(t *testing.T, members int, challenged bool) (escrow types.DevshardEscrow, reads int) {
	k, ms, ctx, mocks := setupDevshardEscrowTest(t)
	addrs := makeDevshardAddrs(1, members)
	setupEpochGroupForDevshard(ctx, k, 5, "", addrs)
	setupEpochGroupForDevshard(ctx, k, 5, testDevshardModelID, addrs)
	if challenged {
		require.NoError(t, k.SetPoCChallenge(ctx, types.PoCChallenge{EpochIndex: 5, Target: addrs[0],
			State: types.PoCChallengeState_POC_CHALLENGE_STATE_OPEN}))
		require.True(t, k.IsUnderChallenge(ctx, addrs[0]))
	}
	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0xFF
	mocks.BankKeeper.EXPECT().
		SendCoinsFromAccountToModule(gomock.Any(), creator, types.ModuleName, gomock.Any(), gomock.Any()).
		Return(nil)

	m := &readCountMeter{GasMeter: storetypes.NewInfiniteGasMeter()}
	resp, err := ms.CreateDevshardEscrow(ctx.WithGasMeter(m), &types.MsgCreateDevshardEscrow{
		Creator: creator.String(),
		Amount:  7_000_000_000,
		ModelId: testDevshardModelID,
	})
	require.NoError(t, err)
	escrow, found := k.GetDevshardEscrow(ctx, resp.EscrowId)
	require.True(t, found)
	require.Equal(t, uint64(1), k.GetDevshardEscrowEpochCount(ctx, 5))
	return escrow, m.reads
}

func TestCreateDevshardEscrow_SkipsChallengedMember(t *testing.T) {
	escrow, _ := createEscrowForGasTest(t, 2, true)
	addrs := makeDevshardAddrs(1, 2)
	require.NotEmpty(t, escrow.Slots)
	for _, s := range escrow.Slots {
		require.Equal(t, addrs[1], s)
	}
}

func TestCreateDevshardEscrow_ReadsDoNotGrowWithGroupSize(t *testing.T) {
	_, small := createEscrowForGasTest(t, 20, false)
	_, large := createEscrowForGasTest(t, 150, false)
	require.Equal(t, small, large, "challenge check must not read once per member")
}

func TestSetParticipantFromStored_MatchesSetParticipant(t *testing.T) {
	stats := &types.CurrentEpochStats{InferenceCount: 4, EarnedCoins: 10, ValidatedInferences: 3}
	run := func(fromStored bool) (types.Participant, int) {
		k, ms, ctx, _ := setupDevshardEscrowTest(t)
		_ = ms
		p := types.Participant{Index: testutil.Executor, Address: testutil.Executor,
			Status: types.ParticipantStatus_ACTIVE, CurrentEpochStats: stats}
		require.NoError(t, k.SetParticipant(ctx, p))
		read, found := k.GetParticipant(ctx, testutil.Executor)
		require.True(t, found)
		stored := *read.CurrentEpochStats
		read.CoinBalance += 7
		read.CurrentEpochStats.EarnedCoins += 7
		read.CurrentEpochStats.InferenceCount++

		m := &readCountMeter{GasMeter: storetypes.NewInfiniteGasMeter()}
		c := ctx.WithGasMeter(m)
		if fromStored {
			require.NoError(t, k.SetParticipantFromStored(c, read, &stored))
		} else {
			require.NoError(t, k.SetParticipant(c, read))
		}
		got, found := k.GetParticipant(ctx, testutil.Executor)
		require.True(t, found)
		return got, m.reads
	}
	plain, plainReads := run(false)
	stored, storedReads := run(true)
	require.Equal(t, plain, stored)
	require.Equal(t, plainReads-1, storedReads, "the participant is not read again")
}

func settleWithRepeatedSlots(t *testing.T, existing *types.DevshardHostEpochStats) (keeper.Keeper, sdk.Context, []string, error, int) {
	return settleSlots(t, existing, []int{0, 1, 0, 0})
}

// settleSlots settles an escrow whose slot i belongs to host hosts[i] (of two).
func settleSlots(t *testing.T, existing *types.DevshardHostEpochStats, hosts []int) (keeper.Keeper, sdk.Context, []string, error, int) {
	k, ms, ctx, mocks := setupDevshardEscrowTest(t)
	keys, addrs := generateDevshardKeys(t, 2)
	for _, a := range addrs {
		setParticipantForDevshardTest(t, k, ctx, a)
	}
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 5))
	setActiveParticipantsForDevshardTest(t, k, ctx, 5, addrs...)
	if existing != nil {
		existing.Participant = addrs[0]
		existing.EpochIndex = 5
		require.NoError(t, k.DevshardHostEpochStatsMap.Set(ctx,
			collections.Join(uint64(5), sdk.MustAccAddressFromBech32(addrs[0])), *existing))
	}
	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0x11
	slots := make([]string, len(hosts))
	slotKeys := make([]*dcrdsecp.PrivateKey, len(hosts))
	for i, h := range hosts {
		slots[i], slotKeys[i] = addrs[h], keys[h]
	}
	escrow := types.DevshardEscrow{Id: 1, Creator: creator.String(), Amount: 1_000_000, Slots: slots, EpochIndex: 5}
	_, err := k.StoreDevshardEscrow(ctx, &escrow, 1)
	require.NoError(t, err)
	hostStats := []*types.DevshardSettlementHostStats{
		{SlotId: 0, Cost: 10, RequiredValidations: 10, CompletedValidations: 9},
		{SlotId: 1, Cost: 20, RequiredValidations: 7, CompletedValidations: 7},
		{SlotId: 2, Cost: 30, RequiredValidations: 5, CompletedValidations: 4},
		{SlotId: 3, Cost: 40, RequiredValidations: 3, CompletedValidations: 3},
	}[:len(hosts)]
	msg := buildSettlementTestData(t, escrow, slotKeys, hostStats, 0)
	mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil).AnyTimes()
	mocks.BankKeeper.EXPECT().LogSubAccountTransaction(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
	m := &readCountMeter{GasMeter: storetypes.NewInfiniteGasMeter()}
	_, err = ms.SettleDevshardEscrow(ctx.WithGasMeter(m), msg)
	return k, ctx, addrs, err, m.reads
}

func TestSettleDevshardEscrow_HostEpochStatsSumRepeatedSlots(t *testing.T) {
	k, ctx, addrs, err, _ := settleWithRepeatedSlots(t, &types.DevshardHostEpochStats{
		Cost: 1, RequiredValidations: 2, CompletedValidations: 2, EscrowCount: 3,
	})
	require.NoError(t, err)
	h0, found := k.GetDevshardHostEpochStats(ctx, 5, sdk.MustAccAddressFromBech32(addrs[0]))
	require.True(t, found)
	require.Equal(t, uint64(1+10+30+40), h0.Cost)
	require.Equal(t, uint32(2+10+5+3), h0.RequiredValidations)
	require.Equal(t, uint32(2+9+4+3), h0.CompletedValidations)
	require.Equal(t, uint32(4), h0.EscrowCount, "one escrow, however many slots")
	h1, found := k.GetDevshardHostEpochStats(ctx, 5, sdk.MustAccAddressFromBech32(addrs[1]))
	require.True(t, found)
	require.Equal(t, uint64(20), h1.Cost)
	require.Equal(t, uint32(7), h1.RequiredValidations)
	require.Equal(t, uint32(1), h1.EscrowCount)
}

func TestSettleDevshardEscrow_HostEpochStatsOverflowAcrossSlots(t *testing.T) {
	// Each slot fits on its own; the three slots together do not.
	_, _, _, err, _ := settleWithRepeatedSlots(t, &types.DevshardHostEpochStats{RequiredValidations: math.MaxUint32 - 15})
	require.ErrorContains(t, err, "required validations overflow")
}

func TestSettleDevshardEscrow_RepeatedSlotsAddNoReads(t *testing.T) {
	_, _, _, err, twoSlots := settleSlots(t, nil, []int{0, 1})
	require.NoError(t, err)
	_, _, _, err, fourSlots := settleSlots(t, nil, []int{0, 1, 0, 0})
	require.NoError(t, err)
	// Before: each repeated slot read its host's epoch stats and challenge record again,
	// and SetParticipant read every participant a second time.
	require.Equal(t, twoSlots, fourSlots)
}
