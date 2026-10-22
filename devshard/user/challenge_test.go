package user

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/transport"
	"devshard/types"
)

type startedChallenge struct {
	nonce  uint64
	reason types.ChallengeReason
}

func challengeJoinSession(started *[]startedChallenge) (*Session, *[]servedBindingReport) {
	reports := &[]servedBindingReport{}
	session := bindingSession(reports)
	session.SetChallengeHandler(func(nonce uint64, reason types.ChallengeReason) {
		*started = append(*started, startedChallenge{nonce: nonce, reason: reason})
	})
	return session, reports
}

// A stream cut before [DONE] and devshard_meta waits for its Finish; the
// challenge starts when the Finish applies, once, and leaves nothing behind
// for the served-binding sweep to log as unchecked.
func TestACutStreamIsChallengedWhenItsFinishApplies(t *testing.T) {
	var started []startedChallenge
	session, reports := challengeJoinSession(&started)

	session.MarkChallengeStream(5, types.ChallengeReason_CHALLENGE_REASON_STREAM_UNBOUND)
	require.Empty(t, started, "no Finish yet")
	applyFinish(session, 5)
	applyFinish(session, 5)

	require.Equal(t, []startedChallenge{{nonce: 5, reason: types.ChallengeReason_CHALLENGE_REASON_STREAM_UNBOUND}}, started)
	require.Empty(t, *reports, "a cut stream has no hashes to judge")
	session.mu.Lock()
	defer session.mu.Unlock()
	require.NotContains(t, session.appliedFinishHashes, uint64(5))
	require.NotContains(t, session.challengeMarks, uint64(5))
}

// The Finish can apply before the attempt ends; the late mark starts the
// challenge on its own.
func TestAMarkAfterTheFinishStartsTheChallenge(t *testing.T) {
	var started []startedChallenge
	session, _ := challengeJoinSession(&started)

	applyFinish(session, 6)
	require.Empty(t, started)
	require.Contains(t, session.appliedFinishHashes, uint64(6), "the Finish waits for a stream to judge")
	session.MarkChallengeStream(6, types.ChallengeReason_CHALLENGE_REASON_EMPTY_STREAM)

	require.Equal(t, []startedChallenge{{nonce: 6, reason: types.ChallengeReason_CHALLENGE_REASON_EMPTY_STREAM}}, started)
	// The mark says the stream ended without binding: there is no stream left
	// to judge the Finish against, so the retention sweep must not report it
	// as unchecked later.
	require.NotContains(t, session.appliedFinishHashes, uint64(6))
	require.NotContains(t, session.challengeFinishes, uint64(6))
}

// A complete stream is judged by the served binding: a mismatch is a stream
// not bound to the Finish and is challenged, a bound stream is not.
func TestACompleteStreamIsChallengedOnlyOnMismatch(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		received [32]byte
		want     []startedChallenge
	}{
		{name: "bound", received: storedSum},
		{name: "mismatch", received: tamperedSum, want: []startedChallenge{{nonce: 7, reason: types.ChallengeReason_CHALLENGE_REASON_STREAM_UNBOUND}}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var started []startedChallenge
			session, _ := challengeJoinSession(&started)
			session.BindReceivedStream(7, [][32]byte{testCase.received})
			applyFinish(session, 7)
			require.Equal(t, testCase.want, started)
		})
	}
}

// An empty receipt-backed stream is challenged as EMPTY_STREAM even when its
// hashes bind, and a mismatch on the same inference does not start a second
// challenge.
func TestAnEmptyStreamIsChallengedOnce(t *testing.T) {
	for _, received := range [][32]byte{storedSum, tamperedSum} {
		var started []startedChallenge
		session, _ := challengeJoinSession(&started)
		session.MarkChallengeStream(8, types.ChallengeReason_CHALLENGE_REASON_EMPTY_STREAM)
		session.BindReceivedStream(8, [][32]byte{received})
		applyFinish(session, 8)
		require.Equal(t, []startedChallenge{{nonce: 8, reason: types.ChallengeReason_CHALLENGE_REASON_EMPTY_STREAM}}, started)
	}
}

// Without a Finish there is nothing to challenge: the record goes by timeout.
func TestACutStreamWithoutFinishIsNotChallenged(t *testing.T) {
	var started []startedChallenge
	session, _ := challengeJoinSession(&started)
	session.MarkChallengeStream(9, types.ChallengeReason_CHALLENGE_REASON_STREAM_UNBOUND)
	applyFinish(session, 10)
	require.Empty(t, started)
}

// scriptedChallengeClient answers VerifyChallenge from a script; the last
// answer repeats.
type scriptedChallengeClient struct {
	HostClient
	mu      sync.Mutex
	answers []*transport.VerifyChallengeResponse
	// errs are returned first, one per ask, before answers; err repeats.
	errs    []error
	err     error
	asks    int
	askedAt []time.Time
	// onAsk runs at every ask, outside the client's lock: what the session
	// learns while a host is being asked.
	onAsk func()
}

func (c *scriptedChallengeClient) VerifyChallenge(ctx context.Context, _ uint64, reason types.ChallengeReason, _ []types.Diff) (*transport.VerifyChallengeResponse, error) {
	if c.onAsk != nil {
		c.onAsk()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.asks++
	c.askedAt = append(c.askedAt, time.Now())
	if reason == types.ChallengeReason_CHALLENGE_REASON_UNSPECIFIED {
		return nil, errors.New("unspecified reason")
	}
	if len(c.errs) > 0 {
		err := c.errs[0]
		c.errs = c.errs[1:]
		return nil, err
	}
	if c.err != nil {
		return nil, c.err
	}
	if len(c.answers) == 0 {
		// Nothing scripted: the ask hangs until the gateway gives up on it.
		c.mu.Unlock()
		<-ctx.Done()
		c.mu.Lock()
		return nil, ctx.Err()
	}
	i := c.asks - 1
	if i >= len(c.answers) {
		i = len(c.answers) - 1
	}
	answer := *c.answers[i]
	return &answer, nil
}

func (c *scriptedChallengeClient) askCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.asks
}

