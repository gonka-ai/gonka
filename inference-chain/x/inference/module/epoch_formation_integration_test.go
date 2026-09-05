package inference

import (
	"context"
	"testing"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func TestOnEndOfPoCValidationStage_ConcentrationCapsFinalTrustWeight(t *testing.T) {
	k, ctx, _ := newMinimalInferenceKeeperWithStub(t)
	am := NewAppModule(nil, k, nil, nil, nil, nil)

	const (
		currentEpoch  = uint64(1)
		upcomingEpoch = uint64(2)
		modelID       = "model-a"
	)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams.Models = []*types.PoCModelConfig{
		{ModelId: modelID, WeightScaleFactor: types.DecimalFromFloat(1)},
	}
	params.DelegationParams = &types.DelegationParams{
		InitialModelId: modelID,
		WThreshold:     types.DecimalFromFloat(0),
		VMin:           0,
		CapFactor:      types.DecimalFromFloat(0.5),
	}
	params.CollateralParams.GracePeriodEndEpoch = upcomingEpoch
	require.NoError(t, k.SetParams(ctx, params))

	genesisParams := types.DefaultGenesisOnlyParams()
	genesisParams.MaxIndividualPowerPercentage = types.DecimalFromFloat(0.30)
	require.NoError(t, k.SetGenesisOnlyParams(ctx, &genesisParams))

	require.NoError(t, k.SetEffectiveEpochIndex(ctx, currentEpoch))
	require.NoError(t, k.SetEpoch(ctx, &types.Epoch{
		Index:               currentEpoch,
		PocStartBlockHeight: 100,
	}))
	require.NoError(t, k.SetEpoch(ctx, &types.Epoch{
		Index:               upcomingEpoch,
		PocStartBlockHeight: 200,
	}))
	k.SetModel(ctx, &types.Model{Id: modelID, ProposedBy: "genesis"})

	fixtures := []struct {
		address            string
		weight             int64
		confirmationWeight int64
		nodeID             string
	}{
		{
			address:            testutil.Validator,
			weight:             400,
			confirmationWeight: 400,
			nodeID:             "node-a",
		},
		{
			address:            testutil.Validator2,
			weight:             350,
			confirmationWeight: 70,
			nodeID:             "node-b",
		},
		{
			address:            testutil.Executor,
			weight:             250,
			confirmationWeight: 0,
			nodeID:             "node-c",
		},
	}

	rootWeights := make([]*types.ValidationWeight, 0, len(fixtures))
	modelWeights := make([]*types.ValidationWeight, 0, len(fixtures))
	for _, fixture := range fixtures {
		rootWeights = append(rootWeights, &types.ValidationWeight{
			MemberAddress:      fixture.address,
			Weight:             fixture.weight,
			ConfirmationWeight: fixture.confirmationWeight,
		})
		modelWeights = append(modelWeights, &types.ValidationWeight{
			MemberAddress: fixture.address,
			Weight:        fixture.weight,
			MlNodes: []*types.MLNodeInfo{
				{NodeId: fixture.nodeID, PocWeight: fixture.weight},
			},
		})
		require.NoError(t, k.SetParticipant(ctx, types.Participant{
			Index:        fixture.address,
			Address:      fixture.address,
			Status:       types.ParticipantStatus_ACTIVE,
			ValidatorKey: "validator-key-" + fixture.address,
			InferenceUrl: "http://" + fixture.address,
		}))
		require.NoError(t, k.SetRandomSeed(ctx, types.RandomSeed{
			Participant: fixture.address,
			EpochIndex:  currentEpoch,
			Signature:   "seed-" + fixture.address,
		}))
	}

	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:          currentEpoch,
		EpochGroupId:        77,
		PocStartBlockHeight: 100,
		SubGroupModels:      []string{modelID},
		ValidationWeights:   rootWeights,
		ConfirmationWeightScales: []*types.ConfirmationWeightScale{
			{
				ModelId:           modelID,
				WeightScaleFactor: types.DecimalFromFloat(1),
			},
		},
	})
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:        currentEpoch,
		EpochGroupId:      78,
		ModelId:           modelID,
		ValidationWeights: modelWeights,
	})

	require.NoError(t, am.onEndOfPoCValidationStage(ctx, 250, 1_000))

	stored, found := k.GetActiveParticipants(ctx, upcomingEpoch)
	require.True(t, found)
	require.True(t, stored.CapWeightApplied)

	realWeights := make(map[string]int64, len(stored.Participants))
	trustWeights := make(map[string]int64, len(stored.Participants))
	for _, participant := range stored.Participants {
		realWeights[participant.Index] = participant.Weight
		trustWeights[participant.Index] = participant.CapWeight
	}

	require.Equal(t, map[string]int64{
		testutil.Validator:  400,
		testutil.Validator2: 350,
		testutil.Executor:   250,
	}, realWeights)
	require.Equal(t, map[string]int64{
		testutil.Validator:  70,
		testutil.Validator2: 70,
		testutil.Executor:   0,
	}, trustWeights)
}

