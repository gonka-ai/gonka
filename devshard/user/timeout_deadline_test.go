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