func (c *scriptedChallengeClient) askTimes() []time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Time(nil), c.askedAt...)
}

func signedChallengeAnswer(t *testing.T, signer *signing.Secp256k1Signer, nonce uint64, slot uint32, fault types.ChallengeFault, responseHash []byte) *transport.VerifyChallengeResponse {
	t.Helper()
	vote := testutil.SignChallengeVote(t, signer, "escrow-1", nonce, slot, fault, types.ChallengeCheck_CHALLENGE_CHECK_HASH, 0, responseHash)
	return &transport.VerifyChallengeResponse{
		Fault: vote.Fault, Check: vote.Check, Deviation: vote.Deviation, Signature: vote.Signature, VoterSlot: vote.VoterSlot,
	}
}

// finishedChallengeSession runs one inference on a three-host session to
// Finished and returns the executor index and the Finish response hash.
func finishedChallengeSession(t *testing.T) (*Session, []*signing.Secp256k1Signer, uint64, int, []byte) {
	t.Helper()
	return finishedChallengeSessionOf(t, 3)
}

// finishedChallengeSessionOf is finishedChallengeSession on numHosts hosts;
// the test config's vote threshold is numHosts / 2.
func finishedChallengeSessionOf(t *testing.T, numHosts int) (*Session, []*signing.Secp256k1Signer, uint64, int, []byte) {
	t.Helper()
	session, hosts, _ := setupSession(t, numHosts, 100000, 10)
	nonce, execIdx, responseHash := finishChallengeInference(t, session, hosts, numHosts)
	return session, hosts, nonce, execIdx, responseHash
}

// finishedChallengeSessionSealingEveryNonce is finishedChallengeSession on a
// session that evaluates the auto-seal at every nonce with a one-nonce grace:
// a record that turns terminal in a diff is sealed in that same diff.
func finishedChallengeSessionSealingEveryNonce(t *testing.T) (*Session, []*signing.Secp256k1Signer, uint64, int, []byte) {
	t.Helper()
	config := testutil.DefaultConfig(3)
	config.AutoSealEveryNNonces = 1
	config.InferenceSealGraceNonces = 1
	session, hosts, _ := setupSessionWithConfigAndEngine(t, 3, 100000, 10, config, nil)
	nonce, execIdx, responseHash := finishChallengeInference(t, session, hosts, 3)
	return session, hosts, nonce, execIdx, responseHash
}

// finishChallengeInference starts, confirms and finishes one inference on the
// session and returns its nonce, executor and response hash.
func finishChallengeInference(t *testing.T, session *Session, hosts []*signing.Secp256k1Signer, numHosts int) (uint64, int, []byte) {
	t.Helper()
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
	nonce, execIdx := startConfirmForErrorMiss(t, session, hosts, params)
	finishTx, _ := signedErrorFinishTx(t, hosts, nonce, execIdx, []byte(`{"events":["data: [DONE]"]}`))
	session.mu.Lock()
	session.addPendingTx(finishTx)
	session.nonceStates[nonce].finished = true
	session.mu.Unlock()
	require.NoError(t, session.SendPendingDiff(context.Background()))
	rec, ok := session.StateMachine().Inference(nonce)
	require.True(t, ok)
	require.Equal(t, types.StatusFinished, rec.Status)
	require.Equal(t, uint32(numHosts/2), session.StateMachine().VoteThreshold())
	return nonce, execIdx, append([]byte(nil), rec.ResponseHash...)
}

func fastChallengeReask(t *testing.T) {
	t.Helper()
	savedErr, savedPending := ChallengeReaskInterval, ChallengePendingReaskInterval
	ChallengeReaskInterval, ChallengePendingReaskInterval = time.Millisecond, time.Millisecond
	t.Cleanup(func() { ChallengeReaskInterval, ChallengePendingReaskInterval = savedErr, savedPending })
}

