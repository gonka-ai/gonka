package poc

import (
	"os"
	"testing"

	"decentralized-api/chainphase"
	"decentralized-api/cosmosclient"
	"decentralized-api/poc/artifacts"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestCommitWorker_ChallengeStoreCommitWhileUnfrozen(t *testing.T) {
	t.Cleanup(OpenChallenges.Reset)
	addr := "participant_addr"
	OpenChallenges.Replace(addr, []*types.OpenPoCChallenge{{
		Target:      addr,
		StartHeight: 500,
		Seed:        []byte{1},
		Finish:      2000,
		Generating:  true,
	}})

	tmpDir, err := os.MkdirTemp("", "challenge_commit")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	store := artifacts.NewManagedArtifactStore(tmpDir, 5)
	defer store.Close()
	store.ActivateStage(500)
	artifactStore, err := store.GetOrCreateStore(500, "model-a")
	require.NoError(t, err)
	require.NoError(t, artifactStore.AddWithNode(1, []byte("vec-1"), "node-1"))
	require.NoError(t, artifactStore.Flush())

	recorder := &cosmosclient.MockCosmosMessageClient{}
	recorder.On("SubmitPoCChallengeStoreCommitWithTimeout", mock.MatchedBy(func(msg *types.MsgPoCChallengeStoreCommit) bool {
		return msg != nil &&
			msg.PocStageStartBlockHeight == 500 &&
			len(msg.Entries) == 1 &&
			msg.Entries[0].ModelId == "model-a"
	}), uint64(803)).Return(nil).Once()

	tracker := &chainphase.ChainPhaseTracker{}
	tracker.Update(
		chainphase.BlockInfo{Height: 800, Hash: "h"},
		&types.Epoch{Index: 1, PocStartBlockHeight: 100},
		&types.EpochParams{EpochLength: 1000, PocStageDuration: 100, PocExchangeDuration: 50},
		true,
		nil,
	)

	worker := &CommitWorker{
		store:                  store,
		recorder:               recorder,
		tracker:                tracker,
		participantAddress:     addr,
		challengeLastCommitted: make(map[commitKey]commitState),
		challengePending:       make(map[commitKey]pendingCommit),
		lastCommitted:          make(map[commitKey]commitState),
		pending:                make(map[commitKey]pendingCommit),
	}
	worker.tick()
	recorder.AssertExpectations(t)
	recorder.AssertNotCalled(t, "SubmitPoCV2StoreCommitWithTimeout", mock.Anything, mock.Anything)
}

func TestCommitWorker_ChallengeStoreCommitStopsAfterFinish(t *testing.T) {
	t.Cleanup(OpenChallenges.Reset)
	addr := "participant_addr"
	OpenChallenges.Replace(addr, []*types.OpenPoCChallenge{{
		Target:      addr,
		StartHeight: 500,
		Finish:      800,
		Generating:  true,
	}})

	tmpDir, err := os.MkdirTemp("", "challenge_commit_done")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	store := artifacts.NewManagedArtifactStore(tmpDir, 5)
	defer store.Close()
	store.ActivateStage(500)
	artifactStore, err := store.GetOrCreateStore(500, "model-a")
	require.NoError(t, err)
	require.NoError(t, artifactStore.AddWithNode(1, []byte("vec-1"), "node-1"))
	require.NoError(t, artifactStore.Flush())

	recorder := &cosmosclient.MockCosmosMessageClient{}
	tracker := &chainphase.ChainPhaseTracker{}
	tracker.Update(
		chainphase.BlockInfo{Height: 800, Hash: "h"},
		&types.Epoch{Index: 1, PocStartBlockHeight: 100},
		&types.EpochParams{EpochLength: 1000, PocStageDuration: 100, PocExchangeDuration: 50},
		true,
		nil,
	)

	worker := &CommitWorker{
		store:                  store,
		recorder:               recorder,
		tracker:                tracker,
		participantAddress:     addr,
		challengeLastCommitted: make(map[commitKey]commitState),
	}
	worker.tick()
	recorder.AssertNotCalled(t, "SubmitPoCChallengeStoreCommitWithTimeout", mock.Anything, mock.Anything)
	assert.Equal(t, int64(0), worker.lastChallengeBroadcastHeight)
}

func TestCommitWorker_ChallengeCommitPendingUntilConfirmed(t *testing.T) {
	t.Cleanup(OpenChallenges.Reset)
	addr := "participant_addr"
	ch := &types.OpenPoCChallenge{
		Target:      addr,
		StartHeight: 500,
		Seed:        []byte{1},
		Finish:      2000,
		Generating:  true,
	}
	OpenChallenges.Replace(addr, []*types.OpenPoCChallenge{ch})

	tmpDir, err := os.MkdirTemp("", "challenge_commit_pending")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)
	store := artifacts.NewManagedArtifactStore(tmpDir, 5)
	defer store.Close()
	store.ActivateStage(500)
	artifactStore, err := store.GetOrCreateStore(500, "model-a")
	require.NoError(t, err)
	require.NoError(t, artifactStore.AddWithNode(1, []byte("vec-1"), "node-1"))
	require.NoError(t, artifactStore.Flush())

	recorder := &cosmosclient.MockCosmosMessageClient{}
	recorder.On("SubmitPoCChallengeStoreCommitWithTimeout", mock.Anything, mock.Anything).Return(nil).Once()

	tracker := &chainphase.ChainPhaseTracker{}
	tracker.Update(
		chainphase.BlockInfo{Height: 800, Hash: "h"},
		&types.Epoch{Index: 1, PocStartBlockHeight: 100},
		&types.EpochParams{EpochLength: 1000, PocStageDuration: 100, PocExchangeDuration: 50},
		true,
		nil,
	)
	worker := &CommitWorker{
		store:                  store,
		recorder:               recorder,
		tracker:                tracker,
		participantAddress:     addr,
		challengeLastCommitted: make(map[commitKey]commitState),
		challengePending:       make(map[commitKey]pendingCommit),
		lastCommitted:          make(map[commitKey]commitState),
		pending:                make(map[commitKey]pendingCommit),
	}
	worker.tick()
	recorder.AssertNumberOfCalls(t, "SubmitPoCChallengeStoreCommitWithTimeout", 1)
	require.Len(t, worker.challengePending, 1)
	require.Empty(t, worker.challengeLastCommitted)

	tracker.Update(
		chainphase.BlockInfo{Height: 801, Hash: "h2"},
		&types.Epoch{Index: 1, PocStartBlockHeight: 100},
		&types.EpochParams{EpochLength: 1000, PocStageDuration: 100, PocExchangeDuration: 50},
		true,
		nil,
	)
	worker.tick()
	recorder.AssertNumberOfCalls(t, "SubmitPoCChallengeStoreCommitWithTimeout", 1)

	count, root := artifactStore.GetFlushedRoot()
	ch.Commits = []*types.PoCV2StoreCommit{{
		ParticipantAddress: addr,
		ModelId:            "model-a",
		Count:              count,
		RootHash:           root,
	}}
	OpenChallenges.Replace(addr, []*types.OpenPoCChallenge{ch})
	tracker.Update(
		chainphase.BlockInfo{Height: 802, Hash: "h3"},
		&types.Epoch{Index: 1, PocStartBlockHeight: 100},
		&types.EpochParams{EpochLength: 1000, PocStageDuration: 100, PocExchangeDuration: 50},
		true,
		nil,
	)
	worker.tick()
	require.Empty(t, worker.challengePending)
	require.NotEmpty(t, worker.challengeLastCommitted)
}

func TestChallengeCommitTimeoutHeight(t *testing.T) {
	require.Equal(t, uint64(803), challengeCommitTimeoutHeight(800, 2000))
	require.Equal(t, uint64(1999), challengeCommitTimeoutHeight(1997, 2000))
	require.Equal(t, uint64(0), challengeCommitTimeoutHeight(800, 1))
}
