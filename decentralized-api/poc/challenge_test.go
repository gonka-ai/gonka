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

func openCh(target string, start, finish int64, generating bool, seed ...byte) *types.OpenPoCChallenge {
	return &types.OpenPoCChallenge{
		Challenge: &types.PoCChallenge{
			Target:      target,
			StartHeight: start,
			Seed:        seed,
		},
		Finish:     finish,
		Generating: generating,
	}
}

func TestShouldValidateChallenge(t *testing.T) {
	short := openCh("me", 100, 200, false)
	require.False(t, ShouldValidateChallenge(short, 500))

	unfrozen := openCh("me", 100, 500, false)
	require.False(t, ShouldValidateChallenge(unfrozen, 400))
	require.True(t, ShouldValidateChallenge(unfrozen, 500))
	require.True(t, ShouldValidateChallenge(unfrozen, 501))
}

func TestPunishableIncludesSelf(t *testing.T) {
	t.Cleanup(OpenChallenges.Reset)
	self := "gonka1self"
	OpenChallenges.Replace(self, []*types.OpenPoCChallenge{
		openCh(self, 10, 400, false),
		openCh("other", 20, 50, false),
	}, 0)
	got := OpenChallenges.Punishable()
	require.Len(t, got, 1)
	require.Equal(t, self, got[0].Target())
}

func TestPunishableUsesCachedMin(t *testing.T) {
	t.Cleanup(OpenChallenges.Reset)
	self := "gonka1self"
	ch := []*types.OpenPoCChallenge{openCh(self, 10, 18, false)}
	OpenChallenges.Replace(self, ch, 0)
	require.Empty(t, OpenChallenges.Punishable())
	require.False(t, ShouldValidateChallenge(ch[0], 18))

	OpenChallenges.Replace(self, ch, 8)
	require.Len(t, OpenChallenges.Punishable(), 1)
	require.True(t, ShouldValidateChallenge(ch[0], 18))
}

func TestGetCurrentPocStageHeight_ChallengeOnlyOutsideVoteWindow(t *testing.T) {
	t.Cleanup(OpenChallenges.Reset)
	self := "gonka1self"
	OpenChallenges.Replace(self, []*types.OpenPoCChallenge{openCh(self, 777, 2000, true, 9)}, 0)

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
	OpenChallenges.Replace(self, []*types.OpenPoCChallenge{openCh(self, 777, 900, true)}, 0)

	inference := createTestEpochState(types.InferencePhase, 800, 100)
	require.NotNil(t, OwnChallengeGenerate(inference))

	afterFinish := createTestEpochState(types.InferencePhase, 900, 100)
	require.Nil(t, OwnChallengeGenerate(afterFinish))

	OpenChallenges.Replace(self, []*types.OpenPoCChallenge{openCh(self, 777, 0, true)}, 0)
	require.Nil(t, OwnChallengeGenerate(inference))
	OpenChallenges.Replace(self, []*types.OpenPoCChallenge{openCh(self, 777, 900, true)}, 0)

	validate := createTestEpochState(types.PoCValidatePhase, 220, 100)
	require.Nil(t, OwnChallengeGenerate(validate))

	cpocVal := createTestEpochState(types.InferencePhase, 800, 100)
	cpocVal.ActiveConfirmationPoCEvent = &types.ConfirmationPoCEvent{
		Phase:         types.ConfirmationPoCPhase_CONFIRMATION_POC_VALIDATION,
		TriggerHeight: 400,
	}
	require.Nil(t, OwnChallengeGenerate(cpocVal))
}

func TestGetCurrentPocStageHeight_AfterFinishUsesRegular(t *testing.T) {
	t.Cleanup(OpenChallenges.Reset)
	self := "gonka1self"
	OpenChallenges.Replace(self, []*types.OpenPoCChallenge{openCh(self, 777, 800, true)}, 0)
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
	OpenChallenges.Replace(self, []*types.OpenPoCChallenge{openCh(self, 10, 400, true)}, 0)

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