// Both other hosts answer pending first and then find the body missing. The
// gateway asks again on pending, never asks the executor, and its MsgChallenge
// carries both votes and invalidates the record.
func TestRunChallenge_ReasksOnPendingAndInvalidates(t *testing.T) {
	fastChallengeReask(t)
	session, hosts, nonce, execIdx, responseHash := finishedChallengeSession(t)
	clients := make([]*scriptedChallengeClient, len(session.clients))
	for i, c := range session.clients {
		clients[i] = &scriptedChallengeClient{HostClient: c, err: errors.New("the executor is not asked")}
		if i != execIdx {
			clients[i].err = nil
			clients[i].answers = []*transport.VerifyChallengeResponse{
				{Pending: true},
				signedChallengeAnswer(t, hosts[i], nonce, uint32(i), types.ChallengeFault_CHALLENGE_FAULT_PAYLOAD_UNAVAILABLE, responseHash),
			}
		}
		session.clients[i] = clients[i]
	}
	result, err := session.RunChallenge(context.Background(), nonce, types.ChallengeReason_CHALLENGE_REASON_STREAM_UNBOUND)
	require.NoError(t, err)
	require.True(t, result.Invalidated)
	require.Len(t, result.Votes, 2)
	require.Equal(t, uint32(2), result.Faults)
	require.Equal(t, uint32(1), result.Threshold)
	require.Zero(t, result.UnansweredWeight)
	require.Equal(t, execIdx, result.ExecutorIdx)
	for i, c := range clients {
		if i == execIdx {
			require.Zero(t, c.askCount(), "the executor must not vote in its own challenge")
			continue
		}
		require.Equal(t, 2, c.askCount(), "pending is asked again")
	}

	rec, _ := session.StateMachine().Inference(nonce)
	require.Equal(t, types.StatusInvalidated, rec.Status)
	require.True(t, rec.UsageChallenged)
	diffs := session.Diffs()
	require.NotEmpty(t, diffs)
	var challenge *types.MsgChallenge
	for _, tx := range diffs[len(diffs)-1].Txs {
		if c := tx.GetChallenge(); c != nil {
			challenge = c
		}
	}
	require.NotNil(t, challenge)
	require.Equal(t, nonce, challenge.InferenceId)
	require.Len(t, challenge.Votes, 2)

	again, err := session.RunChallenge(context.Background(), nonce, types.ChallengeReason_CHALLENGE_REASON_STREAM_UNBOUND)
	require.NoError(t, err)
	require.Empty(t, again.Votes, "an invalidated record is not challenged again")
	for i, c := range clients {
		if i != execIdx {
			require.Equal(t, 2, c.askCount(), "nothing is asked for a record that is not Finished")
		}
	}
}

// The challenge's diff can be the diff that seals the record: an invalidated
// record is terminal, and the auto-seal takes a terminal record as soon as its
// nonce gate clears, in the same apply. The record is then no longer live when
// the gateway reads the outcome back, and the verdict has to come from the
// sealed record: a challenge the state machine applied is not "not applied".
func TestRunChallenge_ReadsTheVerdictOfARecordSealedInItsOwnDiff(t *testing.T) {
	session, hosts, nonce, execIdx, responseHash := finishedChallengeSessionSealingEveryNonce(t)
	for i, c := range session.clients {
		client := &scriptedChallengeClient{HostClient: c, err: errors.New("the executor is not asked")}
		if i != execIdx {
			client.err = nil
			client.answers = []*transport.VerifyChallengeResponse{
				signedChallengeAnswer(t, hosts[i], nonce, uint32(i), types.ChallengeFault_CHALLENGE_FAULT_PAYLOAD_UNAVAILABLE, responseHash),
			}
		}
		session.clients[i] = client
	}
	result, err := session.RunChallenge(context.Background(), nonce, types.ChallengeReason_CHALLENGE_REASON_STREAM_UNBOUND)
	require.NoError(t, err)
	require.True(t, result.Invalidated)
	require.Len(t, result.Votes, 2)

	_, live := session.StateMachine().Inference(nonce)
	require.False(t, live, "the invalidated record is sealed in the challenge's own diff")
	require.Contains(t, session.StateMachine().SealedAtOrBefore([]uint64{nonce}, session.Nonce()), nonce)
	sealed, ok := session.StateMachine().LookupSealedInference(nonce)
	require.True(t, ok)
	require.Equal(t, types.StatusInvalidated, sealed.Status)
	diffs := session.Diffs()
	var challenge *types.MsgChallenge
	for _, tx := range diffs[len(diffs)-1].Txs {
		if c := tx.GetChallenge(); c != nil {
			challenge = c
		}
	}
	require.NotNil(t, challenge, "the MsgChallenge is in the diff that sealed the record")
}

