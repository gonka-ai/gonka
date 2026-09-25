package user

import (
	"crypto/sha256"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

var (
	storedSum   = sha256.Sum256([]byte("stored"))
	servedSum   = sha256.Sum256([]byte("served"))
	tamperedSum = sha256.Sum256([]byte("tampered"))
)

type servedBindingReport struct {
	nonce          uint64
	hostIndex      int
	participantKey string
	verdict        ServedBinding
}

func bindingSession(reports *[]servedBindingReport) *Session {
	session := &Session{
		pendingTxKeys:   map[string]struct{}{},
		appliedTxKeys:   map[string]struct{}{},
		group:           []types.SlotAssignment{{SlotID: 0}, {SlotID: 1}, {SlotID: 2}},
		participantKeys: []string{"host-zero", "host-one", "host-two"},
	}
	session.SetServedBindingHandler(func(nonce uint64, hostIndex int, participantKey string, verdict ServedBinding) {
		*reports = append(*reports, servedBindingReport{nonce: nonce, hostIndex: hostIndex, participantKey: participantKey, verdict: verdict})
	})
	return session
}

func finishTransaction(nonce uint64, responseHash [32]byte) *types.DevshardTx {
	return &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: &types.MsgFinishInference{
		InferenceId: nonce, ResponseHash: responseHash[:], ServedHash: servedSum[:],
	}}}
}

func applyFinish(session *Session, nonce uint64) {
	session.mu.Lock()
	defer session.mu.Unlock()
	session.retainPendingLocked(nil, []*types.DevshardTx{finishTransaction(nonce, storedSum)})
}

// Test flow:
//  1. Bind the hashes the gateway received for a nonce while no Finish for it has applied yet.
//  2. Apply that nonce's Finish afterwards, the way one gossiped through another host's mempool lands.
//  3. Assert the verdict is reported once, against the executor's host and participant, as bound when the stream matched either signed view and as mismatch otherwise.
func TestAFinishAppliedAfterTheStreamIsCheckedAgainstIt(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		received [32]byte
		want     ServedBinding
	}{
		{name: "the served view arrived", received: servedSum, want: ServedBindingBound},
		{name: "the stored view arrived", received: storedSum, want: ServedBindingBound},
		{name: "another answer arrived", received: tamperedSum, want: ServedBindingMismatch},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var reports []servedBindingReport
			session := bindingSession(&reports)

			session.BindReceivedStream(5, [][32]byte{testCase.received})
			require.Empty(t, reports, "no Finish is known yet, so nothing can be judged")
			applyFinish(session, 5)

			require.Equal(t, []servedBindingReport{{nonce: 5, hostIndex: 2, participantKey: "host-two", verdict: testCase.want}}, reports)
		})
	}
}

// Test flow:
//  1. Apply a nonce's Finish before the gateway has finished reading that nonce's stream.
//  2. Bind the hashes the gateway received.
//  3. Assert the verdict is reported at once from the Finish already applied.
func TestAStreamEndingAfterItsFinishIsCheckedAtOnce(t *testing.T) {
	var reports []servedBindingReport
	session := bindingSession(&reports)

	applyFinish(session, 4)
	require.Empty(t, reports, "no stream is known yet, so nothing can be judged")
	session.BindReceivedStream(4, [][32]byte{tamperedSum})

	require.Equal(t, []servedBindingReport{{nonce: 4, hostIndex: 1, participantKey: "host-one", verdict: ServedBindingMismatch}}, reports)
}

// Test flow:
//  1. Bind an empty set of received hashes, as a stream that carried no answer line leaves.
//  2. Apply the nonce's Finish.
//  3. Assert nothing is reported, since there is no answer to judge.
func TestAStreamWithNothingReceivedIsNotJudged(t *testing.T) {
	var reports []servedBindingReport
	session := bindingSession(&reports)

	session.BindReceivedStream(3, nil)
	applyFinish(session, 3)

	require.Empty(t, reports)
}