func TestMissRateResetFallbackPolicy(t *testing.T) {
	k, ctx, _ := newMinimalInferenceKeeperWithStub(t)
	am := NewAppModule(nil, k, nil, nil, nil, nil)
	previous := &previousConfirmedWeights{
		epochIndex:  1,
		weights:     map[string]int64{"alice": 40, "bob": 60},
		totalWeight: 100,
	}
	reset := zeroFailedMissRateWeights(previous, map[string]struct{}{"alice": {}})

	normal := am.applyPreviousConfirmedWeightCap(ctx, []*types.ActiveParticipant{
		{Index: "alice", Weight: 40},
		{Index: "bob", Weight: 60},
	}, reset)
	require.False(t, am.applyZeroTrustFallback(ctx, 2, normal))
	require.Equal(t, int64(0), normal[0].CapWeight)
	require.Equal(t, int64(60), normal[1].CapWeight)

	fallback := am.applyPreviousConfirmedWeightCap(ctx, []*types.ActiveParticipant{
		{Index: "alice", Weight: 40},
	}, previous)
	require.Equal(t, int64(40), fallback[0].CapWeight)

	allReset := zeroFailedMissRateWeights(previous, map[string]struct{}{
		"alice": {},
		"bob":   {},
	})
	recovered := am.applyPreviousConfirmedWeightCap(ctx, []*types.ActiveParticipant{
		{Index: "alice", Weight: 40},
		{Index: "bob", Weight: 60},
	}, allReset)
	require.True(t, am.applyZeroTrustFallback(ctx, 2, recovered))
	require.Equal(t, int64(40), recovered[0].CapWeight)
	require.Equal(t, int64(60), recovered[1].CapWeight)
}

type formationAccountKeeper struct {
	accounts map[string]sdk.AccountI
}

func newFormationAccountKeeper(t *testing.T, addresses ...string) *formationAccountKeeper {
	t.Helper()
	accounts := make(map[string]sdk.AccountI, len(addresses))
	for _, address := range addresses {
		accAddress, err := sdk.AccAddressFromBech32(address)
		require.NoError(t, err)
		accounts[address] = authtypes.NewBaseAccount(accAddress, secp256k1.GenPrivKey().PubKey(), 0, 0)
	}
	return &formationAccountKeeper{accounts: accounts}
}

func (k *formationAccountKeeper) HasAccount(_ context.Context, address sdk.AccAddress) bool {
	return k.accounts[address.String()] != nil
}

func (k *formationAccountKeeper) GetAccount(_ context.Context, address sdk.AccAddress) sdk.AccountI {
	return k.accounts[address.String()]
}

func (*formationAccountKeeper) GetModuleAddress(string) sdk.AccAddress {
	return nil
}

func (k *formationAccountKeeper) SetAccount(_ context.Context, account sdk.AccountI) {
	k.accounts[account.GetAddress().String()] = account
}

func (*formationAccountKeeper) NewAccountWithAddress(_ context.Context, address sdk.AccAddress) sdk.AccountI {
	return authtypes.NewBaseAccountWithAddress(address)
}

func (*formationAccountKeeper) GetModuleAccount(context.Context, string) sdk.ModuleAccountI {
	return nil
}