// A collection the caller cancels has no settled outcome: the votes in hand
// are not sent, or a MsgChallenge below the threshold would mark the record
// challenged for good while the hosts still fetching could have invalidated
// it.
func TestRunChallenge_ACancelledCollectionSendsNoPartialVerdict(t *testing.T) {
	fastChallengeReask(t)
	session, hosts, nonce, execIdx, responseHash := finishedChallengeSessionOf(t, 5)
	require.Equal(t, uint32(2), session.StateMachine().VoteThreshold())
	var hanging []*scriptedChallengeClient
	answered := false
	for i, c := range session.clients {
		client := &scriptedChallengeClient{HostClient: c, err: errors.New("the executor is not asked")}
		if i != execIdx {
			client.err = nil
			if !answered {
				answered = true
				client.answers = []*transport.VerifyChallengeResponse{
					signedChallengeAnswer(t, hosts[i], nonce, uint32(i), types.ChallengeFault_CHALLENGE_FAULT_NONE, responseHash),
				}
			} else {
				// Nothing scripted: the ask hangs until it is cancelled.
				hanging = append(hanging, client)
			}
		}
		session.clients[i] = client
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var result ChallengeResult
	var err error
	done := make(chan struct{})
	go func() {
		result, err = session.RunChallenge(ctx, nonce, types.ChallengeReason_CHALLENGE_REASON_STREAM_UNBOUND)
		close(done)
	}()
	require.Eventually(t, func() bool {
		for _, c := range hanging {
			if c.askCount() == 0 {
				return false
			}
		}
		return true
	}, 3*time.Second, 5*time.Millisecond, "every voter is asked")
	cancel()
	<-done

	require.Error(t, err, "a cancelled collection is no outcome")
	require.Len(t, result.Votes, 1, "the one NONE vote was collected")
	for _, diff := range session.Diffs() {
		for _, tx := range diff.Txs {
			require.Nil(t, tx.GetChallenge(), "no MsgChallenge is sent after a cancelled collection")
		}
	}
	rec, ok := session.StateMachine().Inference(nonce)
	require.True(t, ok)
	require.False(t, rec.UsageChallenged, "the record can still be challenged")
}

// The active nonce cap can arrive while the votes are collected. No diff can
// carry the MsgChallenge then, just as nothing would have been asked had the
// cap been there before: the outcome is nonce_cap, not a failure.
func TestRunChallenge_TheCapReachedDuringCollectionIsNonceCap(t *testing.T) {
	session, hosts, nonce, execIdx, responseHash := finishedChallengeSession(t)
	for i, c := range session.clients {
		client := &scriptedChallengeClient{HostClient: c, err: errors.New("the executor is not asked")}
		if i != execIdx {
			client.err = nil
			client.answers = []*transport.VerifyChallengeResponse{
				signedChallengeAnswer(t, hosts[i], nonce, uint32(i), types.ChallengeFault_CHALLENGE_FAULT_PAYLOAD_UNAVAILABLE, responseHash),
			}
			client.onAsk = func() { capAtCurrentNonce(session) }
		}
		session.clients[i] = client
	}
	result, err := session.RunChallenge(context.Background(), nonce, types.ChallengeReason_CHALLENGE_REASON_STREAM_UNBOUND)
	require.NoError(t, err)
	require.True(t, result.NonceCap)
	require.Len(t, result.Votes, 2, "the votes were collected before the cap was seen")
	for _, diff := range session.Diffs() {
		for _, tx := range diff.Txs {
			require.Nil(t, tx.GetChallenge(), "no diff carries the MsgChallenge past the cap")
		}
	}
	rec, ok := session.StateMachine().Inference(nonce)
	require.True(t, ok)
	require.False(t, rec.UsageChallenged)
}

// A voter's reject cause is the voter's own text. It is cut to a line before
// it reaches the gateway's log and the challenge result, so a hostile voter
// cannot fill either with the 10 MiB the answer may carry.
func TestCollectChallengeVotes_CutsALongRejectCause(t *testing.T) {
	fastChallengeReask(t)
	session, _, nonce, execIdx, responseHash := finishedChallengeSession(t)
	long := strings.Repeat("x", 4096)
	verifiers := map[int]ChallengeVerifier{}
	for i, c := range session.clients {
		if i == execIdx {
			continue
		}
		verifiers[i] = &scriptedChallengeClient{HostClient: c, answers: []*transport.VerifyChallengeResponse{{RejectCause: long}}}
	}
	executorAddr := session.StateMachine().SlotAddress(uint32(execIdx))

	_, unanswered, _, causes := session.CollectChallengeVotes(context.Background(), nonce,
		types.ChallengeReason_CHALLENGE_REASON_STREAM_UNBOUND, responseHash, executorAddr, verifiers, time.Now().Add(30*time.Millisecond))
	require.Equal(t, 2, unanswered)
	require.NotEmpty(t, causes)
	for _, cause := range causes {
		require.LessOrEqual(t, len(cause), 200+len("…"), "a line of the cause is kept, not the body")
		require.True(t, strings.HasPrefix(cause, "xxx"), "the head of the cause is kept")
	}
}

// A vote whose signature does not recover to the host that answered is
// dropped: the state machine would reject the whole MsgChallenge for it. The
// honest NONE vote still lands, and the record stays Finished and paid.
func TestRunChallenge_DropsAVoteSignedByAnotherKey(t *testing.T) {
	fastChallengeReask(t)
	session, hosts, nonce, execIdx, responseHash := finishedChallengeSession(t)
	var honest, forger int = -1, -1
	for i := range session.clients {
		if i == execIdx {
			continue
		}
		if honest < 0 {
			honest = i
		} else {
			forger = i
		}
	}
	session.clients[honest] = &scriptedChallengeClient{HostClient: session.clients[honest], answers: []*transport.VerifyChallengeResponse{
		signedChallengeAnswer(t, hosts[honest], nonce, uint32(honest), types.ChallengeFault_CHALLENGE_FAULT_NONE, responseHash),
	}}
	session.clients[forger] = &scriptedChallengeClient{HostClient: session.clients[forger], answers: []*transport.VerifyChallengeResponse{
		signedChallengeAnswer(t, hosts[execIdx], nonce, uint32(forger), types.ChallengeFault_CHALLENGE_FAULT_HASH_MISMATCH, responseHash),
	}}
	balanceBefore := session.StateMachine().SnapshotState().Balance

	result, err := session.RunChallenge(context.Background(), nonce, types.ChallengeReason_CHALLENGE_REASON_EMPTY_STREAM)
	require.NoError(t, err)
	require.False(t, result.Invalidated)
	require.Len(t, result.Votes, 1)
	require.Equal(t, types.ChallengeFault_CHALLENGE_FAULT_NONE, result.Votes[0].Fault)
	require.Zero(t, result.Faults)
	require.Equal(t, 1, result.Unanswered)
	require.Equal(t, uint32(1), result.UnansweredWeight, "the dropped vote counts as unanswered, with its slot weight")

	st := session.StateMachine().SnapshotState()
	require.Equal(t, types.StatusFinished, st.Inferences[nonce].Status)
	require.True(t, st.Inferences[nonce].UsageChallenged)
	require.Equal(t, balanceBefore, st.Balance, "NONE leaves the payout standing")
}

// A host still pending at the gateway deadline and a host that cannot vote
// both count as unanswered; nothing is sent without a vote. While the faults
// in hand and the hosts still out could still pass the threshold, the pending
// host is asked again until the deadline.
func TestCollectChallengeVotes_PendingAtDeadlineAndRejects(t *testing.T) {
	fastChallengeReask(t)
	t.Run("pending is asked until the deadline while the outcome is open", func(t *testing.T) {
		session, hosts, nonce, execIdx, responseHash := finishedChallengeSession(t)
		verifiers := map[int]ChallengeVerifier{}
		var pendingClient *scriptedChallengeClient
		for i, c := range session.clients {
			if i == execIdx {
				continue
			}
			if pendingClient == nil {
				pendingClient = &scriptedChallengeClient{HostClient: c, answers: []*transport.VerifyChallengeResponse{{Pending: true}}}
				verifiers[i] = pendingClient
				continue
			}
			// One fault of weight one does not pass the threshold of one; the
			// pending host's vote could, so it is awaited.
			verifiers[i] = &scriptedChallengeClient{HostClient: c, answers: []*transport.VerifyChallengeResponse{
				signedChallengeAnswer(t, hosts[i], nonce, uint32(i), types.ChallengeFault_CHALLENGE_FAULT_PAYLOAD_UNAVAILABLE, responseHash),
			}}
		}
		executorAddr := session.StateMachine().SlotAddress(uint32(execIdx))

		votes, unanswered, unansweredWeight, _ := session.CollectChallengeVotes(context.Background(), nonce,
			types.ChallengeReason_CHALLENGE_REASON_STREAM_UNBOUND, responseHash, executorAddr, verifiers, time.Now().Add(30*time.Millisecond))
		require.Len(t, votes, 1)
		require.Equal(t, 1, unanswered)
		require.Equal(t, uint32(1), unansweredWeight)
		require.Greater(t, pendingClient.askCount(), 1, "pending is asked until the deadline")
	})
	t.Run("a reject is unanswered with its cause", func(t *testing.T) {
		session, _, nonce, execIdx, responseHash := finishedChallengeSession(t)
		verifiers := map[int]ChallengeVerifier{}
		for i, c := range session.clients {
			if i == execIdx {
				continue
			}
			verifiers[i] = &scriptedChallengeClient{HostClient: c, answers: []*transport.VerifyChallengeResponse{{RejectCause: "unchecked"}}}
		}
		executorAddr := session.StateMachine().SlotAddress(uint32(execIdx))

		votes, unanswered, unansweredWeight, causes := session.CollectChallengeVotes(context.Background(), nonce,
			types.ChallengeReason_CHALLENGE_REASON_STREAM_UNBOUND, responseHash, executorAddr, verifiers, time.Now().Add(30*time.Millisecond))
		require.Empty(t, votes)
		require.Equal(t, 2, unanswered)
		require.Equal(t, uint32(2), unansweredWeight)
		// The first reject settles the outcome (no fault can reach the
		// threshold any more), so the other ask may be cancelled before it
		// answers and then drains as an error without a cause.
		require.NotEmpty(t, causes)
		for _, cause := range causes {
			require.Equal(t, "unchecked", cause)
		}
	})
}

// A host whose devshardd predates VerifyChallenge answers Unimplemented, as
// every host of an escrow on the previous protocol does once the gateway is
// upgraded. The answer cannot change inside the window, so the gateway asks
// such a host once instead of until the deadline, which holds the escrow
// from settling for the whole fetch window. The first answer settles the
// outcome, and a host the settle cuts before its ask is not asked at all.
func TestCollectChallengeVotes_HostWithoutTheRPCIsAskedOnce(t *testing.T) {
	fastChallengeReask(t)
	session, _, nonce, execIdx, responseHash := finishedChallengeSession(t)
	verifiers := map[int]ChallengeVerifier{}
	var oldHosts []*scriptedChallengeClient
	for i, c := range session.clients {
		if i == execIdx {
			continue
		}
		client := &scriptedChallengeClient{HostClient: c, err: fmt.Errorf("verify-challenge: %w",
			connect.NewError(connect.CodeUnimplemented, errors.New("404 Not Found")))}
		verifiers[i] = client
		oldHosts = append(oldHosts, client)
	}
	executorAddr := session.StateMachine().SlotAddress(uint32(execIdx))

	start := time.Now()
	votes, unanswered, _, _ := session.CollectChallengeVotes(context.Background(), nonce,
		types.ChallengeReason_CHALLENGE_REASON_EMPTY_STREAM, responseHash, executorAddr, verifiers, time.Now().Add(time.Second))
	require.Empty(t, votes)
	require.Equal(t, 2, unanswered)
	asks := 0
	for _, client := range oldHosts {
		require.LessOrEqual(t, client.askCount(), 1, "a host without the RPC is asked at most once")
		asks += client.askCount()
	}
	require.GreaterOrEqual(t, asks, 1, "the outcome settled on an answer")
	require.Less(t, time.Since(start), 500*time.Millisecond, "the gateway waited for the deadline")
}

// lockedChallengeHandler records started challenges under a lock: the
// open-stream timer starts one from its own goroutine.
func lockedChallengeHandler(session *Session) func() []startedChallenge {
	var mu sync.Mutex
	var started []startedChallenge
	session.SetChallengeHandler(func(nonce uint64, reason types.ChallengeReason) {
		mu.Lock()
		defer mu.Unlock()
		started = append(started, startedChallenge{nonce: nonce, reason: reason})
	})
	return func() []startedChallenge {
		mu.Lock()
		defer mu.Unlock()
		return append([]startedChallenge(nil), started...)
	}
}

// A Finish that applies while the attempt's stream is still open is
// challenged once the stream outlives ChallengeOpenStreamGrace: the executor
// signed the Finish over the whole body, so a stream that drags on past it is
// the executor's doing, and waiting for its end would make the challenge late
// at the hosts. A stream that ends in time is handled on its own way out.
func TestAFinishWithAnOpenStreamIsChallengedAfterTheGrace(t *testing.T) {
	saved := ChallengeOpenStreamGrace
	ChallengeOpenStreamGrace = 20 * time.Millisecond
	t.Cleanup(func() { ChallengeOpenStreamGrace = saved })

	t.Run("still open after the grace", func(t *testing.T) {
		session := bindingSession(&[]servedBindingReport{})
		started := lockedChallengeHandler(session)
		session.StreamOpened(11, nil)
		applyFinish(session, 11)
		require.Empty(t, started(), "the grace has not passed")
		require.Eventually(t, func() bool { return len(started()) == 1 }, time.Second, time.Millisecond)
		require.Equal(t, []startedChallenge{{nonce: 11, reason: types.ChallengeReason_CHALLENGE_REASON_STREAM_UNBOUND}}, started())
	})
	t.Run("bound within the grace", func(t *testing.T) {
		session := bindingSession(&[]servedBindingReport{})
		started := lockedChallengeHandler(session)
		session.StreamOpened(12, nil)
		applyFinish(session, 12)
		session.BindReceivedStream(12, [][32]byte{storedSum})
		time.Sleep(5 * ChallengeOpenStreamGrace)
		require.Empty(t, started())
	})
	t.Run("ended with nothing to check within the grace", func(t *testing.T) {
		session := bindingSession(&[]servedBindingReport{})
		started := lockedChallengeHandler(session)
		session.StreamOpened(13, nil)
		applyFinish(session, 13)
		session.StreamEnded(13)
		time.Sleep(5 * ChallengeOpenStreamGrace)
		require.Empty(t, started())
	})
	t.Run("a Finish for a stream the gateway never opened waits for its mark", func(t *testing.T) {
		session := bindingSession(&[]servedBindingReport{})
		started := lockedChallengeHandler(session)
		applyFinish(session, 14)
		time.Sleep(5 * ChallengeOpenStreamGrace)
		require.Empty(t, started())
	})
}

// A stream still open after the grace because the gateway stands in writes
// to its own client is held by that client, not by the executor: the grace
// counts only the time the gateway waited on the executor. Once the client
// has caught up and the stream still drags, the challenge follows.
func TestAStreamHeldOpenByTheClientIsNotChallenged(t *testing.T) {
	saved := ChallengeOpenStreamGrace
	ChallengeOpenStreamGrace = 20 * time.Millisecond
	t.Cleanup(func() { ChallengeOpenStreamGrace = saved })

	session := bindingSession(&[]servedBindingReport{})
	started := lockedChallengeHandler(session)
	// Every moment while the client is slow is spent in a write to it.
	var mu sync.Mutex
	clientSlow := true
	var waited time.Duration
	last := time.Now()
	clientWait := func() time.Duration {
		mu.Lock()
		defer mu.Unlock()
		now := time.Now()
		if clientSlow {
			waited += now.Sub(last)
		}
		last = now
		return waited
	}

	session.StreamOpened(16, clientWait)
	applyFinish(session, 16)
	time.Sleep(5 * ChallengeOpenStreamGrace)
	require.Empty(t, started(), "the gateway stood in writes to its client the whole time")

	mu.Lock()
	clientSlow = false
	mu.Unlock()
	require.Eventually(t, func() bool { return len(started()) == 1 }, time.Second, time.Millisecond,
		"the client caught up and the stream still has not ended")
	require.Equal(t, []startedChallenge{{nonce: 16, reason: types.ChallengeReason_CHALLENGE_REASON_STREAM_UNBOUND}}, started())
}

// A session on a protocol before MsgChallenge is not challenged: its hosts
// do not know the tx, and a diff carrying one would fail on them whole.
// Nothing is asked. A session on v7 stays challengeable after the binary
// moves on to a later protocol: the tx is part of v7.
func TestRunChallenge_SkipsASessionBeforeTheChallengeProtocol(t *testing.T) {
	fastChallengeReask(t)
	saved := types.EffectiveStateRootAndProtocolVersion
	t.Cleanup(func() { types.EffectiveStateRootAndProtocolVersion = saved })
	for _, testCase := range []struct {
		name       string
		session    string
		binary     string
		challenged bool
	}{
		{name: "v6 session under a v7 binary", session: "v6", binary: "v7"},
		{name: "v7 session under a v8 binary", session: "v7", binary: "v8", challenged: true},
		{name: "named stamp of another build", session: "dev", binary: "dev-next"},
		{name: "same named stamp", session: "dev", binary: "dev", challenged: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			types.EffectiveStateRootAndProtocolVersion = testCase.session
			session, hosts, nonce, execIdx, responseHash := finishedChallengeSession(t)
			require.Equal(t, testCase.session, session.StateMachine().ProtocolVersion())
			types.EffectiveStateRootAndProtocolVersion = testCase.binary
			clients := make([]*scriptedChallengeClient, len(session.clients))
			for i, c := range session.clients {
				clients[i] = &scriptedChallengeClient{HostClient: c, err: errors.New("nothing is asked")}
				if i != execIdx {
					// A fault from each voter, so that both are asked: a first NONE
					// would settle the outcome before the other host is reached.
					clients[i].err = nil
					clients[i].answers = []*transport.VerifyChallengeResponse{
						signedChallengeAnswer(t, hosts[i], nonce, uint32(i), types.ChallengeFault_CHALLENGE_FAULT_PAYLOAD_UNAVAILABLE, responseHash),
					}
				}
				session.clients[i] = clients[i]
			}

			result, err := session.RunChallenge(context.Background(), nonce, types.ChallengeReason_CHALLENGE_REASON_STREAM_UNBOUND)
			require.NoError(t, err)
			require.Equal(t, !testCase.challenged, result.UnsupportedProtocol)
			require.Equal(t, execIdx, result.ExecutorIdx)
			rec, _ := session.StateMachine().Inference(nonce)
			require.Equal(t, testCase.challenged, rec.UsageChallenged)
			for i, c := range clients {
				if i == execIdx || !testCase.challenged {
					require.Zero(t, c.askCount())
				} else {
					require.Equal(t, 1, c.askCount())
				}
			}
		})
	}
}

