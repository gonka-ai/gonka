package user

import (
	"bytes"
	"context"
	"log"
	"sync/atomic"
	"testing"
	"time"

	promtestutil "github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"devshard/chainoracle/blocks/headertime"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

func mismatchCount(side, kind string) float64 {
	return promtestutil.ToFloat64(headertime.TimeoutClockMismatchCounter().WithLabelValues(side, kind))
}

// Legacy refusal opens on wall sendTime even when the stamp Decide is not due,
// and records a gateway mismatch.
func TestLegacyRefusalOpensOnWallDespiteStampNotDue(t *testing.T) {
	session, hosts, _, _ := setupArmedSession(t, 3, 1_000_000, 0)
	require.False(t, session.sm.Config().HeightsyncTimeoutClock)

	nonce := prepareNonce(t, session)
	rec, ok := session.sm.GetInference(nonce)
	require.True(t, ok)
	refusal := time.Duration(session.sm.Config().RefusalTimeout) * time.Second

	headers := &countingHeaders{}
	// Stamp far in the future → Decide not due.
	headers.byHeight = map[uint64]int64{rec.StartedAtHeight: time.Now().Add(24 * time.Hour).UnixMilli()}
	session.StateMachine().SetHeaderTimes(headers)

	before := mismatchCount("gateway", "refused")
	var logs bytes.Buffer
	prevLog := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(prevLog)

	pastSend := time.Now().Add(-refusal - TimeoutBuffer - time.Millisecond)
	reason, deadline := session.TimeoutDeadline(nonce, pastSend)
	require.Equal(t, "refused", reason)
	require.False(t, time.Now().Before(deadline), "legacy wall deadline is due")
	require.Greater(t, mismatchCount("gateway", "refused"), before)
	require.Contains(t, logs.String(), "timeout clock mismatch")

	var opened atomic.Int32
	for i := range session.clients {
		session.clients[i] = &verifyingHost{HostClient: session.clients[i], mockTimeoutVerifier: &mockTimeoutVerifier{
			accept: true, signer: hosts[i], group: session.group, slotIdx: i,
			onVerify: func() { opened.Add(1) },
		}}
	}
	result, needsWait, _ := session.VoteTimeout(context.Background(), nonce, pastSend, nil, types.TimeoutReason_TIMEOUT_REASON_REFUSED)
	require.False(t, needsWait)
	require.Positive(t, opened.Load())
	require.Equal(t, "refused", result.Reason)
	require.NotEqual(t, "skipped", result.Outcome)
}

// Header-mode refusal does not open on wall sendTime alone when the stamp is not due.
func TestHeaderRefusalDoesNotOpenOnWallAlone(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	config := testutil.DefaultConfig(len(hosts))
	config.HeightsyncTimeoutClock = true
	session, _, _ := armedSessionFor(t, testutil.MakeGroup(hosts), hosts, config, 1_000_000, 0)

	nonce := prepareNonce(t, session)
	rec, ok := session.sm.GetInference(nonce)
	require.True(t, ok)
	refusal := time.Duration(config.RefusalTimeout) * time.Second

	headers := &countingHeaders{}
	headers.byHeight = map[uint64]int64{rec.StartedAtHeight: time.Now().Add(24 * time.Hour).UnixMilli()}
	session.StateMachine().SetHeaderTimes(headers)

	before := mismatchCount("gateway", "refused")
	pastSend := time.Now().Add(-refusal - time.Millisecond)
	reason, deadline := session.TimeoutDeadline(nonce, pastSend)
	require.Equal(t, "refused", reason)
	require.False(t, refusalPast(time.Now(), deadline), "header Active follows stamp, not wall")
	require.Equal(t, before, mismatchCount("gateway", "refused"), "header mode has no wall shadow mismatch")

	var opened atomic.Int32
	for i := range session.clients {
		session.clients[i] = &verifyingHost{HostClient: session.clients[i], mockTimeoutVerifier: &mockTimeoutVerifier{
			accept: true, signer: hosts[i], group: session.group, slotIdx: i,
			onVerify: func() { opened.Add(1) },
		}}
	}
	result, needsWait, err := session.VoteTimeout(context.Background(), nonce, pastSend, nil, types.TimeoutReason_TIMEOUT_REASON_REFUSED)
	require.NoError(t, err)
	require.True(t, needsWait)
	require.Equal(t, "skipped", result.Outcome)
	require.Equal(t, "deadline_not_due", result.DetailReason)
	require.Zero(t, opened.Load())
}

// Legacy execution uses wall confirmed_at; stamp not due still opens Active and mismatches.
func TestLegacyExecutionOpensOnWallConfirmedAt(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	config := testutil.DefaultConfig(len(hosts))
	config.ExecutionTimeout = 5
	require.False(t, config.HeightsyncTimeoutClock)
	session, _, _ := armedSessionFor(t, testutil.MakeGroup(hosts), hosts, config, 1_000_000, 0)

	confirmedAt := time.Now().Add(-(time.Duration(config.ExecutionTimeout)*time.Second + TimeoutBuffer) - time.Second).Unix()
	nonce := startedNonce(t, session, hosts, confirmedAt)
	rec, ok := session.sm.GetInference(nonce)
	require.True(t, ok)
	require.Equal(t, confirmedAt, rec.ConfirmedAt)

	headers := &countingHeaders{}
	// Stamp not due for a long time.
	headers.byHeight = map[uint64]int64{rec.ConfirmedAtHeight: time.Now().Add(24 * time.Hour).UnixMilli()}
	session.StateMachine().SetHeaderTimes(headers)

	before := mismatchCount("gateway", "execution")
	reason, deadline := session.TimeoutDeadline(nonce, time.Now())
	require.Equal(t, "execution", reason)
	require.Equal(t, time.Unix(confirmedAt, 0).Add(time.Duration(config.ExecutionTimeout)*time.Second+TimeoutBuffer), deadline)
	require.False(t, time.Now().Before(deadline))
	require.Greater(t, mismatchCount("gateway", "execution"), before)

	var opened atomic.Int32
	for i := range session.clients {
		session.clients[i] = &verifyingHost{HostClient: session.clients[i], mockTimeoutVerifier: &mockTimeoutVerifier{
			accept: false, signer: hosts[i], group: session.group, slotIdx: i,
			onVerify: func() { opened.Add(1) },
		}}
	}
	result, needsWait, _ := session.VoteTimeout(context.Background(), nonce, time.Now(), nil, types.TimeoutReason_TIMEOUT_REASON_EXECUTION)
	require.False(t, needsWait)
	require.Positive(t, opened.Load())
	require.Equal(t, "execution", result.Reason)
}

// Legacy execution prefers the in-memory receipt time over the applied record.
func TestLegacyExecutionPrefersOutcomeConfirmedAt(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	config := testutil.DefaultConfig(len(hosts))
	config.ExecutionTimeout = 5
	session, _, _ := armedSessionFor(t, testutil.MakeGroup(hosts), hosts, config, 1_000_000, 0)

	nonce := startedNonce(t, session, hosts, time.Now().Unix())
	outcomeAt := int64(1_700_000_000)
	session.mu.Lock()
	session.nonceStates[nonce].confirmedAt = outcomeAt
	session.mu.Unlock()

	reason, deadline := session.TimeoutDeadline(nonce, time.Now())
	require.Equal(t, "execution", reason)
	require.Equal(t, time.Unix(outcomeAt, 0).Add(time.Duration(config.ExecutionTimeout)*time.Second+TimeoutBuffer), deadline)
}

// Header execution uses stamp / ConfirmedAtTimeMs, not wall alone.
func TestHeaderExecutionUsesStampNotWallAlone(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	config := testutil.DefaultConfig(len(hosts))
	config.ExecutionTimeout = 5
	config.HeightsyncTimeoutClock = true
	session, _, _ := armedSessionFor(t, testutil.MakeGroup(hosts), hosts, config, 1_000_000, 0)

	confirmedAt := time.Now().Add(-6 * time.Second).Unix()
	nonce := startedNonce(t, session, hosts, confirmedAt)
	rec, ok := session.sm.GetInference(nonce)
	require.True(t, ok)

	headers := &countingHeaders{}
	headers.byHeight = map[uint64]int64{rec.ConfirmedAtHeight: time.Now().Add(24 * time.Hour).UnixMilli()}
	session.StateMachine().SetHeaderTimes(headers)

	before := mismatchCount("gateway", "execution")
	reason, deadline := session.TimeoutDeadline(nonce, time.Now())
	require.Equal(t, "execution", reason)
	require.False(t, refusalPast(time.Now(), deadline), "wall past must not open header Active")
	require.Equal(t, before, mismatchCount("gateway", "execution"))
}

// A refusal mismatch does not consume the execution sample, and a repeat of
// the same kind does not increment again.
func TestLegacyMismatchIsOncePerKind(t *testing.T) {
	session, _, _, _ := setupArmedSession(t, 3, 1_000_000, 0)
	nonce := prepareNonce(t, session)
	rec, ok := session.sm.GetInference(nonce)
	require.True(t, ok)
	refusal := time.Duration(session.sm.Config().RefusalTimeout) * time.Second

	headers := &countingHeaders{}
	headers.byHeight = map[uint64]int64{rec.StartedAtHeight: time.Now().Add(24 * time.Hour).UnixMilli()}
	session.StateMachine().SetHeaderTimes(headers)

	beforeRefused := mismatchCount("gateway", "refused")
	beforeExecution := mismatchCount("gateway", "execution")
	pastSend := time.Now().Add(-refusal - TimeoutBuffer - time.Second)
	session.TimeoutDeadline(nonce, pastSend)
	afterFirst := headers.calls.Load()
	session.TimeoutDeadline(nonce, pastSend)
	require.Equal(t, afterFirst, headers.calls.Load(), "a recorded mismatch does not read the map again")
	require.Equal(t, beforeRefused+1, mismatchCount("gateway", "refused"))
	require.Equal(t, beforeExecution, mismatchCount("gateway", "execution"))

	session.mu.Lock()
	session.nonceStates[nonce].confirmedAt = time.Now().Add(-time.Hour).Unix()
	// The confirm is still queued, so the stored time stays the execution anchor.
	session.addPendingTx(&types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
		InferenceId: nonce, ConfirmedAt: session.nonceStates[nonce].confirmedAt, ExecutorSig: []byte{1},
	}}})
	session.mu.Unlock()
	session.TimeoutDeadline(nonce, pastSend)
	session.TimeoutDeadline(nonce, pastSend)
	require.Equal(t, beforeRefused+1, mismatchCount("gateway", "refused"))
	require.Equal(t, beforeExecution+1, mismatchCount("gateway", "execution"))
}

