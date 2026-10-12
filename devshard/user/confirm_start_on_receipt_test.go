package user

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/transport"
	"devshard/types"
	"net/http"

	"github.com/stretchr/testify/require"
)

// holdingClient answers the receipt and then blocks, the way a host that keeps an empty stream open
// does. The gateway's other nonces pass while it holds, which is exactly when the receipt must already
// be queued: it rides the next diff, and after the last one there is none left to ride.
type holdingClient struct {
	receiptSent chan struct{}
	release     chan struct{}
	receipt     []byte
}

func (c *holdingClient) Send(_ context.Context, _ host.HostRequest, _ io.Writer, receiptHandler func(*host.HostResponse)) (*host.HostResponse, error) {
	response := &host.HostResponse{Receipt: c.receipt, ConfirmedAt: 1000}
	if receiptHandler != nil {
		receiptHandler(response)
	}
	close(c.receiptSent)
	<-c.release
	return response, nil
}

func (c *holdingClient) VerifyTimeout(context.Context, uint64, types.TimeoutReason, *host.InferencePayload, []types.Diff) (bool, []byte, uint32, error) {
	return false, nil, 0, nil
}

func (c *holdingClient) ChallengeReceipt(context.Context, uint64, *host.InferencePayload, []types.Diff) ([]byte, error) {
	return nil, nil
}

func pendingConfirmStarts(session *Session) []uint64 {
	var confirmed []uint64
	for _, tx := range session.PendingTxs() {
		if inner := tx.GetConfirmStart(); inner != nil {
			confirmed = append(confirmed, inner.InferenceId)
		}
	}
	return confirmed
}

func TestTheExecutorReceiptIsQueuedWhileTheStreamIsStillOpen(t *testing.T) {
	session, hosts, _ := setupSession(t, 3, 100000, 10)
	holding := &holdingClient{receiptSent: make(chan struct{}), release: make(chan struct{})}
	session.clients[1] = holding

	prepared, err := session.PrepareInference(InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	})
	require.NoError(t, err)
	require.Equal(t, 1, prepared.HostIdx())
	holding.receipt = signedExecutorReceipt(t, session, hosts, prepared.Nonce(), 1000)

	sent := make(chan struct{})
	go func() {
		defer close(sent)
		_, _ = session.SendOnly(context.Background(), prepared, nil, nil)
	}()
	<-holding.receiptSent

	require.Equal(t, []uint64{prepared.Nonce()}, pendingConfirmStarts(session),
		"the receipt must ride the next diff, and a host holding the stream can outlast every diff left")

	close(holding.release)
	<-sent
}

func TestAResponseWithoutAReceiptQueuesNothing(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 10)

	session.confirmStartOnReceipt(1, &host.HostResponse{})
	session.confirmStartOnReceipt(1, nil)

	require.Empty(t, pendingConfirmStarts(session))
}

func TestTheReceiptDecidesTheTimeoutReasonAsSoonAsItArrives(t *testing.T) {
	session, hosts, _ := setupSession(t, 3, 100000, 10)
	holding := &holdingClient{receiptSent: make(chan struct{}), release: make(chan struct{})}
	session.clients[1] = holding

	prepared, err := session.PrepareInference(InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	})
	require.NoError(t, err)
	holding.receipt = signedExecutorReceipt(t, session, hosts, prepared.Nonce(), 1000)

	sent := make(chan struct{})
	go func() {
		defer close(sent)
		_, _ = session.SendOnly(context.Background(), prepared, nil, nil)
	}()
	<-holding.receiptSent

	reason, _ := session.TimeoutDeadline(prepared.Nonce(), time.Now())
	require.Equal(t, "execution", reason,
		"the chain was told the inference started, so a refusal vote here is one every verifier rejects")

	close(holding.release)
	<-sent
}