// A host that answers InvalidArgument does not take the request as sent; the
// answer cannot change inside the window, so it is asked once, like a host
// without the RPC.
func TestCollectChallengeVotes_InvalidArgumentIsFinal(t *testing.T) {
	fastChallengeReask(t)
	session, _, nonce, execIdx, responseHash := finishedChallengeSession(t)
	verifiers := map[int]ChallengeVerifier{}
	var hosts []*scriptedChallengeClient
	for i, c := range session.clients {
		if i == execIdx {
			continue
		}
		client := &scriptedChallengeClient{HostClient: c, err: fmt.Errorf("verify-challenge: %w",
			connect.NewError(connect.CodeInvalidArgument, errors.New("unknown challenge reason 7")))}
		verifiers[i] = client
		hosts = append(hosts, client)
	}
	executorAddr := session.StateMachine().SlotAddress(uint32(execIdx))

	start := time.Now()
	votes, unanswered, _, _ := session.CollectChallengeVotes(context.Background(), nonce,
		types.ChallengeReason_CHALLENGE_REASON_STREAM_UNBOUND, responseHash, executorAddr, verifiers, time.Now().Add(time.Second))
	require.Empty(t, votes)
	require.Equal(t, 2, unanswered)
	for _, client := range hosts {
		require.Equal(t, 1, client.askCount())
	}
	require.Less(t, time.Since(start), 500*time.Millisecond)
}