type selectiveCollateralKeeper struct {
	noopCollateralKeeper
	deposits map[string]sdk.Coin
}

func (k selectiveCollateralKeeper) GetCollateral(_ context.Context, address sdk.AccAddress) (sdk.Coin, bool) {
	collateral, found := k.deposits[address.String()]
	return collateral, found
}

type formationRecoveryFixture struct {
	keeper        keeper.Keeper
	ctx           sdk.Context
	groupStub     *stubGroupKeeper
	module        AppModule
	currentEpoch  types.Epoch
	upcomingEpoch types.Epoch
	modelID       string
	veteran       string
	delegator     string
	newcomer      string
}

func newFormationRecoveryFixture(
	t *testing.T,
	collateralKeeper types.CollateralKeeper,
	vMin int64,
	gracePeriodEnd uint64,
) formationRecoveryFixture {
	t.Helper()

	k, ctx, groupStub := newMinimalInferenceKeeperWithCollateral(t, collateralKeeper)
	currentEpoch := types.Epoch{Index: 1, PocStartBlockHeight: 100}
	upcomingEpoch := types.Epoch{Index: 2, PocStartBlockHeight: 200}
	modelID := "model-a"
	veteran := testutil.Validator
	delegator := testutil.Validator2
	newcomer := testutil.Executor

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams.Models = []*types.PoCModelConfig{{
		ModelId:           modelID,
		WeightScaleFactor: types.DecimalFromFloat(1),
	}}
	params.PocParams.ValidationSlots = 0
	params.DelegationParams = &types.DelegationParams{
		InitialModelId:  modelID,
		WThreshold:      types.DecimalFromFloat(0),
		VMin:            vMin,
		CapFactor:       types.DecimalFromFloat(1),
		DelegationShare: types.DecimalFromFloat(0.1),
	}
	params.CollateralParams.GracePeriodEndEpoch = gracePeriodEnd
	params.CollateralParams.BaseWeightRatio = types.DecimalFromFloat(0)
	params.CollateralParams.CollateralPerWeightUnit = types.DecimalFromFloat(1)
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, k.SetEffectiveEpochIndex(ctx, currentEpoch.Index))
	require.NoError(t, k.SetEpoch(ctx, &currentEpoch))
	require.NoError(t, k.SetEpoch(ctx, &upcomingEpoch))
	k.SetModel(ctx, &types.Model{Id: modelID, ProposedBy: "genesis"})

	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:          currentEpoch.Index,
		EpochGroupId:        77,
		PocStartBlockHeight: uint64(currentEpoch.PocStartBlockHeight),
		SubGroupModels:      []string{modelID},
		ValidationWeights: []*types.ValidationWeight{
			{MemberAddress: veteran, Weight: 100},
			{MemberAddress: delegator, Weight: 49},
		},
	})
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:   currentEpoch.Index,
		EpochGroupId: 78,
		ModelId:      modelID,
		ValidationWeights: []*types.ValidationWeight{{
			MemberAddress: veteran,
			Weight:        100,
			VotingPower:   100,
			MlNodes: []*types.MLNodeInfo{{
				NodeId:    "veteran-node",
				PocWeight: 100,
			}},
		}},
	})
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:          upcomingEpoch.Index,
		EpochGroupId:        79,
		PocStartBlockHeight: uint64(upcomingEpoch.PocStartBlockHeight),
		SubGroupModels:      []string{modelID},
	})

	for _, participant := range []string{veteran, delegator, newcomer} {
		require.NoError(t, k.SetParticipant(ctx, types.Participant{
			Index:             participant,
			Address:           participant,
			Status:            types.ParticipantStatus_ACTIVE,
			ValidatorKey:      "validator-key-" + participant,
			InferenceUrl:      "http://" + participant,
			CurrentEpochStats: types.NewCurrentEpochStats(),
		}))
		nodeID := participant + "-node"
		if participant == veteran {
			nodeID = "veteran-node"
		}
		require.NoError(t, k.SetHardwareNodes(ctx, &types.HardwareNodes{
			Participant: participant,
			HardwareNodes: []*types.HardwareNode{{
				LocalId: nodeID,
				Models:  []string{modelID},
			}},
		}))
	}
	require.NoError(t, k.SetRandomSeed(ctx, types.RandomSeed{
		Participant: veteran,
		EpochIndex:  currentEpoch.Index,
		Signature:   "veteran-current-seed",
	}))
	require.NoError(t, k.SetRandomSeed(ctx, types.RandomSeed{
		Participant: veteran,
		EpochIndex:  upcomingEpoch.Index,
		Signature:   "veteran-upcoming-seed",
	}))
	require.NoError(t, k.SetRandomSeed(ctx, types.RandomSeed{
		Participant: delegator,
		EpochIndex:  currentEpoch.Index,
		Signature:   "delegator-current-seed",
	}))
	require.NoError(t, k.SetRandomSeed(ctx, types.RandomSeed{
		Participant: newcomer,
		EpochIndex:  upcomingEpoch.Index,
		Signature:   "newcomer-seed",
	}))
	require.NoError(t, k.SetDelegationSnapshot(ctx, types.DelegationSnapshot{
		SnapshotHeight: upcomingEpoch.PocStartBlockHeight,
		Delegations: []*types.PoCDelegation{{
			ModelId:    modelID,
			Delegator:  delegator,
			DelegateTo: veteran,
		}},
	}))

	accounts := newFormationAccountKeeper(t, veteran, delegator, newcomer)
	return formationRecoveryFixture{
		keeper:        k,
		ctx:           ctx,
		groupStub:     groupStub,
		module:        NewAppModule(nil, k, accounts, nil, nil, collateralKeeper),
		currentEpoch:  currentEpoch,
		upcomingEpoch: upcomingEpoch,
		modelID:       modelID,
		veteran:       veteran,
		delegator:     delegator,
		newcomer:      newcomer,
	}
}