func TestAResponseWithoutAConfirmedAtLeavesTheReasonAlone(t *testing.T) {
	session, hosts, _ := setupSession(t, 3, 100000, 10)
	prepared, err := session.PrepareInference(InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	})
	require.NoError(t, err)

	session.confirmStartOnReceipt(prepared.Nonce(), &host.HostResponse{
		Receipt: signedExecutorReceipt(t, session, hosts, prepared.Nonce(), 0),
	})
	require.Empty(t, pendingConfirmStarts(session), "a receipt with no confirmation time must not be queued")

	reason, _ := session.TimeoutDeadline(prepared.Nonce(), time.Now())
	require.Equal(t, "refused", reason, "no confirmation stamp means the chain was told nothing to match")
}

func TestALaterResponseWithoutAStampDoesNotEraseTheConfirmation(t *testing.T) {
	session, hosts, _ := setupSession(t, 3, 100000, 10)
	prepared, err := session.PrepareInference(InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	})
	require.NoError(t, err)

	session.confirmStartOnReceipt(prepared.Nonce(), &host.HostResponse{
		Receipt: signedExecutorReceipt(t, session, hosts, prepared.Nonce(), 1000), ConfirmedAt: 1000,
	})
	session.confirmStartOnReceipt(prepared.Nonce(), &host.HostResponse{Receipt: []byte("receipt")})

	reason, _ := session.TimeoutDeadline(prepared.Nonce(), time.Now())
	require.Equal(t, "execution", reason,
		"the chain still holds the confirmation, so the vote must keep matching it")
}

type voteCounter struct {
	calls atomic.Int32
}

func (c *voteCounter) Send(context.Context, host.HostRequest, io.Writer, func(*host.HostResponse)) (*host.HostResponse, error) {
	return &host.HostResponse{}, nil
}

func (c *voteCounter) VerifyTimeout(context.Context, uint64, types.TimeoutReason, *host.InferencePayload, []types.Diff) (bool, []byte, uint32, error) {
	c.calls.Add(1)
	return false, nil, 0, nil
}

// useBubbleConfirmWait replaces the session's wake channel inside a synctest bubble. A channel
// created outside the bubble does not count as a durable block, so Wait would never return.
func useBubbleConfirmWait(session *Session) {
	session.mu.Lock()
	session.confirmWait = make(chan struct{})
	session.mu.Unlock()
}

func TestAReceiptEndsTheRefusalWait(t *testing.T) {
	session, hosts, _ := setupSession(t, 3, 100000, 10)
	nonce := prepareNonce(t, session)

	synctest.Test(t, func(t *testing.T) {
		useBubbleConfirmWait(session)
		done := make(chan bool, 1)
		go func() {
			done <- session.sleepRefusal(context.Background(), nonce, time.Now(), time.Now().Add(time.Minute), nil)
		}()
		synctest.Wait()
		confirmedAt := time.Now().Unix()
		session.confirmStartOnReceipt(nonce, &host.HostResponse{
			Receipt: signedExecutorReceipt(t, session, hosts, nonce, confirmedAt), ConfirmedAt: confirmedAt,
		})
		synctest.Wait()

		require.False(t, <-done, "a confirmed inference is not a refusal, so the wait has to stop")
	})
}

func TestAForgedReceiptDoesNotEndTheRefusalWait(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 10)
	nonce := prepareNonce(t, session)

	synctest.Test(t, func(t *testing.T) {
		useBubbleConfirmWait(session)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan bool, 1)
		go func() {
			done <- session.sleepRefusal(ctx, nonce, time.Now(), time.Now().Add(time.Minute), nil)
		}()
		synctest.Wait()
		session.confirmStartOnReceipt(nonce, &host.HostResponse{Receipt: []byte("forged"), ConfirmedAt: time.Now().Unix()})
		synctest.Wait()
		select {
		case <-done:
			t.Fatal("a receipt that is not the executor's signature must not stop the refusal")
		default:
		}
		reason, _ := session.TimeoutDeadline(nonce, time.Now())
		require.Equal(t, "refused", reason)

		cancel()
		synctest.Wait()
		require.False(t, <-done)
	})
}

