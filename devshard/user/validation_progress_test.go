package user

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/stub"
	"devshard/types"
)

type controlledValidationMempool struct {
	*InProcessClient
	fetch func(context.Context) ([]*types.DevshardTx, error)
}

func (c *controlledValidationMempool) GetMempool(ctx context.Context) ([]*types.DevshardTx, error) {
	return c.fetch(ctx)
}

func pendingValidationSession(t *testing.T) (*Session, []*signing.Secp256k1Signer, uint64) {
	t.Helper()
	return pendingValidationSessionWithHosts(t, 3)
}

func pendingValidationSessionWithHosts(t *testing.T, numHosts int) (*Session, []*signing.Secp256k1Signer, uint64) {
	t.Helper()
	session, keys, _ := setupSessionWithOptions(t, numHosts, 1_000_000, 0,
		WithDisableHeightSyncHeartbeat(true), WithQuietSettleDelay(time.Hour), WithVerifierQueue(newVerifierHostQueue()))
	t.Cleanup(func() { _ = session.Close() })
	_, err := session.SendInference(context.Background(), InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: time.Now().Unix(),
	})
	require.NoError(t, err)
	nonce := session.Nonce()
	initial := &types.MsgValidation{InferenceId: nonce, ValidatorSlot: 0, Valid: false, EscrowId: "escrow-1"}
	initial.ProposerSig = testutil.SignProposerTx(t, keys[0], initial)
	session.mu.Lock()
	session.addPendingTx(&types.DevshardTx{Tx: &types.DevshardTx_Validation{Validation: initial}})
	session.mu.Unlock()
	return session, keys, nonce
}

func TestValidationProgress_CloseCancelsQuery(t *testing.T) {
	session, _, _ := pendingValidationSession(t)
	entered, exited := make(chan struct{}), make(chan struct{})
	session.clients[1] = &controlledValidationMempool{
		InProcessClient: session.clients[1].(*InProcessClient),
		fetch: func(ctx context.Context) ([]*types.DevshardTx, error) {
			close(entered)
			<-ctx.Done()
			close(exited)
			return nil, ctx.Err()
		},
	}
	session.ArmQuietSettle()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("mempool query never started")
	}
	closed := make(chan error, 1)
	go func() { closed <- session.Close() }()
	select {
	case err := <-closed:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not cancel the mempool query")
	}
	select {
	case <-exited:
	default:
		t.Fatal("Close returned before the mempool query exited")
	}
}

