package app_test

import (
	"testing"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/app"
	"github.com/productscience/inference/app/upgrades/v0_2_18"
)

func finalizeAndCommit(t *testing.T, testApp *app.App, height int64, txs [][]byte) *abci.ResponseFinalizeBlock {
	t.Helper()
	resp, err := testApp.FinalizeBlock(&abci.RequestFinalizeBlock{Height: height, Txs: txs})
	require.NoError(t, err)
	_, err = testApp.Commit()
	require.NoError(t, err)
	return resp
}

// TestBeaconVoteExtensions_EnableAndRunBlocks drives the full app across the
// vote-extensions enable height: the v0.2.18 enable logic schedules E, the
// FinalizeBlock response hands the new params to CometBFT, ExtendVote is gated
// by the SDK until E and returns an empty extension from E on, VerifyVoteExtension
// accepts unknown payloads up to the size cap, and blocks keep
// finalizing with the beacon proposal handlers and PreBlocker installed.
func TestBeaconVoteExtensions_EnableAndRunBlocks(t *testing.T) {
	testApp := createTestApp(t) // height 1 committed

	// Schedule vote extensions at 2 + 2 = 4 in the state that block 2 commits,
	// exactly as the upgrade handler does from x/upgrade's PreBlocker.
	const delay = 2
	seedCtx := testApp.NewUncachedContext(false, cmtproto.Header{Height: 2})
	enableHeight, changed, err := v0_2_18.EnableVoteExtensions(seedCtx, testApp.ConsensusParamsKeeper.ParamsStore, delay)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, int64(4), enableHeight)

	resp := finalizeAndCommit(t, testApp, 2, nil)
	require.NotNil(t, resp.ConsensusParamUpdates)
	require.NotNil(t, resp.ConsensusParamUpdates.Abci)
	require.Equal(t, enableHeight, resp.ConsensusParamUpdates.Abci.VoteExtensionsEnableHeight,
		"CometBFT learns the enable height from the FinalizeBlock response")

	finalizeAndCommit(t, testApp, 3, nil)

	// Before E the SDK refuses to call the handler (CometBFT never asks).
	_, err = testApp.ExtendVote(seedCtx, &abci.RequestExtendVote{Height: 3})
	require.Error(t, err)

	// At E: empty extension, no error; verify accepts empty, rejects junk.
	ext, err := testApp.ExtendVote(seedCtx, &abci.RequestExtendVote{Height: enableHeight})
	require.NoError(t, err)
	require.Empty(t, ext.VoteExtension)

	ver, err := testApp.VerifyVoteExtension(&abci.RequestVerifyVoteExtension{Height: enableHeight, VoteExtension: nil})
	require.NoError(t, err)
	require.Equal(t, abci.ResponseVerifyVoteExtension_ACCEPT, ver.Status)

	// Unknown payloads within the cap are accepted (version-skew safety)...
	ver, err = testApp.VerifyVoteExtension(&abci.RequestVerifyVoteExtension{Height: enableHeight, VoteExtension: []byte{1}})
	require.NoError(t, err)
	require.Equal(t, abci.ResponseVerifyVoteExtension_ACCEPT, ver.Status)

	// ...oversized ones are rejected (cap derived from x/bls i_total_slots in committed state).
	ver, err = testApp.VerifyVoteExtension(&abci.RequestVerifyVoteExtension{Height: enableHeight, VoteExtension: make([]byte, 1<<20)})
	require.NoError(t, err)
	require.Equal(t, abci.ResponseVerifyVoteExtension_REJECT, ver.Status)

	finalizeAndCommit(t, testApp, enableHeight, nil)

	// E+1: the proposer gets an extended commit with empty extensions;
	// PrepareProposal passes txs through, ProcessProposal accepts, block finalizes.
	next := enableHeight + 1
	txs := [][]byte{{0x0a, 0x00}}
	prep, err := testApp.PrepareProposal(&abci.RequestPrepareProposal{
		Height:     next,
		MaxTxBytes: 1 << 20,
		Txs:        txs,
		LocalLastCommit: abci.ExtendedCommitInfo{
			Votes: []abci.ExtendedVoteInfo{{VoteExtension: []byte{}, BlockIdFlag: cmtproto.BlockIDFlagCommit}},
		},
	})
	require.NoError(t, err)
	require.Equal(t, txs, prep.Txs)

	proc, err := testApp.ProcessProposal(&abci.RequestProcessProposal{Height: next, Txs: prep.Txs})
	require.NoError(t, err)
	require.Equal(t, abci.ResponseProcessProposal_ACCEPT, proc.Status)

	for h := next; h < next+5; h++ {
		finalizeAndCommit(t, testApp, h, nil)
	}
	require.Equal(t, next+4, testApp.LastBlockHeight())

	// Re-running the enable logic after E leaves params untouched.
	lateCtx := testApp.NewUncachedContext(false, cmtproto.Header{Height: next + 5})
	h, changed, err := v0_2_18.EnableVoteExtensions(lateCtx, testApp.ConsensusParamsKeeper.ParamsStore, delay)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, enableHeight, h)
}
