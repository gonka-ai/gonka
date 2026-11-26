package host

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

func unversionedStartTx() *types.DevshardTx {
	return &types.DevshardTx{Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{InferenceId: 1}}}
}

func TestColdWalkRejectsWhenFirstPageHasNoStart(t *testing.T) {
	st := progressState(4)
	diffs := progressDiffs(4, bytes.Repeat([]byte{1}, 32))
	diffs[0].Txs = nil
	journal := &nonceJournal{diffs: diffs}
	peer := &coldPeer{head: coldNotFound}
	c := coldChecker{t: t, gate: NewColdWalks(), key: "exec", st: st, peer: peer, journal: journal}

	accept, err := c.check()
	require.NoError(t, err)
	require.False(t, accept)
	require.Empty(t, peer.sentNonces())
	require.Zero(t, peer.payloadCalls())
	require.Equal(t, []uint64{1}, journal.loadedNonces())

	accept, err = c.check()
	require.NoError(t, err)
	require.False(t, accept)
	require.Equal(t, []uint64{1}, journal.loadedNonces())
	require.Zero(t, peer.payloadCalls())
}

func TestColdWalkRejectsUnversionedStartInFirstPage(t *testing.T) {
	st := progressState(4)
	diffs := progressDiffs(4, bytes.Repeat([]byte{1}, 32))
	diffs[0].Txs = []*types.DevshardTx{unversionedStartTx()}
	journal := &nonceJournal{diffs: diffs}
	peer := &coldPeer{head: coldNotFound}
	c := coldChecker{t: t, gate: NewColdWalks(), key: "exec", st: st, peer: peer, journal: journal}

	accept, err := c.check()
	require.NoError(t, err)
	require.False(t, accept)
	require.Empty(t, peer.sentNonces())
	require.Zero(t, peer.payloadCalls())
}

func TestColdWalkRejectsWhenFirstPageVersionDiffers(t *testing.T) {
	st := progressState(4)
	diffs := progressDiffs(4, bytes.Repeat([]byte{1}, 32))
	diffs[0].Txs[0].GetStartInference().ProtocolVersion = "v-other"
	journal := &nonceJournal{diffs: diffs}
	peer := &coldPeer{head: coldNotFound}
	c := coldChecker{t: t, gate: NewColdWalks(), key: "exec", st: st, peer: peer, journal: journal}

	accept, err := c.check()
	require.NoError(t, err)
	require.False(t, accept)
	require.Zero(t, peer.payloadCalls())
}

func TestColdCheckWithoutGateRejectsUnbindableFirstPage(t *testing.T) {
	st := progressState(4)
	diffs := progressDiffs(4, bytes.Repeat([]byte{1}, 32))
	diffs[0].Txs = nil
	journal := &nonceJournal{diffs: diffs}
	peer := &coldPeer{head: coldNotFound}

	accept, err := runColdCheck(t, context.Background(), st, peer, journal, newEvidenceGroup(t).sm, 2*time.Second)
	require.NoError(t, err)
	require.False(t, accept)
	require.Empty(t, peer.sentNonces())
	require.Zero(t, peer.payloadCalls())
}

func TestColdStartProofCachesOnlyFinalAnswers(t *testing.T) {
	g := NewColdWalks()
	g.noteStartProof(false, false)
	_, known := g.startProof()
	require.False(t, known)

	g.noteStartProof(false, true)
	bindable, known := g.startProof()
	require.True(t, known)
	require.False(t, bindable)

	g = NewColdWalks()
	g.noteStartProof(true, true)
	bindable, known = g.startProof()
	require.True(t, known)
	require.True(t, bindable)
}

func TestColdWalkStillChallengesAnExecutorThatProvedItWasBound(t *testing.T) {
	st := progressState(4)
	st.HostStats = map[uint32]*types.HostStats{1: {Finished: 1, Cost: 10}}
	diffs := progressDiffs(4, bytes.Repeat([]byte{1}, 32))
	diffs[0].Txs = nil
	journal := &nonceJournal{diffs: diffs}
	peer := &coldPeer{head: coldNotFound}
	c := coldChecker{t: t, gate: NewColdWalks(), key: "exec", st: st, peer: peer, journal: journal}

	accept, err := c.check()
	require.NoError(t, err)
	require.True(t, accept)
	require.NotEmpty(t, peer.sentNonces())
}

func TestExecutorParticipated(t *testing.T) {
	st := progressState(1)
	st.Group = []types.SlotAssignment{{SlotID: 0, ValidatorAddress: "a"}, {SlotID: 1, ValidatorAddress: "b"}, {SlotID: 2, ValidatorAddress: "b"}}
	st.HostStats = map[uint32]*types.HostStats{}
	st.Inferences = map[uint64]*types.InferenceRecord{1: {ExecutorSlot: 1, Status: types.StatusPending}}
	require.False(t, types.ExecutorParticipated(st, 1))

	st.HostStats[2] = &types.HostStats{Finished: 1}
	require.True(t, types.ExecutorParticipated(st, 1))
	require.False(t, types.ExecutorParticipated(st, 0))

	st.HostStats = map[uint32]*types.HostStats{}
	st.Inferences[2] = &types.InferenceRecord{ExecutorSlot: 2, Status: types.StatusStarted}
	require.True(t, types.ExecutorParticipated(st, 1))

	st.Inferences[2].Status = types.StatusTimedOut
	require.False(t, types.ExecutorParticipated(st, 1))

	st.Inferences[2].ConfirmedAtHeight = 7
	require.True(t, types.ExecutorParticipated(st, 1))
}