func TestValidationProgress_ReadyVoteDoesNotWaitForSlowPeer(t *testing.T) {
	session, keys, nonce := pendingValidationSession(t)
	entered, exited := make(chan struct{}), make(chan struct{})
	session.clients[1] = &controlledValidationMempool{
		InProcessClient: session.clients[1].(*InProcessClient),
		fetch: func(ctx context.Context) ([]*types.DevshardTx, error) {
			close(entered)
			<-ctx.Done()
			close(exited)
			return nil, ctx.Err()
		},
	}
	voter := session.clients[2].(*InProcessClient)
	vote := &types.MsgValidationVote{InferenceId: nonce, VoterSlot: 2, VoteValid: false, EscrowId: "escrow-1"}
	vote.ProposerSig = testutil.SignProposerTx(t, keys[2], vote)
	session.clients[2] = &controlledValidationMempool{
		InProcessClient: voter,
		fetch: func(ctx context.Context) ([]*types.DevshardTx, error) {
			select {
			case <-entered:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return []*types.DevshardTx{{Tx: &types.DevshardTx_ValidationVote{ValidationVote: vote}}}, nil
		},
	}
	session.ArmQuietSettle()
	require.Eventually(t, func() bool {
		rec, ok := session.StateMachine().Inference(nonce)
		return ok && rec.Status == types.StatusInvalidated
	}, 3*time.Second, 10*time.Millisecond)
	select {
	case <-exited:
	case <-time.After(3 * time.Second):
		t.Fatal("resolved dispute left a slow query running")
	}
}

func TestValidationProgress_InvalidVoteDoesNotSpendNonce(t *testing.T) {
	config := testutil.DefaultConfig(3)
	config.FeePerNonce = 7
	session, _, _ := setupSessionWithConfigAndEngine(t, 3, 100000, 10, config, nil)
	t.Cleanup(func() { _ = session.Close() })
	before, err := session.StateMachine().ComputeStateRoot()
	require.NoError(t, err)
	session.mu.Lock()
	session.addPendingTx(&types.DevshardTx{Tx: &types.DevshardTx_ValidationVote{ValidationVote: &types.MsgValidationVote{
		InferenceId: 999, VoterSlot: 0, VoteValid: false, EscrowId: "escrow-1",
	}}})
	session.mu.Unlock()
	changed, err := session.drainValidationProgress(context.Background())
	require.NoError(t, err)
	require.False(t, changed)
	require.Zero(t, session.Nonce())
	after, err := session.StateMachine().ComputeStateRoot()
	require.NoError(t, err)
	require.Equal(t, before, after, "an invalid message cannot consume FeePerNonce")
}

func TestValidationProgress_TrafficAndDuplicatesKeepPollSchedule(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		session, _, nonce := pendingValidationSession(t)
		var polls [3][]time.Time
		for i, client := range session.clients {
			session.clients[i] = &controlledValidationMempool{
				InProcessClient: client.(*InProcessClient),
				fetch: func(context.Context) ([]*types.DevshardTx, error) {
					polls[i] = append(polls[i], time.Now())
					return nil, nil
				},
			}
		}
		session.mu.Lock()
		var duplicate *types.DevshardTx
		for _, tx := range session.pendingTxs {
			if validationMessage(tx) {
				duplicate = tx
			}
		}
		session.mu.Unlock()
		require.NotNil(t, duplicate)
		start := time.Now()
		session.ArmQuietSettle()
		synctest.Wait()
		for i := 0; i < 90; i++ {
			time.Sleep(100 * time.Millisecond)
			// These are the same hooks ordinary traffic and repeated gossip use.
			session.mu.Lock()
			session.noteValidationDiffLocked([]*types.DevshardTx{{
				Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{}},
			}}, nil)
			session.mu.Unlock()
			require.NoError(t, session.ProcessResponse(0, &host.HostResponse{
				Nonce: session.Nonce(), Mempool: []*types.DevshardTx{duplicate},
			}, nonce))
			session.ArmQuietSettle()
			synctest.Wait()
		}
		want := []time.Duration{0, time.Second, 3 * time.Second, 8 * time.Second}
		for i, times := range polls {
			got := make([]time.Duration, len(times))
			for j, at := range times {
				got[j] = at.Sub(start)
			}
			require.Equal(t, want, got, "host %d: traffic must not create extra poll rounds", i)
		}
		require.NoError(t, session.Close())
		synctest.Wait()
	})
}

func TestValidationProgress_ReadyVoteDoesNotStartAnotherPoll(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		session, keys, nonce := pendingValidationSession(t)
		var polls [3]int
		for i, client := range session.clients {
			session.clients[i] = &controlledValidationMempool{
				InProcessClient: client.(*InProcessClient),
				fetch: func(context.Context) ([]*types.DevshardTx, error) {
					polls[i]++
					return nil, nil
				},
			}
		}
		session.ArmQuietSettle()
		synctest.Wait()
		time.Sleep(4 * time.Second)
		synctest.Wait()
		before := polls
		vote := &types.MsgValidationVote{InferenceId: nonce, VoterSlot: 2, VoteValid: false, EscrowId: "escrow-1"}
		vote.ProposerSig = testutil.SignProposerTx(t, keys[2], vote)
		require.NoError(t, session.ProcessResponse(2, &host.HostResponse{
			Nonce: session.Nonce(), Mempool: []*types.DevshardTx{{Tx: &types.DevshardTx_ValidationVote{ValidationVote: vote}}},
		}, nonce))
		synctest.Wait()
		require.Equal(t, types.StatusInvalidated, mustInference(t, session, nonce).Status,
			"an available signed vote must be delivered without waiting for the poll timer")
		require.Equal(t, before, polls, "delivering a vote must not query all peers again")
		require.NoError(t, session.Close())
		synctest.Wait()
	})
}

