package inference

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/testutil"
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
