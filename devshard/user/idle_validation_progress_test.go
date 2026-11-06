package user

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/types"
)

// Both timings occur when validation completes near the final host response.
// A signed vote must reach the state machine without another client inference.
func TestQuietSettleValidationProgress(t *testing.T) {
	for _, inline := range []bool{true, false} {
		name := "vote_ready_after_last_response"
		if inline {
			name = "vote_in_last_response"
		}
		t.Run(name, func(t *testing.T) {
			session, hosts, _ := setupSessionWithOptions(t, 3, 1_000_000, 0,
				WithDisableHeightSyncHeartbeat(true), WithQuietSettleDelay(20*time.Millisecond))
			t.Cleanup(func() { _ = session.Close() })
			nonce := prepareNonce(t, session)
			require.Equal(t, uint32(1), mustInference(t, session, nonce).ExecutorSlot)
			confirmedAt := time.Now().Unix()
			require.NoError(t, session.ProcessResponse(1, &host.HostResponse{
				Nonce: nonce, Receipt: signedExecutorReceipt(t, session, hosts, nonce, confirmedAt),
				ConfirmedAt: confirmedAt,
				Mempool:     []*types.DevshardTx{finishTxFor(t, hosts[1], nonce, 1, testutil.TestResponseHash)},
			}, nonce))
			initial := &types.MsgValidation{
				InferenceId: nonce, ValidatorSlot: 0, Valid: false, EscrowId: "escrow-1",
			}
			// The fixture's host signs the same proposer payload as production.
			initial.ProposerSig = testutil.SignProposerTx(t, hosts[0], initial)
			session.mu.Lock()
			session.addPendingTx(&types.DevshardTx{Tx: &types.DevshardTx_Validation{Validation: initial}})
			session.mu.Unlock()

			voter := session.clients[2].(*InProcessClient)
			vote := &types.MsgValidationVote{
				InferenceId: nonce, VoterSlot: 2, VoteValid: false, EscrowId: "escrow-1",
			}
			vote.ProposerSig = testutil.SignProposerTx(t, hosts[2], vote)
			client := &idleValidationClient{
				InProcessClient:   voter,
				vote:              &types.DevshardTx{Tx: &types.DevshardTx_ValidationVote{ValidationVote: vote}},
				includeInResponse: inline, responseNonce: nonce + 1, published: make(chan struct{}),
			}
			session.clients[2] = client
			session.ArmQuietSettle()
			select {
			case <-client.published:
			case <-time.After(2 * time.Second):
				t.Fatal("quiet settlement never reached the voting host")
			}

			settled := assert.Eventually(t, func() bool {
				rec, ok := session.StateMachine().Inference(nonce)
				return ok && rec.Status == types.StatusInvalidated
			}, time.Second, 10*time.Millisecond, "a ready signed vote must resolve the dispute without another client request")
			if settled {
				return
			}
			session.mu.Lock()
			pending := len(session.pendingTxs)
			session.mu.Unlock()
			rec := mustInference(t, session, nonce)
			t.Logf("stalled: status=%v invalid_votes=%d gateway_pending=%d host_mempool=%d", rec.Status, rec.VotesInvalid, pending, len(voter.Host.MempoolTxs()))
			if inline {
				require.Equal(t, 1, pending, "the last response must already have delivered the vote")
			} else {
				require.Zero(t, pending, "the vote was published after the response snapshot")
				require.Len(t, voter.Host.MempoolTxs(), 1)
			}

			// Explicit collection and composition are a control for vote validity:
			// the identical signed vote must resolve the identical live dispute.
			response, err := voter.Send(context.Background(), host.HostRequest{Nonce: session.Nonce()}, nil, nil)
			require.NoError(t, err)
			require.NoError(t, session.ProcessResponse(2, response, session.Nonce()))
			require.NoError(t, session.DrainUnpinnedPending(context.Background()))
			require.Equal(t, types.StatusInvalidated, mustInference(t, session, nonce).Status)
			t.Log("control: explicit collection and composition resolved the dispute with the same vote")
		})
	}
}

type idleValidationClient struct {
	*InProcessClient
	vote              *types.DevshardTx
	includeInResponse bool
	responseNonce     uint64
	published         chan struct{}
	once              sync.Once
}

func (c *idleValidationClient) Send(ctx context.Context, req host.HostRequest, w io.Writer, onReceipt func(*host.HostResponse)) (*host.HostResponse, error) {
	response, err := c.InProcessClient.Send(ctx, req, w, onReceipt)
	if err == nil && req.Payload == nil && req.Nonce == c.responseNonce {
		c.once.Do(func() {
			c.Host.AddTx(c.vote)
			if c.includeInResponse {
				response.Mempool = c.Host.MempoolTxs()
			}
			close(c.published)
		})
	}
	return response, err
}
