package broker

import (
	"testing"

	"decentralized-api/apiconfig"
	"decentralized-api/chainphase"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

type stubChallengeOverlay struct {
	self string
	ch   *types.OpenPoCChallenge
}

func (s stubChallengeOverlay) Self() string { return s.self }

func (s stubChallengeOverlay) Own(addr string) *types.OpenPoCChallenge {
	if s.ch != nil && s.ch.Target == addr {
		return s.ch
	}
	return nil
}

func (s stubChallengeOverlay) SelfGenerating() *types.OpenPoCChallenge {
	if s.ch != nil && s.ch.Generating && s.ch.Target == s.self {
		return s.ch
	}
	return nil
}

func withOverlay(t *testing.T, o challengeOverlay) {
	t.Helper()
	prev := overlay
	SetChallengeOverlay(o)
	t.Cleanup(func() { SetChallengeOverlay(prev) })
}

func TestStartPocCommand_OwnGeneratingIgnoresPocSlotDuringInference(t *testing.T) {
	node := createTestNode("node-1")
	node.State.PreservedModels = map[string]bool{"m": true}
	node.State.IntendedStatus = types.HardwareNodeStatus_INFERENCE

	tracker := newPhaseTrackerWithPhase(t, types.InferencePhase)
	withOverlay(t, stubChallengeOverlay{
		self: "me",
		ch: &types.OpenPoCChallenge{
			Target:      "me",
			StartHeight: 500,
			Seed:        []byte{1},
			Finish:      900,
			Generating:  true,
		},
	})

	b := &Broker{
		nodes:        map[string]*NodeWithState{"node-1": node},
		phaseTracker: tracker,
	}
	cmd := NewStartPocCommand()
	cmd.Execute(b)

	require.Equal(t, types.HardwareNodeStatus_POC, node.State.IntendedStatus)
	require.Equal(t, PocStatusGenerating, node.State.PocIntendedStatus)
}

func TestInitValidateCommand_VoteWindowKeepsPocSlot(t *testing.T) {
	node := createTestNode("node-1")
	node.State.PreservedModels = map[string]bool{"m": true}
	node.State.IntendedStatus = types.HardwareNodeStatus_INFERENCE

	tracker := newPhaseTrackerWithPhase(t, types.PoCValidatePhase)
	withOverlay(t, stubChallengeOverlay{
		self: "me",
		ch: &types.OpenPoCChallenge{
			Target:      "me",
			StartHeight: 500,
			Finish:      900,
			Generating:  true,
		},
	})

	b := &Broker{
		nodes:        map[string]*NodeWithState{"node-1": node},
		phaseTracker: tracker,
	}
	cmd := NewInitValidateCommand()
	cmd.Execute(b)

	require.Equal(t, types.HardwareNodeStatus_INFERENCE, node.State.IntendedStatus)
}

func TestInferenceUpAllCommand_NoopWhileOwnGenerating(t *testing.T) {
	node := createTestNode("node-1")
	node.State.IntendedStatus = types.HardwareNodeStatus_POC

	tracker := newPhaseTrackerWithPhase(t, types.InferencePhase)
	withOverlay(t, stubChallengeOverlay{
		self: "me",
		ch: &types.OpenPoCChallenge{
			Target:      "me",
			StartHeight: 500,
			Finish:      900,
			Generating:  true,
		},
	})

	b := &Broker{
		nodes:        map[string]*NodeWithState{"node-1": node},
		phaseTracker: tracker,
	}
	cmd := NewInferenceUpAllCommand()
	cmd.Execute(b)
	require.Equal(t, types.HardwareNodeStatus_POC, node.State.IntendedStatus)
}

func TestPrefetchPocParams_UsesChallengeSeedOutsideVoteWindow(t *testing.T) {
	tracker := newPhaseTrackerWithPhase(t, types.InferencePhase)
	withOverlay(t, stubChallengeOverlay{
		self: "me",
		ch: &types.OpenPoCChallenge{
			Target:      "me",
			StartHeight: 777,
			Seed:        []byte{0xab, 0xcd},
			Finish:      2000,
			Generating:  true,
		},
	})

	bridge := &MockBrokerChainBridge{}
	bridge.On("GetParams").Return(&types.QueryParamsResponse{Params: types.Params{}}, nil)

	b := &Broker{phaseTracker: tracker, chainBridge: bridge}
	node := createTestNode("node-1")
	node.State.IntendedStatus = types.HardwareNodeStatus_POC
	node.State.PocIntendedStatus = PocStatusGenerating
	params, err := b.prefetchPocParams(*tracker.GetCurrentEpochState(), map[string]*NodeWithState{"n": node}, 800)
	require.NoError(t, err)
	require.NotNil(t, params)
	require.Equal(t, int64(777), params.startPoCBlockHeight)
	require.Equal(t, "abcd", params.startPoCBlockHash)
}

func TestPrefetchPocParams_VoteWindowKeepsRegularParams(t *testing.T) {
	tracker := newPhaseTrackerWithPhase(t, types.PoCValidatePhase)
	withOverlay(t, stubChallengeOverlay{
		self: "me",
		ch: &types.OpenPoCChallenge{
			Target:      "me",
			StartHeight: 777,
			Seed:        []byte{0xab, 0xcd},
			Finish:      2000,
			Generating:  true,
		},
	})

	bridge := &MockBrokerChainBridge{}
	bridge.On("GetBlockHash", int64(100)).Return("regular-hash", nil)
	bridge.On("GetParams").Return(&types.QueryParamsResponse{Params: types.Params{}}, nil)

	b := &Broker{phaseTracker: tracker, chainBridge: bridge}
	node := createTestNode("node-1")
	node.State.IntendedStatus = types.HardwareNodeStatus_POC
	node.State.PocIntendedStatus = PocStatusValidating
	params, err := b.prefetchPocParams(*tracker.GetCurrentEpochState(), map[string]*NodeWithState{"n": node}, 130)
	require.NoError(t, err)
	require.NotNil(t, params)
	require.Equal(t, int64(100), params.startPoCBlockHeight)
	require.Equal(t, "regular-hash", params.startPoCBlockHash)
}

func TestChallengeGenerateNeedsDispatch(t *testing.T) {
	tracker := newPhaseTrackerWithPhase(t, types.InferencePhase)
	epoch := *tracker.GetCurrentEpochState()
	withOverlay(t, stubChallengeOverlay{
		self: "me",
		ch: &types.OpenPoCChallenge{
			Target:      "me",
			StartHeight: 500,
			Seed:        []byte{0xab, 0xcd},
			Finish:      900,
			Generating:  true,
		},
	})

	node := createTestNode("n1")
	node.State.IntendedStatus = types.HardwareNodeStatus_POC
	node.State.PocIntendedStatus = PocStatusGenerating
	node.State.CurrentStatus = types.HardwareNodeStatus_POC
	node.State.PocCurrentStatus = PocStatusGenerating
	node.State.LastPocV2BlockHeight = 400
	node.State.LastPocV2BlockHash = "old"
	require.True(t, challengeGenerateNeedsDispatch(node, epoch))

	node.State.LastPocV2BlockHeight = 500
	node.State.LastPocV2BlockHash = hexEncodeSeed([]byte{0xab, 0xcd})
	require.False(t, challengeGenerateNeedsDispatch(node, epoch), "matching params outside lead should not dispatch")
}

func TestChallengeGenerateNeedsDispatch_FinishLead(t *testing.T) {
	tracker := &chainphase.ChainPhaseTracker{}
	epoch := &types.Epoch{Index: 1, PocStartBlockHeight: 100}
	params := &types.EpochParams{
		EpochLength:           1000,
		EpochMultiplier:       1,
		PocStageDuration:      100,
		PocExchangeDuration:   50,
		PocValidationDelay:    10,
		PocValidationDuration: 100,
	}
	tracker.Update(chainphase.BlockInfo{Height: 897, Hash: "h"}, epoch, params, true, nil)
	withOverlay(t, stubChallengeOverlay{
		self: "me",
		ch: &types.OpenPoCChallenge{
			Target:      "me",
			StartHeight: 500,
			Seed:        []byte{1},
			Finish:      900,
			Generating:  true,
		},
	})
	node := createTestNode("n1")
	node.State.IntendedStatus = types.HardwareNodeStatus_POC
	node.State.PocIntendedStatus = PocStatusGenerating
	node.State.LastPocV2BlockHeight = 500
	node.State.LastPocV2BlockHash = hexEncodeSeed([]byte{1})
	require.True(t, challengeGenerateNeedsDispatch(node, *tracker.GetCurrentEpochState()))
}

func TestGetCommandForState_SetsWindDownAndLastPocV2(t *testing.T) {
	tracker := &chainphase.ChainPhaseTracker{}
	epoch := &types.Epoch{Index: 1, PocStartBlockHeight: 100}
	params := &types.EpochParams{
		EpochLength:           1000,
		EpochMultiplier:       1,
		PocStageDuration:      100,
		PocExchangeDuration:   50,
		PocValidationDelay:    10,
		PocValidationDuration: 100,
	}
	tracker.Update(chainphase.BlockInfo{Height: 897, Hash: "h"}, epoch, params, true, nil)
	withOverlay(t, stubChallengeOverlay{
		self: "me",
		ch: &types.OpenPoCChallenge{
			Target:      "me",
			StartHeight: 500,
			Seed:        []byte{1},
			Finish:      900,
			Generating:  true,
		},
	})
	b := NewTestBroker()
	b.phaseTracker = tracker
	nodeState := &NodeState{
		IntendedStatus:       types.HardwareNodeStatus_POC,
		PocIntendedStatus:    PocStatusGenerating,
		LastPocV2BlockHeight: 500,
		LastPocV2BlockHash:   "old",
		EpochModels:          map[string]types.Model{},
		EpochMLNodes:         map[string]types.MLNodeInfo{},
	}
	cmd := b.getCommandForState("n1", nodeState, map[string]ModelArgs{"model-a": {}}, &pocParams{
		startPoCBlockHeight: 500,
		startPoCBlockHash:   "abcd",
		models:              map[string]apiconfig.PoCModelConfigCache{"model-a": {ModelId: "model-a", SeqLen: 128}},
	}, nil, 1, nil)
	got, ok := cmd.(StartPoCNodeCommandV2)
	require.True(t, ok)
	require.True(t, got.WindDown)
	require.Equal(t, int64(500), got.LastPocV2BlockHeight)
	require.Equal(t, "old", got.LastPocV2BlockHash)
}

func TestEpochState_IsPoCVoteWindow(t *testing.T) {
	validate := newPhaseTrackerWithPhase(t, types.PoCValidatePhase).GetCurrentEpochState()
	require.True(t, validate.IsPoCVoteWindow())
	inference := newPhaseTrackerWithPhase(t, types.InferencePhase).GetCurrentEpochState()
	require.False(t, inference.IsPoCVoteWindow())
	_ = chainphase.EpochState{}
}
