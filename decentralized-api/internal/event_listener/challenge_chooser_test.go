package event_listener

import (
	"testing"

	"decentralized-api/broker"
	"decentralized-api/chainphase"
	"decentralized-api/poc"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func challengeChooserEpoch(phase types.EpochPhase, height int64, event *types.ConfirmationPoCEvent) chainphase.EpochState {
	epoch := types.Epoch{Index: 1, PocStartBlockHeight: 100}
	params := types.EpochParams{
		EpochLength:           1000,
		EpochShift:            0,
		PocStageDuration:      100,
		PocExchangeDuration:   50,
		PocValidationDelay:    10,
		PocValidationDuration: 100,
	}
	return chainphase.EpochState{
		LatestEpoch: types.NewEpochContext(epoch, params),
		CurrentBlock: chainphase.BlockInfo{
			Height: height,
			Hash:   "hash",
		},
		CurrentPhase:               phase,
		IsSynced:                   true,
		ActiveConfirmationPoCEvent: event,
	}
}

func TestGetCommandForPhase_ChallengeGenerating(t *testing.T) {
	t.Cleanup(poc.OpenChallenges.Reset)
	self := "gonka1target"
	poc.OpenChallenges.Replace(self, []*types.OpenPoCChallenge{{
		Challenge: &types.PoCChallenge{
			Target:      self,
			StartHeight: 500,
			Seed:        []byte{1, 2, 3},
		},
		Finish:     900,
		Generating: true,
	}}, 0)

	t.Run("inference returns StartPoc", func(t *testing.T) {
		cmd, _ := getCommandForPhase(challengeChooserEpoch(types.InferencePhase, 600, nil))
		_, ok := cmd.(broker.StartPocCommand)
		require.True(t, ok)
	})

	t.Run("cPoC generation returns StartPoc", func(t *testing.T) {
		event := &types.ConfirmationPoCEvent{
			Phase:         types.ConfirmationPoCPhase_CONFIRMATION_POC_GENERATION,
			TriggerHeight: 550,
		}
		cmd, _ := getCommandForPhase(challengeChooserEpoch(types.InferencePhase, 560, event))
		_, ok := cmd.(broker.StartPocCommand)
		require.True(t, ok)
	})

	t.Run("cPoC completed returns StartPoc", func(t *testing.T) {
		event := &types.ConfirmationPoCEvent{
			Phase:         types.ConfirmationPoCPhase_CONFIRMATION_POC_COMPLETED,
			TriggerHeight: 550,
		}
		cmd, _ := getCommandForPhase(challengeChooserEpoch(types.InferencePhase, 580, event))
		_, ok := cmd.(broker.StartPocCommand)
		require.True(t, ok)
	})

	t.Run("regular validation still InitValidate", func(t *testing.T) {
		cmd, _ := getCommandForPhase(challengeChooserEpoch(types.PoCValidatePhase, 220, nil))
		_, ok := cmd.(broker.InitValidateCommand)
		require.True(t, ok)
	})

	t.Run("cPoC validation still InitValidate", func(t *testing.T) {
		event := &types.ConfirmationPoCEvent{
			Phase:         types.ConfirmationPoCPhase_CONFIRMATION_POC_VALIDATION,
			TriggerHeight: 550,
		}
		cmd, _ := getCommandForPhase(challengeChooserEpoch(types.InferencePhase, 570, event))
		_, ok := cmd.(broker.InitValidateCommand)
		require.True(t, ok)
	})
}

func TestGetCommandForPhase_AfterFinishReturnsInference(t *testing.T) {
	t.Cleanup(poc.OpenChallenges.Reset)
	self := "gonka1target"
	poc.OpenChallenges.Replace(self, []*types.OpenPoCChallenge{{
		Challenge: &types.PoCChallenge{
			Target:      self,
			StartHeight: 500,
			Seed:        []byte{1},
		},
		Finish:     600,
		Generating: true,
	}}, 0)
	cmd, _ := getCommandForPhase(challengeChooserEpoch(types.InferencePhase, 600, nil))
	_, ok := cmd.(broker.InferenceUpAllCommand)
	require.True(t, ok)
}

func TestGetCommandForPhase_AfterGeneratingFalse(t *testing.T) {
	t.Cleanup(poc.OpenChallenges.Reset)
	self := "gonka1target"
	poc.OpenChallenges.Replace(self, []*types.OpenPoCChallenge{{
		Challenge: &types.PoCChallenge{
			Target:      self,
			StartHeight: 500,
		},
		Finish:     900,
		Generating: false,
	}}, 0)

	cmd, _ := getCommandForPhase(challengeChooserEpoch(types.InferencePhase, 600, nil))
	_, ok := cmd.(broker.InferenceUpAllCommand)
	require.True(t, ok)

	cmd, _ = getCommandForPhase(challengeChooserEpoch(types.PoCGeneratePhase, 100, nil))
	_, ok = cmd.(broker.StartPocCommand)
	require.True(t, ok)

	cmd, _ = getCommandForPhase(challengeChooserEpoch(types.PoCValidatePhase, 220, nil))
	_, ok = cmd.(broker.InitValidateCommand)
	require.True(t, ok)
}

func TestGetCommandForPhase_NoChallengeUnchanged(t *testing.T) {
	t.Cleanup(poc.OpenChallenges.Reset)
	cmd, _ := getCommandForPhase(challengeChooserEpoch(types.InferencePhase, 600, nil))
	_, ok := cmd.(broker.InferenceUpAllCommand)
	require.True(t, ok)
}
