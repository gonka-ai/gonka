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
// verify: a junk signature, or its own signature on another member's slot.
type forgedErrorMissClient struct {
	*applyingErrorMissClient
	junkSig   bool
	claimSlot uint32
}

func (c *forgedErrorMissClient) VerifyErrorMiss(ctx context.Context, inferenceID uint64, diffs []types.Diff, artifacts host.TimeoutArtifacts) (bool, []byte, uint32, []*types.DevshardTx, string, error) {
	if c.junkSig {
		return true, make([]byte, 65), c.claimSlot, nil, "", nil
	}
	accept, sig, _, mempool, cause, err := c.applyingErrorMissClient.VerifyErrorMiss(ctx, inferenceID, diffs, artifacts)
	return accept, sig, c.claimSlot, mempool, cause, err
}

func TestHandleErrorMiss_ForgedVoteDoesNotDiscardHonestQuorum(t *testing.T) {
	for _, tc := range []struct {
		name    string
		junkSig bool
		applied bool
	}{
		{"junk signature", true, false},
		{"own signature on another member's slot", false, false},
		{"junk signature, finish already applied", true, true},
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
			if !tc.junkSig {
				claim = session.group[(execIdx+2)%len(session.clients)].SlotID
			}
			session.clients[bad] = &forgedErrorMissClient{
				applyingErrorMissClient: session.clients[bad].(*applyingErrorMissClient),
				junkSig:                 tc.junkSig,
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