// The gateway stops asking once the outcome is settled, as the timeout and
// error-miss collections do: a voter that never answers does not hold the
// escrow for the whole window when the votes in hand already decide it.
func TestCollectChallengeVotes_StopsWhenTheOutcomeIsSettled(t *testing.T) {
	fastChallengeReask(t)
	t.Run("the faults in hand already pass the threshold", func(t *testing.T) {
		session, hosts, nonce, execIdx, responseHash := finishedChallengeSessionOf(t, 5)
		verifiers := map[int]ChallengeVerifier{}
		var silent *scriptedChallengeClient
		for i, c := range session.clients {
			if i == execIdx {
				continue
			}
			if silent == nil {
				silent = &scriptedChallengeClient{HostClient: c}
				verifiers[i] = silent
				continue
			}
			verifiers[i] = &scriptedChallengeClient{HostClient: c, answers: []*transport.VerifyChallengeResponse{
				signedChallengeAnswer(t, hosts[i], nonce, uint32(i), types.ChallengeFault_CHALLENGE_FAULT_PAYLOAD_UNAVAILABLE, responseHash),
			}}
		}
		executorAddr := session.StateMachine().SlotAddress(uint32(execIdx))

		start := time.Now()
		votes, unanswered, _, _ := session.CollectChallengeVotes(context.Background(), nonce,
			types.ChallengeReason_CHALLENGE_REASON_STREAM_UNBOUND, responseHash, executorAddr, verifiers, time.Now().Add(10*time.Second))
		require.Len(t, votes, 3, "three faults of weight one pass the threshold of two")
		require.Equal(t, 1, unanswered)
		require.Equal(t, 1, silent.askCount())
		require.Less(t, time.Since(start), 2*time.Second, "the silent voter is not awaited")
	})
	t.Run("the missing votes can no longer reach the threshold", func(t *testing.T) {
		session, hosts, nonce, execIdx, responseHash := finishedChallengeSession(t)
		verifiers := map[int]ChallengeVerifier{}
		var silent *scriptedChallengeClient
		for i, c := range session.clients {
			if i == execIdx {
				continue
			}
			if silent == nil {
				silent = &scriptedChallengeClient{HostClient: c}
				verifiers[i] = silent
				continue
			}
			verifiers[i] = &scriptedChallengeClient{HostClient: c, answers: []*transport.VerifyChallengeResponse{
				signedChallengeAnswer(t, hosts[i], nonce, uint32(i), types.ChallengeFault_CHALLENGE_FAULT_NONE, responseHash),
			}}
		}
		executorAddr := session.StateMachine().SlotAddress(uint32(execIdx))

		start := time.Now()
		votes, unanswered, _, _ := session.CollectChallengeVotes(context.Background(), nonce,
			types.ChallengeReason_CHALLENGE_REASON_STREAM_UNBOUND, responseHash, executorAddr, verifiers, time.Now().Add(10*time.Second))
		require.Len(t, votes, 1)
		require.Equal(t, 1, unanswered)
		require.Less(t, time.Since(start), 2*time.Second, "one NONE plus one silent voter cannot pass the threshold of one")
	})
}