func TestValidationProgress_NewVoteShortensPollDelay(t *testing.T) {
	for _, tc := range []struct {
		name   string
		voteAt time.Duration
		want   []time.Duration
	}{
		{"shorten_long_wait", 4 * time.Second, []time.Duration{0, time.Second, 3 * time.Second, 5 * time.Second, 7 * time.Second, 12 * time.Second}},
		{"keep_earlier_poll", 7500 * time.Millisecond, []time.Duration{0, time.Second, 3 * time.Second, 8 * time.Second, 10 * time.Second, 15 * time.Second}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				session, keys, nonce := pendingValidationSessionWithHosts(t, 5)
				polls := make([][]time.Time, len(session.clients))
				for i, client := range session.clients {
					session.clients[i] = &controlledValidationMempool{
						InProcessClient: client.(*InProcessClient),
						fetch: func(context.Context) ([]*types.DevshardTx, error) {
							polls[i] = append(polls[i], time.Now())
							return nil, nil
						},
					}
				}
				start := time.Now()
				session.ArmQuietSettle()
				synctest.Wait()
				time.Sleep(tc.voteAt)
				synctest.Wait()
				vote := &types.MsgValidationVote{InferenceId: nonce, VoterSlot: 2, VoteValid: false, EscrowId: "escrow-1"}
				vote.ProposerSig = testutil.SignProposerTx(t, keys[2], vote)
				tx := &types.DevshardTx{Tx: &types.DevshardTx_ValidationVote{ValidationVote: vote}}
				require.NoError(t, session.ProcessResponse(2, &host.HostResponse{
					Nonce: session.Nonce(), Mempool: []*types.DevshardTx{tx},
				}, nonce))
				synctest.Wait()
				require.Equal(t, types.StatusChallenged, mustInference(t, session, nonce).Status)
				require.Equal(t, uint32(2), mustInference(t, session, nonce).VotesInvalid)
				for i := 0; i < 10; i++ {
					time.Sleep(100 * time.Millisecond)
					require.NoError(t, session.ProcessResponse(2, &host.HostResponse{
						Nonce: session.Nonce(), Mempool: []*types.DevshardTx{tx},
					}, nonce))
					synctest.Wait()
				}
				time.Sleep(2 * time.Second)
				synctest.Wait()
				time.Sleep(5 * time.Second)
				synctest.Wait()
				for i, times := range polls {
					got := make([]time.Duration, len(times))
					for j, at := range times {
						got[j] = at.Sub(start)
					}
					require.Equal(t, tc.want, got, "host %d: new votes shorten the wait; duplicates do not postpone it", i)
				}
				require.NoError(t, session.Close())
				synctest.Wait()
			})
		})
	}
}