func TestAPendingInferenceWaitsOutTheRefusalDeadline(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 10)
	nonce := prepareNonce(t, session)

	require.True(t, session.sleepRefusal(context.Background(), nonce, time.Unix(0, 0), time.Unix(0, 0), nil),
		"nothing confirmed it, so the deadline still opens the refusal challenge")
}

func TestHandleTimeoutDoesNotChallengeARefusalAfterTheReceipt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		confirm func(t *testing.T, session *Session, hosts []*signing.Secp256k1Signer, nonce uint64)
	}{
		{"stream receipt", func(t *testing.T, session *Session, hosts []*signing.Secp256k1Signer, nonce uint64) {
			t.Helper()
			confirmedAt := time.Now().Unix()
			session.confirmStartOnReceipt(nonce, &host.HostResponse{
				Receipt: signedExecutorReceipt(t, session, hosts, nonce, confirmedAt), ConfirmedAt: confirmedAt,
			})
		}},
		{"finished response", func(t *testing.T, session *Session, hosts []*signing.Secp256k1Signer, nonce uint64) {
			t.Helper()
			confirmedAt := time.Now().Unix()
			require.NoError(t, session.ProcessResponse(0, &host.HostResponse{
				Receipt: signedExecutorReceipt(t, session, hosts, nonce, confirmedAt), ConfirmedAt: confirmedAt,
			}, nonce))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session, hosts, _ := setupSession(t, 3, 100000, 10)
			prepared, err := session.PrepareInference(InferenceParams{
				Model: "llama", Prompt: testutil.TestPrompt,
				InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: time.Now().Add(-time.Hour).Unix(),
			})
			require.NoError(t, err)
			counter := &voteCounter{}
			for i := range session.clients {
				session.clients[i] = counter
			}
			payload := &host.InferencePayload{
				Prompt: testutil.TestPrompt, Model: "llama",
				InputLength: 100, MaxTokens: testutil.TestMaxTokens,
				StartedAt: time.Now().Add(-time.Hour).Unix(),
			}

			synctest.Test(t, func(t *testing.T) {
				useBubbleConfirmWait(session)
				done := make(chan TimeoutResult, 1)
				go func() {
					result, _ := session.HandleTimeout(context.Background(), prepared.Nonce(), time.Now(), payload)
					done <- result
				}()
				synctest.Wait()
				tc.confirm(t, session, hosts, prepared.Nonce())
				synctest.Wait()

				result := <-done
				require.Equal(t, "skipped", result.Outcome)
				require.Equal(t, "confirmed_before_vote", result.DetailReason)
				require.Empty(t, result.Reason, "a challenge that was not sent is not a refused timeout")
				require.Zero(t, counter.calls.Load(), "verifiers must not be asked for a refusal against a started inference")
			})
		})
	}
}

func TestHandleTimeoutStillChallengesAPendingInference(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 10)
	startedAt := time.Now().Add(-time.Hour)
	prepared, err := session.PrepareInference(InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: startedAt.Unix(),
	})
	require.NoError(t, err)
	counter := &voteCounter{}
	for i := range session.clients {
		session.clients[i] = counter
	}

	result, err := session.HandleTimeout(context.Background(), prepared.Nonce(), startedAt, &host.InferencePayload{
		Prompt: testutil.TestPrompt, Model: "llama",
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: startedAt.Unix(),
	})

	require.Error(t, err)
	require.Equal(t, "refused", result.Reason)
	require.NotEqual(t, "confirmed_before_vote", result.DetailReason)
	require.Equal(t, int32(2), counter.calls.Load(), "a still-pending inference is a refusal, and both verifiers are asked")
}