// A host that answers 429 or 503 is asked again after its Retry-After, and
// not before the pending interval: the hosts share the verifier queue with
// the timeout and error-miss votes, and a 5 s retry on a full queue would
// only add to it.
func TestCollectChallengeVotes_ResourceExhaustedWaitsForRetryAfter(t *testing.T) {
	savedErr, savedPending := ChallengeReaskInterval, ChallengePendingReaskInterval
	ChallengeReaskInterval, ChallengePendingReaskInterval = time.Millisecond, 300*time.Millisecond
	t.Cleanup(func() { ChallengeReaskInterval, ChallengePendingReaskInterval = savedErr, savedPending })
	withRetryAfter := connect.NewError(connect.CodeResourceExhausted, errors.New("too many sessions"))
	withRetryAfter.Meta().Set("Retry-After", "1")
	for _, testCase := range []struct {
		name string
		err  error
		wait time.Duration
	}{
		{name: "429 with Retry-After", err: withRetryAfter, wait: time.Second},
		{name: "429 without Retry-After", err: connect.NewError(connect.CodeResourceExhausted, errors.New("too many sessions")), wait: 300 * time.Millisecond},
		{name: "503", err: connect.NewError(connect.CodeUnavailable, errors.New("draining")), wait: 300 * time.Millisecond},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			session, hosts, nonce, execIdx, responseHash := finishedChallengeSession(t)
			verifiers := map[int]ChallengeVerifier{}
			var limited []*scriptedChallengeClient
			for i, c := range session.clients {
				if i == execIdx {
					continue
				}
				// A fault from each voter: with NONE the first answer would settle the
				// outcome and the second host would not be asked again at all.
				client := &scriptedChallengeClient{HostClient: c, errs: []error{fmt.Errorf("verify-challenge: %w", testCase.err)},
					answers: []*transport.VerifyChallengeResponse{
						signedChallengeAnswer(t, hosts[i], nonce, uint32(i), types.ChallengeFault_CHALLENGE_FAULT_PAYLOAD_UNAVAILABLE, responseHash),
					}}
				verifiers[i] = client
				limited = append(limited, client)
			}
			executorAddr := session.StateMachine().SlotAddress(uint32(execIdx))

			votes, unanswered, _, _ := session.CollectChallengeVotes(context.Background(), nonce,
				types.ChallengeReason_CHALLENGE_REASON_STREAM_UNBOUND, responseHash, executorAddr, verifiers, time.Now().Add(10*time.Second))
			require.Len(t, votes, 2)
			require.Zero(t, unanswered)
			for _, client := range limited {
				asks := client.askTimes()
				require.Len(t, asks, 2)
				require.GreaterOrEqual(t, asks[1].Sub(asks[0]), testCase.wait, "the second ask waited for the host")
			}
		})
	}
}

