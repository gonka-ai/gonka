package user

import (
	"bytes"
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	promtestutil "github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"devshard/chainoracle/blocks/headertime"
	"devshard/heightsync"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

func mismatchCount(side, kind string) float64 {
	return promtestutil.ToFloat64(headertime.TimeoutClockMismatchCounter().WithLabelValues(side, kind))
}

func captureWarns(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
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
	logs := captureWarns(t)

	pastSend := time.Now().Add(-refusal - TimeoutBuffer - time.Millisecond)
	reason, deadline := session.TimeoutDeadline(nonce, pastSend)
	require.Equal(t, "refused", reason)
	require.False(t, time.Now().Before(deadline), "legacy wall deadline is due")
	require.Equal(t, before+1, mismatchCount("gateway", "refused"))
	requireClockMismatchWarn(t, logs, "gateway", "refused", "stamp_late")

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
	logs := captureWarns(t)
	pastSend := time.Now().Add(-refusal - time.Millisecond)
	reason, deadline := session.TimeoutDeadline(nonce, pastSend)
	require.Equal(t, "refused", reason)
	require.False(t, refusalPast(time.Now(), deadline), "header Active follows stamp, not wall")
	require.Equal(t, before, mismatchCount("gateway", "refused"), "header mode has no wall shadow mismatch")
	require.NotContains(t, logs.String(), "timeout clock mismatch")

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

// Header mode with a due stamp does not shadow the wall, even when the wall
// itself is not due.
func TestHeaderStampDueDoesNotShadowWall(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	config := testutil.DefaultConfig(len(hosts))
	config.HeightsyncTimeoutClock = true
	session, _, _ := armedSessionFor(t, testutil.MakeGroup(hosts), hosts, config, 1_000_000, 0)

	nonce := prepareNonce(t, session)
	rec, ok := session.sm.GetInference(nonce)
	require.True(t, ok)
	refusal := time.Duration(config.RefusalTimeout) * time.Second
	headers := &countingHeaders{}
	headers.byHeight = map[uint64]int64{rec.StartedAtHeight: time.Now().Add(-refusal - time.Second).UnixMilli()}
	session.StateMachine().SetHeaderTimes(headers)

	before := mismatchCount("gateway", "refused")
	logs := captureWarns(t)
	reason, deadline := session.TimeoutDeadline(nonce, time.Now())
	require.Equal(t, "refused", reason)
	require.False(t, time.Now().Before(deadline), "header Active follows the due stamp")
	require.Equal(t, before, mismatchCount("gateway", "refused"))
	require.NotContains(t, logs.String(), "timeout clock mismatch")
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

	st := session.sm.ExportState()
	st.Inferences[nonce].ConfirmedAtTimeMs = time.Now().Add(24 * time.Hour).UnixMilli()
	floor, err := heightsync.FloorIndexFromProto(heightsync.FloorConfig{}, session.sm.ExportHeightSyncFloor())
	require.NoError(t, err)
	require.NoError(t, session.sm.RestoreStateWithFloor(st, floor))
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
	require.Equal(t, time.Unix(rec.StartedAt, 0).Add(refusal+TimeoutBuffer), deadline)
	require.True(t, time.Now().Before(deadline), "a zero sendTime waits on StartedAt")
	require.Equal(t, before, mismatchCount("gateway", "refused"))
}

// Wall and stamp both past the open deadline do not warn or increment.
func TestLegacyBothDueDoesNotWarn(t *testing.T) {
	session, _, _, _ := setupArmedSession(t, 3, 1_000_000, 0)
	nonce := prepareNonce(t, session)
	rec, ok := session.sm.GetInference(nonce)
	require.True(t, ok)
	refusal := time.Duration(session.sm.Config().RefusalTimeout) * time.Second

	sendTime := time.Now().Add(-refusal - TimeoutBuffer - time.Second)
	headers := &countingHeaders{}
	headers.byHeight = map[uint64]int64{rec.StartedAtHeight: sendTime.UnixMilli()}
	session.StateMachine().SetHeaderTimes(headers)

	before := mismatchCount("gateway", "refused")
	logs := captureWarns(t)
	reason, deadline := session.TimeoutDeadline(nonce, sendTime)
	require.Equal(t, "refused", reason)
	require.False(t, time.Now().Before(deadline))
	require.Equal(t, before, mismatchCount("gateway", "refused"))
	require.NotContains(t, logs.String(), "timeout clock mismatch")
}

func requireClockMismatchWarn(t *testing.T, logs *bytes.Buffer, side, kind, cause string) {
	t.Helper()
	text := logs.String()
	require.Contains(t, text, "timeout clock mismatch")
	require.Contains(t, text, `"side":"`+side+`"`)
	require.Contains(t, text, `"reason":"`+kind+`"`)
	require.Contains(t, text, `"escrow":`)
	require.Contains(t, text, `"nonce":`)
	require.Contains(t, text, `"wall_due":true`)
	require.Contains(t, text, `"stamp_due":false`)
	require.Contains(t, text, `"wall_anchor":`)
	require.Contains(t, text, `"stamp_ms":`)
	require.Contains(t, text, `"height":`)
	require.Contains(t, text, `"mismatch_cause":"`+cause+`"`)
}

// A restart has no sendTime. The legacy refusal waits on record StartedAt
// plus the timeout and TimeoutBuffer. It does not use the zero time.
func TestLegacyZeroSendTimeUsesStartedAt(t *testing.T) {
	session, _, _, _ := setupArmedSession(t, 3, 1_000_000, 0)
	nonce := prepareNonce(t, session)
	rec, ok := session.sm.GetInference(nonce)
	require.True(t, ok)
	require.Positive(t, rec.StartedAt)

	refusal := time.Duration(session.sm.Config().RefusalTimeout) * time.Second
	reason, deadline := session.TimeoutDeadline(nonce, time.Time{})
	require.Equal(t, "refused", reason)
	require.Equal(t, time.Unix(rec.StartedAt, 0).Add(refusal+TimeoutBuffer), deadline)
	require.True(t, time.Now().Before(deadline))
	require.False(t, deadline.Equal(time.Unix(0, 0).Add(refusal+TimeoutBuffer)))
}

// No sendTime and no StartedAt is unarmed. PrepareInference fills a missing
// StartedAt, so this calls the same helper TimeoutDeadline uses.
func TestLegacyZeroSendTimeWithoutStartedAtIsNotEpoch(t *testing.T) {
	session, _, _, _ := setupArmedSession(t, 3, 1_000_000, 0)
	nonce := prepareNonce(t, session)
	now := time.Now()
	reason, deadline, _ := session.legacyDueLocked(nonce, headertime.KindRefused, time.Time{}, session.sm.Config().RefusalTimeout, types.InferenceRecord{}, now)
	require.Equal(t, "refused", reason)
	require.False(t, deadline.Equal(time.Unix(0, 0)))
	require.False(t, deadline.IsZero())
	require.True(t, now.Before(deadline))
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

// Polls before the wall deadline do not read the header map. The first poll
// after the wall arm is due reads once, including when the clocks agree.
func TestLegacyShadowReadsTheMapOnlyWhenWallIsDue(t *testing.T) {
	session, _, _, _ := setupArmedSession(t, 3, 1_000_000, 0)
	nonce := prepareNonce(t, session)
	rec, ok := session.sm.GetInference(nonce)
	require.True(t, ok)
	refusal := time.Duration(session.sm.Config().RefusalTimeout) * time.Second

	sendTime := time.Now()
	headers := &countingHeaders{}
	headers.byHeight = map[uint64]int64{rec.StartedAtHeight: sendTime.UnixMilli()}
	session.StateMachine().SetHeaderTimes(headers)

	before := mismatchCount("gateway", "refused")
	session.TimeoutDeadline(nonce, sendTime)
	session.TimeoutDeadline(nonce, sendTime)
	require.Equal(t, int32(0), headers.calls.Load(), "a wait poll does not read the header map")
	require.Equal(t, before, mismatchCount("gateway", "refused"))

	past := time.Now().Add(-refusal - TimeoutBuffer - time.Second)
	headers.byHeight[rec.StartedAtHeight] = past.UnixMilli()
	logs := captureWarns(t)
	session.TimeoutDeadline(nonce, past)
	require.Equal(t, int32(1), headers.calls.Load())
	session.TimeoutDeadline(nonce, past)
	require.Equal(t, int32(1), headers.calls.Load(), "an agreeing due sample is not read again")
	require.Equal(t, before, mismatchCount("gateway", "refused"))
	require.NotContains(t, logs.String(), "timeout clock mismatch")
}

// Receipt wakes ask whether the inference is confirmed. That check does not
// observe the shadow and does not take the header map lock.
func TestInferenceConfirmedDoesNotReadTheHeaderMap(t *testing.T) {
	session, _, _, _ := setupArmedSession(t, 3, 1_000_000, 0)
	nonce := prepareNonce(t, session)
	rec, ok := session.sm.GetInference(nonce)
	require.True(t, ok)
	refusal := time.Duration(session.sm.Config().RefusalTimeout) * time.Second

	headers := &countingHeaders{}
	headers.byHeight = map[uint64]int64{rec.StartedAtHeight: time.Now().Add(24 * time.Hour).UnixMilli()}
	session.StateMachine().SetHeaderTimes(headers)

	past := time.Now().Add(-refusal - TimeoutBuffer - time.Second)
	for range 8 {
		require.False(t, session.inferenceConfirmed(nonce, past))
	}
	require.Equal(t, int32(0), headers.calls.Load())
}

// A stamp that opened more than TimeoutBuffer before the wall arm is stamp_early
// at the wall boundary, and both_due when the sample itself is already late.
func TestLegacyShadowLabelsStampEarlyAndBothDue(t *testing.T) {
	session, _, _, _ := setupArmedSession(t, 3, 1_000_000, 0)
	nonce := prepareNonce(t, session)
	rec, ok := session.sm.GetInference(nonce)
	require.True(t, ok)
	refusal := time.Duration(session.sm.Config().RefusalTimeout) * time.Second

	justDue := time.Now().Add(-refusal - TimeoutBuffer - time.Millisecond)
	headers := &countingHeaders{}
	headers.byHeight = map[uint64]int64{rec.StartedAtHeight: justDue.Add(-time.Hour).UnixMilli()}
	session.StateMachine().SetHeaderTimes(headers)

	before := mismatchCount("gateway", "refused")
	logs := captureWarns(t)
	session.TimeoutDeadline(nonce, justDue)
	afterEarly := headers.calls.Load()
	session.TimeoutDeadline(nonce, justDue)
	require.Equal(t, afterEarly, headers.calls.Load())
	require.Equal(t, before+1, mismatchCount("gateway", "refused"))
	require.Contains(t, logs.String(), `"mismatch_cause":"stamp_early"`)
	require.Contains(t, logs.String(), `"stamp_due":true`)

	lateSession, _, _, _ := setupArmedSession(t, 3, 1_000_000, 0)
	lateNonce := prepareNonce(t, lateSession)
	lateRec, ok := lateSession.sm.GetInference(lateNonce)
	require.True(t, ok)
	lateRefusal := time.Duration(lateSession.sm.Config().RefusalTimeout) * time.Second
	lateSend := time.Now().Add(-lateRefusal - TimeoutBuffer - 2*time.Hour)
	lateHeaders := &countingHeaders{}
	lateHeaders.byHeight = map[uint64]int64{lateRec.StartedAtHeight: lateSend.Add(-time.Hour).UnixMilli()}
	lateSession.StateMachine().SetHeaderTimes(lateHeaders)
	beforeLate := mismatchCount("gateway", "refused")
	lateSession.TimeoutDeadline(lateNonce, lateSend)
	require.Equal(t, beforeLate+1, mismatchCount("gateway", "refused"))
	require.Contains(t, logs.String(), `"mismatch_cause":"both_due"`)
}

// A legacy gateway with no header row still opens on the wall. The miss is
// logged once and does not increment the mismatch counter or get re-read.
func TestLegacyMissingStampDoesNotIncrement(t *testing.T) {
	session, _, _, _ := setupArmedSession(t, 3, 1_000_000, 0)
	nonce := prepareNonce(t, session)
	refusal := time.Duration(session.sm.Config().RefusalTimeout) * time.Second

	headers := &countingHeaders{}
	session.StateMachine().SetHeaderTimes(headers)
	past := time.Now().Add(-refusal - TimeoutBuffer - time.Second)
	before := mismatchCount("gateway", "refused")
	logs := captureWarns(t)
	session.TimeoutDeadline(nonce, past)
	after := headers.calls.Load()
	require.Positive(t, after)
	session.TimeoutDeadline(nonce, past)
	require.Equal(t, after, headers.calls.Load())
	require.Equal(t, before, mismatchCount("gateway", "refused"))
	require.Contains(t, logs.String(), `"mismatch_cause":"stamp_missing"`)
}
