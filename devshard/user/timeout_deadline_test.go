package user

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Test flow:
//  1. Start an inference whose executor receipt has landed in the record.
//  2. Forget the session's in-memory outcome for it, as a gateway restart does.
//  3. Expect an execution deadline counted from the record's confirmation stamp, since the chain rejects a refused timeout against a started record.
func TestTimeoutDeadlineReadsTheConfirmStampFromTheRecordAfterARestart(t *testing.T) {
	session, hosts, _ := setupSession(t, 3, 1_000_000, 0)
	confirmedAt := time.Now().Unix()
	nonce := startedNonce(t, session, hosts, confirmedAt)

	session.mu.Lock()
	delete(session.nonceStates, nonce)
	session.mu.Unlock()

	reason, deadline := session.TimeoutDeadline(nonce, time.Now())

	require.Equal(t, "execution", reason, "the record carries the receipt this session forgot")
	want := time.Unix(confirmedAt, 0).Add(time.Duration(session.sm.Config().ExecutionTimeout)*time.Second + TimeoutBuffer)
	require.Equal(t, want, deadline, "the deadline is counted from the record's own stamp")
}

// Test flow:
//  1. Prepare an inference that no host has confirmed.
//  2. Expect a refusal deadline counted from the send time, the only reason the chain admits for it.
func TestTimeoutDeadlineKeepsARecordWithoutTheStampRefused(t *testing.T) {
	session, _, _ := setupSession(t, 3, 1_000_000, 0)
	nonce := prepareNonce(t, session)
	sendTime := time.Now()

	reason, deadline := session.TimeoutDeadline(nonce, sendTime)

	require.Equal(t, "refused", reason)
	require.Equal(t, sendTime.Add(time.Duration(session.sm.Config().RefusalTimeout)*time.Second+TimeoutBuffer), deadline)
}

// Test flow:
//  1. Ask for the deadline of a nonce no record tracks.
//  2. Expect a refusal deadline counted from the send time.
func TestTimeoutDeadlineJudgesAnUntrackedNonceByTheSendTime(t *testing.T) {
	session, _, _ := setupSession(t, 3, 1_000_000, 0)
	sendTime := time.Now()

	reason, deadline := session.TimeoutDeadline(9999, sendTime)

	require.Equal(t, "refused", reason)
	require.Equal(t, sendTime.Add(time.Duration(session.sm.Config().RefusalTimeout)*time.Second+TimeoutBuffer), deadline)
}

// Test flow:
//  1. Prepare an inference and give the session memory a confirmation stamp the record does not carry yet.
//  2. Expect an execution deadline counted from that stamp.
func TestTimeoutDeadlineFallsBackToTheSessionMemory(t *testing.T) {
	session, _, _ := setupSession(t, 3, 1_000_000, 0)
	nonce := prepareNonce(t, session)
	confirmedAt := time.Now().Unix()

	session.mu.Lock()
	session.nonceStates[nonce] = &nonceOutcome{confirmedAt: confirmedAt}
	session.mu.Unlock()

	reason, deadline := session.TimeoutDeadline(nonce, time.Now())

	require.Equal(t, "execution", reason)
	require.Equal(t, time.Unix(confirmedAt, 0).Add(time.Duration(session.sm.Config().ExecutionTimeout)*time.Second+TimeoutBuffer), deadline)
}
