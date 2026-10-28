package user

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"google.golang.org/protobuf/proto"

	"devshard/host"
	"devshard/signing"
	"devshard/types"
)

// startedNonce drives a nonce to Started the way a served one gets there: the executor's own signature
// over the record queues a confirm-start, and the next diff carries it into what every verifier reads.
func startedNonce(t *testing.T, session *Session, hosts []*signing.Secp256k1Signer, confirmedAt int64) uint64 {
	t.Helper()
	nonce := prepareNonce(t, session)
	record, tracked := session.sm.GetInference(nonce)
	require.True(t, tracked)
	receipt, err := proto.MarshalOptions{Deterministic: true}.Marshal(&types.ExecutorReceiptContent{
		InferenceId: nonce,
		PromptHash:  record.PromptHash,
		Model:       record.Model,
		InputLength: record.InputLength,
		MaxTokens:   record.MaxTokens,
		StartedAt:   record.StartedAt,
		EscrowId:    session.escrowID,
		ConfirmedAt: confirmedAt,
	})
	require.NoError(t, err)
	signature, err := hosts[record.ExecutorSlot].Sign(receipt)
	require.NoError(t, err)

	require.NoError(t, session.ProcessResponse(int(nonce%3), &host.HostResponse{
		Nonce: nonce, Receipt: signature, ConfirmedAt: confirmedAt,
	}, nonce))
	_, err = session.sendPendingDiff(context.Background(), nil, nil)
	require.NoError(t, err)

	started, tracked := session.sm.GetInference(nonce)
	require.True(t, tracked)
	require.Equal(t, types.StatusStarted, started.Status, "the fixture must reach the status it is named for")
	return nonce
}

// unstampedStartedNonce reaches Started through a confirm-start that carries no confirm time. A live
// receipt without one is refused, so such a record only comes back from replayed or recovered diffs.
func unstampedStartedNonce(t *testing.T, session *Session, hosts []*signing.Secp256k1Signer) uint64 {
	t.Helper()
	nonce := prepareNonce(t, session)
	record, tracked := session.sm.GetInference(nonce)
	require.True(t, tracked)
	receipt, err := proto.MarshalOptions{Deterministic: true}.Marshal(&types.ExecutorReceiptContent{
		InferenceId: nonce,
		PromptHash:  record.PromptHash,
		Model:       record.Model,
		InputLength: record.InputLength,
		MaxTokens:   record.MaxTokens,
		StartedAt:   record.StartedAt,
		EscrowId:    session.escrowID,
	})
	require.NoError(t, err)
	signature, err := hosts[record.ExecutorSlot].Sign(receipt)
	require.NoError(t, err)

	session.addPendingTx(&types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
		InferenceId: nonce, ExecutorSig: signature,
	}}})
	_, err = session.sendPendingDiff(context.Background(), nil, nil)
	require.NoError(t, err)

	started, tracked := session.sm.GetInference(nonce)
	require.True(t, tracked)
	require.Equal(t, types.StatusStarted, started.Status, "the fixture must reach the status it is named for")
	require.Zero(t, started.ConfirmedAt, "the fixture must leave the record unstamped")
	return nonce
}

// The reason is chosen from this session's memory of a receipt, but the verifiers judge the record the
// chain holds. A confirm-start that never landed leaves it pending, and an execution vote on a pending
// record is rejected by every one of them.
func TestAPendingRecordIsVotedRefusedWhateverWasPlanned(t *testing.T) {
	session, _, _ := setupSession(t, 3, 1_000_000, 0)
	nonce := prepareNonce(t, session)

	admitted, votable := session.reasonTheGroupWillAccept(nonce, types.TimeoutReason_TIMEOUT_REASON_EXECUTION, time.Now())

	require.True(t, votable, "waiting the execution deadline already outlasts the refusal one")
	require.Equal(t, types.TimeoutReason_TIMEOUT_REASON_REFUSED, admitted)
}

// The reverse does not hold: a started record cannot be voted refused, and its own execution deadline
// is the earliest moment any verifier will accept the vote it can take.
func TestAStartedRecordIsNotVotableBeforeItsOwnExecutionDeadline(t *testing.T) {
	session, hosts, _ := setupSession(t, 3, 1_000_000, 0)
	nonce := startedNonce(t, session, hosts, time.Now().Unix())

	_, votable := session.reasonTheGroupWillAccept(nonce, types.TimeoutReason_TIMEOUT_REASON_REFUSED, time.Now())

	require.False(t, votable, "no verifier accepts either reason yet, so the round is pure waste")
}

func TestAStartedRecordPastItsExecutionDeadlineIsVotedExecution(t *testing.T) {
	session, hosts, _ := setupSession(t, 3, 1_000_000, 0)
	nonce := startedNonce(t, session, hosts, time.Now().Unix())
	past := time.Now().Add(time.Duration(session.sm.Config().ExecutionTimeout)*time.Second + 2*TimeoutBuffer)

	admitted, votable := session.reasonTheGroupWillAccept(nonce, types.TimeoutReason_TIMEOUT_REASON_REFUSED, past)

	require.True(t, votable)
	require.Equal(t, types.TimeoutReason_TIMEOUT_REASON_EXECUTION, admitted)
}

// A nonce the record never tracked is the caller's own to judge; nothing here can improve on it.
func TestAnUntrackedNonceKeepsThePlannedReason(t *testing.T) {
	session, _, _ := setupSession(t, 3, 1_000_000, 0)

	admitted, votable := session.reasonTheGroupWillAccept(9999, types.TimeoutReason_TIMEOUT_REASON_EXECUTION, time.Now())

	require.True(t, votable)
	require.Equal(t, types.TimeoutReason_TIMEOUT_REASON_EXECUTION, admitted)
}

// A record whose executor stamped no confirm time is timed out from its start, the moment the sweep selects it.
func TestAStartedRecordWithoutAStampIsVotedExecutionFromItsStart(t *testing.T) {
	session, hosts, _ := setupSession(t, 3, 1_000_000, 0)
	nonce := unstampedStartedNonce(t, session, hosts)
	record, tracked := session.sm.GetInference(nonce)
	require.True(t, tracked)
	deadline := time.Unix(record.StartedAt, 0).Add(time.Duration(session.sm.Config().ExecutionTimeout)*time.Second + TimeoutBuffer)

	_, votableEarly := session.reasonTheGroupWillAccept(nonce, types.TimeoutReason_TIMEOUT_REASON_REFUSED, deadline.Add(-time.Second))
	admitted, votable := session.reasonTheGroupWillAccept(nonce, types.TimeoutReason_TIMEOUT_REASON_REFUSED, deadline)

	require.False(t, votableEarly, "before its start-anchored deadline the round would be waste")
	require.True(t, votable, "past it the group accepts an execution timeout")
	require.Equal(t, types.TimeoutReason_TIMEOUT_REASON_EXECUTION, admitted)
}