func (f formationRecoveryFixture) addFreshPoC(t *testing.T, weight int64) {
	t.Helper()
	require.NoError(t, f.keeper.SetPoCV2StoreCommit(f.ctx, types.PoCV2StoreCommit{
		ParticipantAddress:       f.newcomer,
		PocStageStartBlockHeight: f.upcomingEpoch.PocStartBlockHeight,
		Count:                    uint32(weight),
		ModelId:                  f.modelID,
	}))
	require.NoError(t, f.keeper.SetMLNodeWeightDistribution(f.ctx, types.MLNodeWeightDistribution{
		ParticipantAddress:       f.newcomer,
		PocStageStartBlockHeight: f.upcomingEpoch.PocStartBlockHeight,
		ModelId:                  f.modelID,
		Weights: []*types.MLNodeWeight{{
			NodeId: f.newcomer + "-node",
			Weight: uint32(weight),
		}},
	}))
	require.NoError(t, f.keeper.SetPocValidationV2(f.ctx, types.PoCValidationV2{
		ParticipantAddress:          f.newcomer,
		ValidatorParticipantAddress: f.veteran,
		PocStageStartBlockHeight:    f.upcomingEpoch.PocStartBlockHeight,
		ValidatedWeight:             weight,
		ModelId:                     f.modelID,
	}))
	require.NoError(t, f.keeper.SetPoCValidationSnapshot(f.ctx, types.PoCValidationSnapshot{
		PocStageStartHeight: f.upcomingEpoch.PocStartBlockHeight,
		SnapshotHeight:      f.upcomingEpoch.PocStartBlockHeight,
		TotalNetworkWeight:  149,
		ModelVotingPowers: []*types.ModelVotingPowers{{
			ModelId: f.modelID,
			VotingPowers: []*types.VotingPowerEntry{{
				Address:     f.veteran,
				VotingPower: 100,
			}},
		}},
	}))
}

func fallbackEventReason(ctx sdk.Context) (string, bool) {
	for _, event := range ctx.EventManager().Events() {
		if event.Type != "empty_epoch_fallback_applied" {
			continue
		}
		for _, attribute := range event.Attributes {
			if attribute.Key == "reason" {
				return attribute.Value, true
			}
		}
	}
	return "", false
}