func TestAReceiptWithoutAStampDoesNotEndTheRefusalWait(t *testing.T) {
	session, hosts, _ := setupSession(t, 3, 100000, 10)
	nonce := prepareNonce(t, session)

	synctest.Test(t, func(t *testing.T) {
		useBubbleConfirmWait(session)
		done := make(chan bool, 1)
		go func() {
			done <- session.sleepRefusal(context.Background(), nonce, time.Now(), time.Now().Add(time.Minute), nil)
		}()
		synctest.Wait()
		session.confirmStartOnReceipt(nonce, &host.HostResponse{Receipt: []byte("receipt")})
		synctest.Wait()
		select {
		case <-done:
			t.Fatal("a receipt without a confirmation stamp leaves the inference pending")
		default:
		}

		confirmedAt := time.Now().Unix()
		session.confirmStartOnReceipt(nonce, &host.HostResponse{
			Receipt: signedExecutorReceipt(t, session, hosts, nonce, confirmedAt), ConfirmedAt: confirmedAt,
		})
		synctest.Wait()
		require.False(t, <-done)
	})
}

func TestAReceiptForAnotherNonceLeavesTheRefusalWait(t *testing.T) {
	session, hosts, _ := setupSession(t, 3, 100000, 10)
	nonce := prepareNonce(t, session)
	other := prepareNonce(t, session)

	synctest.Test(t, func(t *testing.T) {
		useBubbleConfirmWait(session)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan bool, 1)
		go func() {
			done <- session.sleepRefusal(ctx, nonce, time.Now(), time.Now().Add(time.Minute), nil)
		}()
		synctest.Wait()
		confirmedAt := time.Now().Unix()
		session.confirmStartOnReceipt(other, &host.HostResponse{
			Receipt: signedExecutorReceipt(t, session, hosts, other, confirmedAt), ConfirmedAt: confirmedAt,
		})
		synctest.Wait()
		select {
		case <-done:
			t.Fatal("another inference's receipt is not a confirmation of this one")
		default:
		}

		cancel()
		synctest.Wait()
		require.False(t, <-done)
	})
}

func TestHandleTimeoutCancelDuringTheRefusalWaitDoesNotChallenge(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 10)
	prepared, err := session.PrepareInference(InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: time.Now().Unix(),
	})
	require.NoError(t, err)
	counter := &voteCounter{}
	for i := range session.clients {
		session.clients[i] = counter
	}

	synctest.Test(t, func(t *testing.T) {
		useBubbleConfirmWait(session)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan TimeoutResult, 1)
		go func() {
			result, _ := session.HandleTimeout(ctx, prepared.Nonce(), time.Now(), &host.InferencePayload{
				Prompt: testutil.TestPrompt, Model: "llama",
				InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: time.Now().Unix(),
			})
			done <- result
		}()
		synctest.Wait()
		cancel()
		synctest.Wait()

		result := <-done
		require.Equal(t, "skipped", result.Outcome)
		require.Equal(t, "context_canceled", result.DetailReason)
		require.Zero(t, counter.calls.Load())
	})
}

type admissionRefusingClient struct {
	InProcessClient
	refusals int
}

func (c *admissionRefusingClient) WithoutAdmission() any { return &c.InProcessClient }

func (c *admissionRefusingClient) Send(context.Context, host.HostRequest, io.Writer, func(*host.HostResponse)) (*host.HostResponse, error) {
	c.refusals++
	return nil, errors.New("participant request budget exhausted")
}

func TestTheTimeoutDiffBypassesTheParticipantBudget(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 10)
	// A diff composed at nonce 0 binds to slot 1, so that is the client the budget would refuse.
	refusing := &admissionRefusingClient{InProcessClient: *session.clients[1].(*InProcessClient)}
	session.clients[1] = refusing

	_, err := session.sendPendingDiff(context.Background())

	require.NoError(t, err, "the vote was already cast; the participant budget must not be able to refuse its diff")
	require.Zero(t, refusing.refusals, "the admission-gated client must not be the one that carries it")
}

