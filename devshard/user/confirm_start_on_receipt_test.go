package user

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"devshard/host"
	"devshard/internal/testutil"
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
	session, _, _ := setupSession(t, 3, 100000, 10)
	holding := &holdingClient{receiptSent: make(chan struct{}), release: make(chan struct{}), receipt: []byte("receipt")}
	session.clients[1] = holding

	prepared, err := session.PrepareInference(InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	})
	require.NoError(t, err)
	require.Equal(t, 1, prepared.HostIdx())

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
	session, _, _ := setupSession(t, 3, 100000, 10)
	holding := &holdingClient{receiptSent: make(chan struct{}), release: make(chan struct{}), receipt: []byte("receipt")}
	session.clients[1] = holding

	prepared, err := session.PrepareInference(InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	})
	require.NoError(t, err)

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
	session, _, _ := setupSession(t, 3, 100000, 10)
	prepared, err := session.PrepareInference(InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	})
	require.NoError(t, err)

	session.confirmStartOnReceipt(prepared.Nonce(), &host.HostResponse{Receipt: []byte("receipt")})

	reason, _ := session.TimeoutDeadline(prepared.Nonce(), time.Now())
	require.Equal(t, "refused", reason, "no confirmation stamp means the chain was told nothing to match")
}

func TestALaterResponseWithoutAStampDoesNotEraseTheConfirmation(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 10)
	prepared, err := session.PrepareInference(InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	})
	require.NoError(t, err)

	session.confirmStartOnReceipt(prepared.Nonce(), &host.HostResponse{Receipt: []byte("receipt"), ConfirmedAt: 1000})
	session.confirmStartOnReceipt(prepared.Nonce(), &host.HostResponse{Receipt: []byte("receipt")})

	reason, _ := session.TimeoutDeadline(prepared.Nonce(), time.Now())
	require.Equal(t, "execution", reason,
		"the chain still holds the confirmation, so the vote must keep matching it")
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
	require.Contains(t, catchUpErr.Error(), "host 2")
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