// Test flow:
//  1. Leave a stream waiting for its Finish, or a Finish waiting for its stream, on a nonce the escrow no longer tracks.
//  2. Move the clock past twice the execution deadline and trigger pruning with an unrelated entry.
//  3. Complete the first nonce and assert nothing is reported, because the waiting entry was forgotten.
func TestServedBindingForgetsEntriesPastTwiceTheExecutionDeadline(t *testing.T) {
	for _, testCase := range []struct {
		name           string
		leaveWaiting   func(session *Session)
		completeWaiter func(session *Session)
	}{
		{
			name:           "a stream waiting for its Finish",
			leaveWaiting:   func(session *Session) { session.BindReceivedStream(1, [][32]byte{tamperedSum}) },
			completeWaiter: func(session *Session) { applyFinish(session, 1) },
		},
		{
			name:           "a Finish waiting for its stream",
			leaveWaiting:   func(session *Session) { applyFinish(session, 1) },
			completeWaiter: func(session *Session) { session.BindReceivedStream(1, [][32]byte{tamperedSum}) },
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var reports []servedBindingReport
			session := bindingSession(&reports)
			now := time.Unix(1_000_000, 0)
			session.clock = func() time.Time { return now }

			testCase.leaveWaiting(session)
			now = now.Add(servedBindingRetention(0) + time.Second)
			session.BindReceivedStream(99, [][32]byte{tamperedSum})
			applyFinish(session, 98)
			testCase.completeWaiter(session)

			require.Empty(t, reports)
		})
	}
}

// Test flow:
//  1. Start an inference, so its record is pending and its Finish can still apply.
//  2. Leave the gateway's stream for it waiting, then move the clock past twice the execution deadline and trigger pruning.
//  3. Apply its Finish and assert the verdict is still reported, because a stream is never forgotten while its Finish can land.
func TestServedBindingKeepsAStreamWhileItsFinishCanStillApply(t *testing.T) {
	var reports []servedBindingReport
	session := bindingSession(&reports)
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	userKey := testutil.MustGenerateKey(t)
	session.sm = newTestStateMachine(t, "escrow-1", testutil.DefaultConfig(len(hosts)), testutil.MakeGroup(hosts), 1_000_000, userKey.Address(), signing.NewSecp256k1Verifier())
	_, err := session.sm.ApplyDiff(testutil.SignDiff(t, userKey, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)}))
	require.NoError(t, err)
	now := time.Unix(1_000_000, 0)
	session.clock = func() time.Time { return now }

	session.BindReceivedStream(1, [][32]byte{tamperedSum})
	now = now.Add(servedBindingRetention(session.sm.Config().ExecutionTimeout) + time.Second)
	session.BindReceivedStream(99, [][32]byte{tamperedSum})
	applyFinish(session, 1)

	require.Equal(t, []servedBindingReport{{nonce: 1, hostIndex: 1, participantKey: "host-one", verdict: ServedBindingMismatch}}, reports)
}

// Test flow:
//  1. Bind the stream the gateway received for a nonce.
//  2. Queue a Finish that matches that stream but never applies, as one refused for a malformed hash or a forged signature is.
//  3. Apply the executor's other Finish, signed over a different answer.
//  4. Assert the only verdict is a mismatch against the Finish that applied.
func TestOnlyTheFinishThatAppliesIsJudged(t *testing.T) {
	var reports []servedBindingReport
	session := bindingSession(&reports)

	session.BindReceivedStream(6, [][32]byte{tamperedSum})
	session.mu.Lock()
	session.addPendingTx(finishTransaction(6, tamperedSum))
	session.mu.Unlock()
	require.Empty(t, reports, "a queued Finish may still be refused, so it is not judged")
	applyFinish(session, 6)

	require.Equal(t, []servedBindingReport{{nonce: 6, hostIndex: 0, participantKey: "host-zero", verdict: ServedBindingMismatch}}, reports)
}