func requireNoFormationOutput(t *testing.T, fixture formationRecoveryFixture) {
	t.Helper()
	_, found := fixture.keeper.GetActiveParticipants(fixture.ctx, fixture.upcomingEpoch.Index)
	require.False(t, found)
	_, found = fixture.keeper.GetDelegationRewardTransferSnapshot(fixture.ctx)
	require.False(t, found)
	root, found := fixture.keeper.GetEpochGroupData(fixture.ctx, fixture.upcomingEpoch.Index, "")
	require.True(t, found)
	require.Empty(t, root.ValidationWeights)
	_, found = fallbackEventReason(fixture.ctx)
	require.False(t, found)
	for _, event := range fixture.ctx.EventManager().Events() {
		require.NotEqual(t, "epoch_error", event.Type)
	}
}

func TestOnEndOfPoCValidationStage_RecoversZeroEligibilityWeight(t *testing.T) {
	fixture := newFormationRecoveryFixture(t, noopCollateralKeeper{}, 1, 2)
	fixture.addFreshPoC(t, 100)

	require.NoError(t, fixture.module.onEndOfPoCValidationStage(fixture.ctx, 250, 1_000))

	stored, found := fixture.keeper.GetActiveParticipants(fixture.ctx, fixture.upcomingEpoch.Index)
	require.True(t, found)
	require.Len(t, stored.Participants, 1)
	require.Equal(t, fixture.veteran, stored.Participants[0].Index)
	require.Positive(t, stored.Participants[0].Weight)

	rewardSnapshot, found := fixture.keeper.GetDelegationRewardTransferSnapshot(fixture.ctx)
	require.True(t, found)
	require.Equal(t, fixture.upcomingEpoch.Index, rewardSnapshot.EpochIndex)
	for _, penalty := range rewardSnapshot.Penalties {
		require.Equal(t, fixture.veteran, penalty.Participant)
	}
	require.Len(t, rewardSnapshot.Transfers, 1)
	require.Equal(t, fixture.modelID, rewardSnapshot.Transfers[0].ModelId)
	require.Equal(t, fixture.delegator, rewardSnapshot.Transfers[0].From)
	require.Equal(t, fixture.veteran, rewardSnapshot.Transfers[0].To)
	require.True(t, rewardSnapshot.Transfers[0].Share.ToDecimal().Equal(types.DecimalFromFloat(0.1).ToDecimal()))

	root, found := fixture.keeper.GetEpochGroupData(fixture.ctx, fixture.upcomingEpoch.Index, "")
	require.True(t, found)
	require.NotEmpty(t, root.ValidationWeights)
	subgroup, found := fixture.keeper.GetEpochGroupData(fixture.ctx, fixture.upcomingEpoch.Index, fixture.modelID)
	require.True(t, found)
	require.NotEmpty(t, subgroup.ValidationWeights)
	require.Positive(t, fixture.groupStub.memberUpdatesByGroup[root.EpochGroupId])
	_, err := fixture.keeper.BlsKeeper.GetEpochBLSData(fixture.ctx, fixture.upcomingEpoch.Index)
	require.NoError(t, err)

	reason, found := fallbackEventReason(fixture.ctx)
	require.True(t, found)
	require.Equal(t, "zero_final_weight", reason)
	require.Equal(t, 2, fixture.groupStub.memberReadsByGroup[77])
}

func TestOnEndOfPoCValidationStage_RecoversZeroCollateralWeight(t *testing.T) {
	collateral := selectiveCollateralKeeper{deposits: map[string]sdk.Coin{
		testutil.Validator: sdk.NewInt64Coin("ngonka", 100),
	}}
	fixture := newFormationRecoveryFixture(t, collateral, 0, 0)
	fixture.addFreshPoC(t, 100)

	require.NoError(t, fixture.module.onEndOfPoCValidationStage(fixture.ctx, 250, 1_000))

	stored, found := fixture.keeper.GetActiveParticipants(fixture.ctx, fixture.upcomingEpoch.Index)
	require.True(t, found)
	require.Len(t, stored.Participants, 1)
	require.Equal(t, fixture.veteran, stored.Participants[0].Index)
	require.Positive(t, stored.Participants[0].Weight)
	reason, found := fallbackEventReason(fixture.ctx)
	require.True(t, found)
	require.Equal(t, "zero_final_weight", reason)
}