// The shadow follows the deadline that opens: inside TimeoutBuffer, and on a
// zero sendTime, a stamp that matches the wall anchor does not count.
func TestLegacyShadowUsesTheOpenDeadline(t *testing.T) {
	prev := TimeoutBuffer
	TimeoutBuffer = 5 * time.Second
	t.Cleanup(func() { TimeoutBuffer = prev })

	session, _, _, _ := setupArmedSession(t, 3, 1_000_000, 0)
	nonce := prepareNonce(t, session)
	rec, ok := session.sm.GetInference(nonce)
	require.True(t, ok)
	refusal := time.Duration(session.sm.Config().RefusalTimeout) * time.Second

	// One millisecond past the bare timeout is still inside TimeoutBuffer.
	sendTime := time.Now().Add(-refusal - time.Millisecond)
	headers := &countingHeaders{}
	headers.byHeight = map[uint64]int64{rec.StartedAtHeight: sendTime.UnixMilli()}
	session.StateMachine().SetHeaderTimes(headers)

	before := mismatchCount("gateway", "refused")
	reason, deadline := session.TimeoutDeadline(nonce, sendTime)
	require.Equal(t, "refused", reason)
	require.True(t, time.Now().Before(deadline), "buffer keeps the vote closed")
	require.Equal(t, before, mismatchCount("gateway", "refused"), "stamp at sendTime is not a mismatch inside the buffer")

	reason, deadline = session.TimeoutDeadline(nonce, time.Time{})
	require.Equal(t, "refused", reason)
	require.False(t, time.Now().Before(deadline), "zero sendTime is already due")
	require.Equal(t, before, mismatchCount("gateway", "refused"), "a missing sendTime is not a wall instant")
}