// A host locked out by the budget cannot sign or vote until it holds the diffs.
func TestTheCatchUpBypassesTheParticipantBudget(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 10)
	// An empty catch-up returns before any client, so the refusal needs a diff to be reachable.
	_, err := session.sendPendingDiff(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, session.Diffs(), "the catch-up needs a diff to carry")

	refusing := &admissionRefusingClient{InProcessClient: *session.clients[1].(*InProcessClient)}
	session.clients[1] = refusing
	session.mu.Lock()
	session.hostSyncNonce[1] = 0
	session.mu.Unlock()

	require.NoError(t, session.sendCatchUp(context.Background(), 1),
		"these diffs are already signed by the group; the participant budget must not be able to refuse them")
	require.Zero(t, refusing.refusals, "the admission-gated client must not be the one that carries them")
}

func sessionNotFound() error {
	return &transport.UpstreamStatusError{
		Path:       "/sessions/1/verify-timeout",
		StatusCode: http.StatusNotFound,
		Body:       `{"message":"session not found"}`,
	}
}

func TestAHostThatLostTheEscrowIsTaughtItAgain(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 10)
	session.mu.Lock()
	session.hostSyncNonce[2] = 40
	session.mu.Unlock()

	session.forgetHostState(2, sessionNotFound())

	session.mu.Lock()
	defer session.mu.Unlock()
	require.Zero(t, session.hostSyncNonce[2],
		"catch-up sends only what follows the cursor, so a host that lost its storage stays cold until it rewinds")
}

func TestAnOrdinaryVoteFailureLeavesTheCursorAlone(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 10)
	session.mu.Lock()
	session.hostSyncNonce[2] = 40
	session.mu.Unlock()

	session.forgetHostState(2, errors.New("context deadline exceeded"))
	session.forgetHostState(2, &transport.UpstreamStatusError{
		Path: "/sessions/1/verify-timeout", StatusCode: http.StatusInternalServerError,
		Body: `{"message":"inference 8: expected started, got 0"}`,
	})
	// A 404 the network really produces, and it says the host runs another protocol, not that it lost state.
	session.forgetHostState(2, &transport.UpstreamStatusError{
		Path: "/sessions/1/verify-timeout", StatusCode: http.StatusNotFound,
		Body: `version "v3" not found`,
	})

	session.mu.Lock()
	defer session.mu.Unlock()
	require.Equal(t, uint64(40), session.hostSyncNonce[2],
		"a host that disagrees about one nonce still holds the escrow; replaying everything to it is waste")
}

type lostSessionVerifier struct{}

func (lostSessionVerifier) VerifyTimeout(context.Context, uint64, types.TimeoutReason, *host.InferencePayload, []types.Diff) (bool, []byte, uint32, error) {
	return false, nil, 0, sessionNotFound()
}

func TestAVerifierThatLostTheEscrowRewindsItsCursorDuringCollection(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 10)
	session.mu.Lock()
	session.hostSyncNonce[2] = 40
	session.mu.Unlock()

	_, err := session.CollectTimeoutVotes(context.Background(), 1,
		types.TimeoutReason_TIMEOUT_REASON_REFUSED,
		&host.InferencePayload{
			Prompt: testutil.TestPrompt, Model: "llama",
			InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
		},
		map[int]TimeoutVerifier{2: lostSessionVerifier{}}, nil)
	require.NoError(t, err)

	session.mu.Lock()
	defer session.mu.Unlock()
	require.Zero(t, session.hostSyncNonce[2],
		"the vote handler is where the gateway learns a host lost the escrow; noticing it anywhere else is too late")
}

func TestTheRewindStopsAtTheOldestDiffTheSessionStillHolds(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 10)
	session.mu.Lock()
	// A restarted session backfills only from the group's lowest cursor, so its history starts mid-chain.
	session.diffs = []types.Diff{{Nonce: 700}, {Nonce: 701}}
	session.hostSyncNonce[2] = 740
	session.mu.Unlock()

	session.forgetHostState(2, sessionNotFound())

	session.mu.Lock()
	defer session.mu.Unlock()
	require.Equal(t, uint64(699), session.hostSyncNonce[2],
		"rewinding past the held history would hand the host a chain missing its own beginning")
}