func TestOnEndOfPoCValidationStage_InitialFallbackZeroWeightHalts(t *testing.T) {
	fixture := newFormationRecoveryFixture(t, noopCollateralKeeper{}, 0, 0)

	err := fixture.module.onEndOfPoCValidationStage(fixture.ctx, 250, 1_000)
	require.ErrorContains(t, err, "fallback participants have no positive final weight")
	requireNoFormationOutput(t, fixture)
	require.Equal(t, 2, fixture.groupStub.memberReadsByGroup[77])
}

func TestOnEndOfPoCValidationStage_EmptyFallbackHalts(t *testing.T) {
	fixture := newFormationRecoveryFixture(t, noopCollateralKeeper{}, 0, 2)
	for _, address := range []string{fixture.veteran, fixture.delegator} {
		participant, found := fixture.keeper.GetParticipant(fixture.ctx, address)
		require.True(t, found)
		participant.Status = types.ParticipantStatus_INACTIVE
		require.NoError(t, fixture.keeper.SetParticipant(fixture.ctx, participant))
	}

	err := fixture.module.onEndOfPoCValidationStage(fixture.ctx, 250, 1_000)
	require.ErrorContains(t, err, "no eligible participants")
	requireNoFormationOutput(t, fixture)
}

func TestOnEndOfPoCValidationStage_FailedRetryHalts(t *testing.T) {
	fixture := newFormationRecoveryFixture(t, noopCollateralKeeper{}, 1, 0)
	fixture.addFreshPoC(t, 100)

	err := fixture.module.onEndOfPoCValidationStage(fixture.ctx, 250, 1_000)
	require.ErrorContains(t, err, "fallback participants have no positive final weight")
	requireNoFormationOutput(t, fixture)
	require.Equal(t, 2, fixture.groupStub.memberReadsByGroup[77])
}

func TestOnEndOfPoCValidationStage_LoadsPreviousWeightsOnceOnSuccess(t *testing.T) {
	fixture := newFormationRecoveryFixture(t, noopCollateralKeeper{}, 0, 2)
	fixture.addFreshPoC(t, 100)

	require.NoError(t, fixture.module.onEndOfPoCValidationStage(fixture.ctx, 250, 1_000))
	require.Equal(t, 1, fixture.groupStub.memberReadsByGroup[77])
}

func TestOnEndOfPoCValidationStage_UnfilteredFallbackDoesNotReloadPreviousWeights(t *testing.T) {
	fixture := newFormationRecoveryFixture(t, noopCollateralKeeper{}, 0, 2)
	require.NoError(t, fixture.keeper.SetHardwareNodes(fixture.ctx, &types.HardwareNodes{
		Participant: fixture.veteran,
		HardwareNodes: []*types.HardwareNode{{
			LocalId: "veteran-node",
			Models:  []string{"unsupported-model"},
		}},
	}))

	require.NoError(t, fixture.module.onEndOfPoCValidationStage(fixture.ctx, 250, 1_000))
	require.Equal(t, 3, fixture.groupStub.memberReadsByGroup[77])
	reason, found := fallbackEventReason(fixture.ctx)
	require.True(t, found)
	require.Equal(t, "no_fresh_poc_node", reason)
}

func TestOnEndOfPoCValidationStage_PreviousWeightReadErrorHalts(t *testing.T) {
	fixture := newFormationRecoveryFixture(t, noopCollateralKeeper{}, 0, 2)
	fixture.addFreshPoC(t, 100)
	fixture.groupStub.membersErr = context.Canceled

	err := fixture.module.onEndOfPoCValidationStage(fixture.ctx, 250, 1_000)
	require.ErrorContains(t, err, "load previous confirmed weights")
	requireNoFormationOutput(t, fixture)
}

var _ types.CollateralKeeper = selectiveCollateralKeeper{}
var _ types.AccountKeeper = (*formationAccountKeeper)(nil)