// A challenge at the active nonce cap, or in a finalize round, is skipped
// before any host is asked, as the timeout votes are: no diff could carry
// the MsgChallenge, so the votes would be collected for nothing and the
// settle barrier held meanwhile. The Finish past the cap stays paid, the way
// a timeout past it stays unvoted.
func TestRunChallenge_SkipsAtTheNonceCap(t *testing.T) {
	fastChallengeReask(t)
	session, _, nonce, execIdx, _ := finishedChallengeSession(t)
	clients := make([]*scriptedChallengeClient, len(session.clients))
	for i, c := range session.clients {
		clients[i] = &scriptedChallengeClient{HostClient: c, err: errors.New("nothing is asked")}
		session.clients[i] = clients[i]
	}
	capAtCurrentNonce(session)

	result, err := session.RunChallenge(context.Background(), nonce, types.ChallengeReason_CHALLENGE_REASON_EMPTY_STREAM)
	require.NoError(t, err)
	require.True(t, result.NonceCap)
	require.Equal(t, execIdx, result.ExecutorIdx)
	require.Empty(t, result.Votes)
	for _, c := range clients {
		require.Zero(t, c.askCount())
	}
	rec, _ := session.StateMachine().Inference(nonce)
	require.False(t, rec.UsageChallenged)
}

// A challenge started because the stream outlived the grace keeps the applied
// Finish hashes: when the stream ends at last, its hashes are still judged
// against the Finish, so a body the executor swapped is a strike as on the
// base, and the judging does not start a second challenge.
func TestAnOpenStreamChallengeKeepsTheFinishForTheServedBinding(t *testing.T) {
	saved := ChallengeOpenStreamGrace
	ChallengeOpenStreamGrace = 20 * time.Millisecond
	t.Cleanup(func() { ChallengeOpenStreamGrace = saved })
	for _, testCase := range []struct {
		name     string
		received [32]byte
		verdict  ServedBinding
	}{
		{name: "mismatch", received: tamperedSum, verdict: ServedBindingMismatch},
		{name: "bound", received: storedSum, verdict: ServedBindingBound},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			reports := &[]servedBindingReport{}
			session := bindingSession(reports)
			started := lockedChallengeHandler(session)
			session.StreamOpened(15, nil)
			applyFinish(session, 15)
			require.Eventually(t, func() bool { return len(started()) == 1 }, time.Second, time.Millisecond)

			session.BindReceivedStream(15, [][32]byte{testCase.received})
			require.Equal(t, []servedBindingReport{{nonce: 15, hostIndex: 0, participantKey: "host-zero", verdict: testCase.verdict}}, *reports)
			require.Len(t, started(), 1, "the served binding does not start a second challenge")
			session.mu.Lock()
			defer session.mu.Unlock()
			require.NotContains(t, session.appliedFinishHashes, uint64(15))
			require.NotContains(t, session.streamsOpen, uint64(15))
		})
	}
}