// Zero sendTime (restart) is the v6 wall deadline: the zero time plus
// RefusalTimeout and TimeoutBuffer, which is already past. It does not
// switch to record StartedAt.
func TestLegacyZeroSendTimeUsesSendTime(t *testing.T) {
	session, _, _, _ := setupArmedSession(t, 3, 1_000_000, 0)
	nonce := prepareNonce(t, session)
	rec, ok := session.sm.GetInference(nonce)
	require.True(t, ok)
	require.Positive(t, rec.StartedAt)

	refusal := time.Duration(session.sm.Config().RefusalTimeout) * time.Second
	reason, deadline := session.TimeoutDeadline(nonce, time.Time{})
	require.Equal(t, "refused", reason)
	require.Equal(t, time.Time{}.Add(refusal+TimeoutBuffer), deadline)
	require.False(t, time.Now().Before(deadline))
}

// Zero sendTime in header mode still uses the header map (not wall StartedAt alone).
func TestHeaderZeroSendTimeUsesStampMap(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	config := testutil.DefaultConfig(len(hosts))
	config.HeightsyncTimeoutClock = true
	session, _, _ := armedSessionFor(t, testutil.MakeGroup(hosts), hosts, config, 1_000_000, 0)
	nonce := prepareNonce(t, session)
	rec, ok := session.sm.GetInference(nonce)
	require.True(t, ok)

	refusal := time.Duration(config.RefusalTimeout) * time.Second
	headers := &countingHeaders{}
	session.StateMachine().SetHeaderTimes(headers)

	reason, deadline := session.TimeoutDeadline(nonce, time.Time{})
	require.Equal(t, "refused", reason)
	require.True(t, deadline.After(time.Now()), "map miss does not open")

	stamp := time.Now().Add(-refusal - time.Millisecond)
	headers.byHeight = map[uint64]int64{rec.StartedAtHeight: stamp.UnixMilli()}
	reason, deadline = session.TimeoutDeadline(nonce, time.Time{})
	require.Equal(t, "refused", reason)
	require.True(t, refusalPast(time.Now(), deadline))
}
