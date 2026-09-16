package poc

import (
	"encoding/base64"
	"encoding/hex"
	"testing"

	"decentralized-api/broker"
	"decentralized-api/chainphase"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestShouldValidateChallenge(t *testing.T) {
	short := &types.OpenPoCChallenge{StartHeight: 100, Finish: 200, Target: "me"}
	require.False(t, ShouldValidateChallenge(short, 500))

	unfrozen := &types.OpenPoCChallenge{StartHeight: 100, Finish: 500, Target: "me"}
	require.False(t, ShouldValidateChallenge(unfrozen, 400))
	require.True(t, ShouldValidateChallenge(unfrozen, 500))
	require.True(t, ShouldValidateChallenge(unfrozen, 501))
}

func TestPunishableIncludesSelf(t *testing.T) {
	t.Cleanup(OpenChallenges.Reset)
	self := "gonka1self"
	OpenChallenges.Replace(self, []*types.OpenPoCChallenge{
		{Target: self, StartHeight: 10, Finish: 400, Generating: false},
		{Target: "other", StartHeight: 20, Finish: 50, Generating: false},
	})
	got := OpenChallenges.Punishable()
	require.Len(t, got, 1)
	require.Equal(t, self, got[0].Target)
}

func TestGetCurrentPocStageHeight_ChallengeOnlyOutsideVoteWindow(t *testing.T) {
	t.Cleanup(OpenChallenges.Reset)
	self := "gonka1self"
	OpenChallenges.Replace(self, []*types.OpenPoCChallenge{{
		Target:      self,
		StartHeight: 777,
		Seed:        []byte{9},
		Finish:      2000,
		Generating:  true,
	}})

	inference := createTestEpochState(types.InferencePhase, 800, 100)
	require.Equal(t, int64(777), GetCurrentPocStageHeight(inference))
	require.True(t, ShouldAcceptGeneratedArtifacts(inference))

	validate := createTestEpochState(types.PoCValidatePhase, 220, 100)
	require.Equal(t, int64(100), GetCurrentPocStageHeight(validate))
	require.False(t, ShouldAcceptGeneratedArtifacts(validate))

	cpocVal := createTestEpochState(types.InferencePhase, 800, 100)
	cpocVal.ActiveConfirmationPoCEvent = &types.ConfirmationPoCEvent{
		Phase:         types.ConfirmationPoCPhase_CONFIRMATION_POC_VALIDATION,
		TriggerHeight: 400,
	}
	require.Equal(t, int64(400), GetCurrentPocStageHeight(cpocVal))
}

func TestShouldStopChallengeValidationIgnoresStageHeight(t *testing.T) {
	validate := createTestEpochState(types.PoCValidatePhase, 220, 100)
	require.False(t, shouldStopChallengeValidationForTest(validate))

	inference := createTestEpochState(types.InferencePhase, 800, 100)
	require.True(t, shouldStopChallengeValidationForTest(inference))
}

func shouldStopChallengeValidationForTest(state *chainphase.EpochState) bool {
	if state.IsNilOrNotSynced() {
		return false
	}
	return !ShouldAcceptValidatedArtifacts(state)
}

func TestAccountPubKeyToHex(t *testing.T) {
	raw := []byte{0x01, 0x02, 0x03, 0x04}
	b64 := base64.StdEncoding.EncodeToString(raw)
	require.Equal(t, hex.EncodeToString(raw), AccountPubKeyToHex(b64))
	require.Equal(t, "", AccountPubKeyToHex(""))
	require.Equal(t, "", AccountPubKeyToHex("not-base64!"))
}

func TestOwnChallengeGenerate(t *testing.T) {
	t.Cleanup(OpenChallenges.Reset)
	self := "gonka1self"
	OpenChallenges.Replace(self, []*types.OpenPoCChallenge{{
		Target:      self,
		StartHeight: 777,
		Finish:      900,
		Generating:  true,
	}})

	inference := createTestEpochState(types.InferencePhase, 800, 100)
	require.NotNil(t, OwnChallengeGenerate(inference))

	afterFinish := createTestEpochState(types.InferencePhase, 900, 100)
	require.Nil(t, OwnChallengeGenerate(afterFinish))

	OpenChallenges.Replace(self, []*types.OpenPoCChallenge{{
		Target:      self,
		StartHeight: 777,
		Finish:      0,
		Generating:  true,
	}})
	require.Nil(t, OwnChallengeGenerate(inference))
	OpenChallenges.Replace(self, []*types.OpenPoCChallenge{{
		Target:      self,
		StartHeight: 777,
		Finish:      900,
		Generating:  true,
	}})

	validate := createTestEpochState(types.PoCValidatePhase, 220, 100)
	require.Nil(t, OwnChallengeGenerate(validate))
}

func TestGetCurrentPocStageHeight_AfterFinishUsesRegular(t *testing.T) {
	t.Cleanup(OpenChallenges.Reset)
	self := "gonka1self"
	OpenChallenges.Replace(self, []*types.OpenPoCChallenge{{
		Target:      self,
		StartHeight: 777,
		Finish:      800,
		Generating:  true,
	}})
	inference := createTestEpochState(types.InferencePhase, 800, 100)
	require.Equal(t, int64(100), GetCurrentPocStageHeight(inference))
}

func TestSeedHex(t *testing.T) {
	require.Equal(t, "0102", SeedHex([]byte{1, 2}))
	require.Equal(t, "", SeedHex(nil))
}

func TestFilterNodesForValidationIncludesPocSlotWhenChallenged(t *testing.T) {
	t.Cleanup(OpenChallenges.Reset)
	self := "gonka1self"
	OpenChallenges.Replace(self, []*types.OpenPoCChallenge{{
		Target:      self,
		StartHeight: 10,
		Finish:      400,
		Generating:  true,
	}})

	nodes := []broker.NodeResponse{{
		Node: broker.Node{Id: "n1"},
		State: broker.NodeState{
			CurrentStatus:   types.HardwareNodeStatus_POC,
			PreservedModels: map[string]bool{"m": true},
			AdminState:      broker.AdminState{Enabled: true, Epoch: 0},
		},
	}}
	filtered := filterNodesForValidation(nodes, 1, types.InferencePhase, true)
	require.Len(t, filtered, 1)
}

func TestFilterNodesForValidationSkipsPocSlotWhenNotChallenged(t *testing.T) {
	t.Cleanup(OpenChallenges.Reset)
	nodes := []broker.NodeResponse{{
		Node: broker.Node{Id: "n1"},
		State: broker.NodeState{
			CurrentStatus:   types.HardwareNodeStatus_POC,
			PreservedModels: map[string]bool{"m": true},
			AdminState:      broker.AdminState{Enabled: true, Epoch: 0},
		},
	}}
	filtered := filterNodesForValidation(nodes, 1, types.PoCValidatePhase, false)
	require.Len(t, filtered, 0)
}