func pendingValidationSessionForSlots(t *testing.T, keys []*signing.Secp256k1Signer, count int) (*Session, []uint64) {
	t.Helper()
	group := testutil.MakeGroup(keys)
	config := testutil.DefaultConfig(len(group))
	user := testutil.MustGenerateKey(t)
	verifier := signing.NewSecp256k1Verifier()
	byAddress := make(map[string]*InProcessClient)
	clients := make([]HostClient, len(group))
	for i, key := range keys {
		client := byAddress[key.Address()]
		if client == nil {
			sm := newTestStateMachine(t, "escrow-1", config, group, 1_000_000, user.Address(), verifier)
			h, err := host.NewHost(sm, key, stub.NewInferenceEngine(), "escrow-1", group, nil, host.WithGrace(0))
			require.NoError(t, err)
			client = &InProcessClient{Host: h}
			byAddress[key.Address()] = client
		}
		clients[i] = client
	}
	sm := newTestStateMachine(t, "escrow-1", config, group, 1_000_000, user.Address(), verifier)
	session, err := NewSession(sm, user, "escrow-1", group, clients, verifier,
		WithDisableHeightSyncHeartbeat(true), WithQuietSettleDelay(time.Hour), WithVerifierQueue(newVerifierHostQueue()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	var ids []uint64
	for i := 0; i < count; i++ {
		_, err := session.SendInference(context.Background(), InferenceParams{
			Model: "llama", Prompt: testutil.TestPrompt,
			InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: time.Now().Unix(),
		})
		require.NoError(t, err)
		ids = append(ids, session.Nonce())
	}
	initial := &types.MsgValidation{InferenceId: ids[0], ValidatorSlot: 0, Valid: false, EscrowId: "escrow-1"}
	initial.ProposerSig = testutil.SignProposerTx(t, keys[0], initial)
	session.mu.Lock()
	session.addPendingTx(&types.DevshardTx{Tx: &types.DevshardTx_Validation{Validation: initial}})
	session.mu.Unlock()
	return session, ids
}

func TestValidationProgress_UnrelatedAndDuplicateMessagesKeepPollSchedule(t *testing.T) {
	for _, alias := range []bool{false, true} {
		name := "positive_validation_of_another_inference"
		if alias {
			name = "same_validator_through_another_slot"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				keys := make([]*signing.Secp256k1Signer, 5)
				for i := range keys {
					keys[i] = testutil.MustGenerateKey(t)
				}
				count := 2
				if alias {
					keys[2] = keys[0]
					count = 1
				}
				session, ids := pendingValidationSessionForSlots(t, keys, count)
				polls := make([][]time.Time, len(keys))
				for i, client := range session.clients {
					session.clients[i] = &controlledValidationMempool{
						InProcessClient: client.(*InProcessClient),
						fetch: func(context.Context) ([]*types.DevshardTx, error) {
							polls[i] = append(polls[i], time.Now())
							return nil, nil
						},
					}
				}
				start := time.Now()
				session.ArmQuietSettle()
				synctest.Wait()
				before := mustInference(t, session, ids[0])
				require.Equal(t, types.StatusChallenged, before.Status)
				time.Sleep(4 * time.Second)
				synctest.Wait()
				msg := &types.MsgValidation{InferenceId: ids[len(ids)-1], ValidatorSlot: 0, Valid: true, EscrowId: "escrow-1"}
				if alias {
					msg.ValidatorSlot = 2
					msg.Valid = false
				}
				msg.ProposerSig = testutil.SignProposerTx(t, keys[msg.ValidatorSlot], msg)
				nonceBefore := session.Nonce()
				require.NoError(t, session.ProcessResponse(int(msg.ValidatorSlot), &host.HostResponse{
					Nonce: nonceBefore, Mempool: []*types.DevshardTx{{Tx: &types.DevshardTx_Validation{Validation: msg}}},
				}, msg.InferenceId))
				synctest.Wait()
				require.Greater(t, session.Nonce(), nonceBefore, "the signed message must actually land in a diff")
				after := mustInference(t, session, ids[0])
				require.Equal(t, before.Status, after.Status)
				require.Equal(t, before.VotesValid, after.VotesValid)
				require.Equal(t, before.VotesInvalid, after.VotesInvalid)
				if !alias {
					other := mustInference(t, session, ids[1])
					require.Equal(t, types.StatusFinished, other.Status)
					require.Equal(t, uint32(1), other.VotesValid)
				}
				time.Sleep(5 * time.Second)
				synctest.Wait()
				for i, times := range polls {
					if alias && i == 2 {
						continue // Both slots route to the same physical participant.
					}
					got := make([]time.Duration, len(times))
					for j, at := range times {
						got[j] = at.Sub(start)
					}
					require.Equal(t, []time.Duration{0, time.Second, 3 * time.Second, 8 * time.Second}, got,
						"host %d: messages that do not advance a dispute must keep its poll schedule", i)
				}
				require.NoError(t, session.Close())
				synctest.Wait()
			})
		})
	}
}
