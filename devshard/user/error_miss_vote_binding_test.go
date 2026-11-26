package user

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/types"
)

// forgedErrorMissClient accepts every error miss with a vote that does not
// verify: a junk signature, another member's slot, or another response hash.
type forgedErrorMissClient struct {
	*applyingErrorMissClient
	junkSig   bool
	wrongHash bool
	claimSlot uint32
}

func (c *forgedErrorMissClient) VerifyErrorMiss(ctx context.Context, inferenceID uint64, diffs []types.Diff, artifacts host.TimeoutArtifacts) (bool, []byte, uint32, []*types.DevshardTx, string, error) {
	if c.junkSig {
		return true, make([]byte, 65), c.claimSlot, nil, "", nil
	}
	accept, sig, _, mempool, cause, err := c.applyingErrorMissClient.VerifyErrorMiss(ctx, inferenceID, diffs, artifacts)
	if err == nil && accept && c.wrongHash {
		data, marshalErr := types.CanonicalSignedBytes(&types.ErrorMissVoteContent{
			EscrowId: "escrow-1", InferenceId: inferenceID, Accept: true, ResponseHash: make([]byte, 32),
		})
		if marshalErr != nil {
			return false, nil, 0, nil, "", marshalErr
		}
		sig, err = c.signer.Sign(data)
	}
	return accept, sig, c.claimSlot, mempool, cause, err
}

func TestHandleErrorMiss_ForgedVoteDoesNotDiscardHonestQuorum(t *testing.T) {
	for _, tc := range []struct {
		name      string
		junkSig   bool
		wrongHash bool
		applied   bool
	}{
		{name: "junk signature", junkSig: true},
		{name: "own signature on another member's slot"},
		{name: "junk signature, finish already applied", junkSig: true, applied: true},
		{name: "wrong response hash", wrongHash: true},
		{name: "wrong response hash, finish already applied", wrongHash: true, applied: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// 5 slots: threshold 2, four verifiers besides the executor.
			session, hosts, _ := setupSession(t, 5, 100000, 10)
			params := InferenceParams{
				Model: "llama", Prompt: testutil.TestPrompt,
				InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
			}
			nonce, execIdx := startConfirmForErrorMiss(t, session, hosts, params)
			payload := errorMissPayload(t)
			finishTx, finishBytes := signedErrorFinishTx(t, hosts, nonce, execIdx, payload)
			session.mu.Lock()
			session.addPendingTx(finishTx)
			session.nonceStates[nonce].finished = true
			session.mu.Unlock()
			if tc.applied {
				require.NoError(t, session.SendPendingDiff(context.Background()))
				require.Equal(t, types.StatusFinished, session.StateMachine().SnapshotState().Inferences[nonce].Status)
			}

			wrapApplyingErrorMissVerifiers(session, hosts)
			bad := (execIdx + 1) % len(session.clients)
			claim := session.group[bad].SlotID
			if !tc.junkSig && !tc.wrongHash {
				claim = session.group[(execIdx+2)%len(session.clients)].SlotID
			}
			session.clients[bad] = &forgedErrorMissClient{
				applyingErrorMissClient: session.clients[bad].(*applyingErrorMissClient),
				junkSig:                 tc.junkSig,
				wrongHash:               tc.wrongHash,
				claimSlot:               claim,
			}

			result, err := session.HandleErrorMiss(context.Background(), nonce, finishBytes, payload)
			require.ErrorIs(t, err, ErrInferenceMissed)
			require.True(t, result.Accepted)
			require.Equal(t, 3, result.Votes, "only the three honest votes are submitted")
			st := session.StateMachine().SnapshotState()
			require.Equal(t, types.StatusTimedOut, st.Inferences[nonce].Status)
			require.Equal(t, uint32(1), st.HostStats[uint32(execIdx)].Missed)
		})
	}
}

// The v7 gateway binds every live grant at the head of the diff carrying the
// votes. Unlike the old single-key model, a second live key must be accepted
// even before that bind is published; a revoked key must still be dropped.
func TestHandleErrorMiss_WarmKeyRotation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		useSecond  bool
		revokeVote bool
	}{
		{"original key stays live", false, false},
		{"second key binds with vote", true, false},
		{"revoked original key", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session, coldKeys, warmKeys := setupWarmKeySession(t, 5)
			nonce, execIdx := startConfirmForErrorMiss(t, session, warmKeys, defaultParams)
			payload := errorMissPayload(t)
			finishTx, finishBytes := signedErrorFinishTx(t, warmKeys, nonce, execIdx, payload)
			session.mu.Lock()
			session.addPendingTx(finishTx)
			session.nonceStates[nonce].finished = true
			session.mu.Unlock()

			voter := (execIdx + 1) % len(warmKeys)
			slot := session.group[voter].SlotID
			grants := session.warmKeySource.(*testutil.WarmKeyGrants)
			second := testutil.MustGenerateKey(t)
			grants.Grant(coldKeys[voter].Address(), second.Address())
			require.Equal(t, []string{warmKeys[voter].Address()}, session.sm.WarmKeyHistory().Live(slot))
			if tc.revokeVote {
				grants.Revoke(coldKeys[voter].Address(), warmKeys[voter].Address())
			}
			wrapApplyingErrorMissVerifiers(session, warmKeys)
			client := session.clients[voter].(*applyingErrorMissClient)
			if tc.useSecond {
				client.signer = second
			}

			// Isolate this voter so quorum cancellation cannot hide whether
			// its signature was accepted.
			votes, _, _, err := session.CollectErrorMissVotes(context.Background(), nonce,
				map[int]TimeoutVerifier{voter: client}, nil,
				host.TimeoutArtifacts{FinishTx: finishBytes, ResponsePayload: payload})
			require.NoError(t, err)
			if tc.revokeVote {
				require.Empty(t, votes)
			} else {
				require.Len(t, votes, 1)
			}

			result, err := session.HandleErrorMiss(context.Background(), nonce, finishBytes, payload)
			require.ErrorIs(t, err, ErrInferenceMissed)
			require.True(t, result.Accepted)
			st := session.sm.SnapshotState()
			require.Equal(t, types.StatusTimedOut, st.Inferences[nonce].Status)
			require.Equal(t, uint32(1), st.HostStats[uint32(execIdx)].Missed)
			live := st.WarmKeyHistory.Live(slot)
			require.Contains(t, live, second.Address(), "the new grant is bound before votes apply")
			if tc.revokeVote {
				require.NotContains(t, live, warmKeys[voter].Address())
			} else {
				require.Contains(t, live, warmKeys[voter].Address(), "overlapping live grants remain allowed")
			}
		})
	}
}
