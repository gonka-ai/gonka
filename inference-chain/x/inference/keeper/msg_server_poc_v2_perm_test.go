package keeper_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestMsgServer_SubmitPocValidationsV2_Permissions(t *testing.T) {
	assertParticipantOnlyPoCV2Message(t, func(signer string) keeper.HasSigners {
		return &types.MsgSubmitPocValidationsV2{
			Creator:                  signer,
			PocStageStartBlockHeight: 100,
			Validations: []*types.PoCValidationEntryV2{
				{
					ParticipantAddress: testutil.Executor,
					ModelId:            testPoCModelID,
					ValidatedWeight:    100,
				},
			},
		}
	})
}

func TestMsgServer_PoCV2StoreCommit_Permissions(t *testing.T) {
	assertParticipantOnlyPoCV2Message(t, func(signer string) keeper.HasSigners {
		return &types.MsgPoCV2StoreCommit{
			Creator:                  signer,
			PocStageStartBlockHeight: 100,
			Entries:                  []*types.PoCV2CommitEntry{makePoCV2CommitEntry(testPoCModelID, 10, 1)},
		}
	})
}

func TestMsgServer_MLNodeWeightDistribution_Permissions(t *testing.T) {
	assertParticipantOnlyPoCV2Message(t, func(signer string) keeper.HasSigners {
		return &types.MsgMLNodeWeightDistribution{
			Creator:                  signer,
			PocStageStartBlockHeight: 100,
			Entries: []*types.MLNodeDistributionEntry{
				{
					ModelId: testPoCModelID,
					Weights: []*types.MLNodeWeight{{NodeId: "node-1", Weight: 10}},
				},
			},
		}
	})
}

func assertParticipantOnlyPoCV2Message(t *testing.T, build func(signer string) keeper.HasSigners) {
	t.Helper()
	k, ms, ctx, _ := setupPermissionsHarness(t)

	signer := testutil.Creator
	msg := build(signer)

	err := keeper.CheckPermission(ms, ctx, msg, keeper.ParticipantPermission)
	require.Error(t, err)

	p := types.Participant{Index: signer, Address: signer}
	require.NoError(t, k.Participants.Set(ctx, sdk.MustAccAddressFromBech32(signer), p))
	err = keeper.CheckPermission(ms, ctx, msg, keeper.ParticipantPermission)
	require.NoError(t, err)
}