func TestAHostAlreadyBehindTheHistoryIsLeftAlone(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 10)
	session.mu.Lock()
	session.diffs = []types.Diff{{Nonce: 700}}
	session.hostSyncNonce[2] = 500
	session.mu.Unlock()

	session.forgetHostState(2, sessionNotFound())

	session.mu.Lock()
	defer session.mu.Unlock()
	require.Equal(t, uint64(500), session.hostSyncNonce[2],
		"there is nothing further back to give it, so moving the cursor buys nothing")
}

type refusingCatchUpClient struct {
	InProcessClient
	refusal error
}

func (c *refusingCatchUpClient) Send(context.Context, host.HostRequest, io.Writer, func(*host.HostResponse)) (*host.HostResponse, error) {
	return nil, c.refusal
}

func TestCatchUpReportsAHostThatRefusedTheDiffs(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 10)
	_, err := session.SendInference(context.Background(), warmupParams())
	require.NoError(t, err)
	session.clients[2] = &refusingCatchUpClient{
		InProcessClient: *session.clients[2].(*InProcessClient),
		refusal: &transport.UpstreamStatusError{
			Path: "/sessions/1/chat/completions", StatusCode: http.StatusConflict,
			Body: `{"message":"session version conflict: stored v3, host v4"}`,
		},
	}

	catchUpErr := session.CatchUpAllHosts(context.Background())

	require.Error(t, catchUpErr,
		"a caller told the group is caught up treats every host as holding the escrow, and this one does not")
	require.Contains(t, catchUpErr.Error(), "host "+session.HostLabel(2))
}

type countingClient struct {
	InProcessClient
	sends atomic.Int64
}

func (c *countingClient) Send(ctx context.Context, req host.HostRequest, stream io.Writer, onReceipt func(*host.HostResponse)) (*host.HostResponse, error) {
	c.sends.Add(1)
	return c.InProcessClient.Send(ctx, req, stream, onReceipt)
}

func TestOneRefusingHostDoesNotStopTheOthersFromSyncing(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 10)
	_, err := session.SendInference(context.Background(), warmupParams())
	require.NoError(t, err)

	refusing := &refusingCatchUpClient{
		InProcessClient: *session.clients[0].(*InProcessClient),
		refusal:         errors.New("session version conflict"),
	}
	counting := &countingClient{InProcessClient: *session.clients[2].(*InProcessClient)}
	session.clients[0] = refusing
	session.clients[2] = counting

	syncErr := session.SyncHosts(context.Background())

	require.Error(t, syncErr, "the caller must learn which hosts did not catch up")
	require.NotZero(t, counting.sends.Load(),
		"a host after the refusing one must still be reached: stopping there leaves the group half taught")
}

func TestRewindMakesTheNextCatchUpCarryTheWholeHistory(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 10)
	for range 6 {
		_, err := session.PrepareInference(InferenceParams{
			Model: "llama", Prompt: testutil.TestPrompt,
			InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
		})
		require.NoError(t, err)
	}
	session.mu.Lock()
	session.hostSyncNonce[2] = 4
	before := len(session.diffsForHost(2))
	session.mu.Unlock()
	require.NotZero(t, before, "precondition: the cursor hides part of a non-empty history")

	require.True(t, session.RewindHostCatchUp(2, "escrow_state_root_diverged"))

	session.mu.Lock()
	defer session.mu.Unlock()
	require.Greater(t, len(session.diffsForHost(2)), before,
		"a rewound host has to be handed the chain its cursor claimed it already had")
}

func TestRewindIsANoOpForAHostAlreadyAtTheStartOfTheHistory(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 10)
	session.mu.Lock()
	session.hostSyncNonce[2] = 0
	session.mu.Unlock()

	require.False(t, session.RewindHostCatchUp(2, "escrow_state_root_diverged"),
		"rewinding past the retained history would hand the host a chain missing its own beginning")
}
