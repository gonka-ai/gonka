package user

import (
	"bytes"
	"context"
	"log"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"common/chainoracle/blocks"
	"devshard/chainoracle/blocks/headertime"
	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

func startedNonce(t *testing.T, session *Session, hosts []*signing.Secp256k1Signer, confirmedAt int64) uint64 {
	t.Helper()
	nonce := prepareNonce(t, session)
	record, tracked := session.sm.GetInference(nonce)
	require.True(t, tracked)
	stamp := floorStampOf(&record)
	receipt, err := proto.MarshalOptions{Deterministic: true}.Marshal(&types.ExecutorReceiptContent{
		InferenceId:       nonce,
		PromptHash:        record.PromptHash,
		Model:             record.Model,
		InputLength:       record.InputLength,
		MaxTokens:         record.MaxTokens,
		EscrowId:          session.escrowID,
		ObservedHeight:    stamp.Height,
		ObservedBlockHash: stamp.Hash,
	})
	require.NoError(t, err)
	signature, err := hosts[record.ExecutorSlot].Sign(receipt)
	require.NoError(t, err)

	require.NoError(t, session.ProcessResponse(int(nonce%3), &host.HostResponse{
		Nonce: nonce, Receipt: signature, ConfirmedAt: confirmedAt,
		ObservedHeight: stamp.Height, ObservedBlockHash: stamp.Hash,
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
//  3. Expect an execution deadline from the wall clock against time(ConfirmedAtHeight). The forgotten wall clock of the receipt is not the deadline.
func TestTimeoutDeadlineReadsTheConfirmedHeightAfterARestart(t *testing.T) {
	session, hosts, _, _ := setupArmedSession(t, 3, 1_000_000, 0)
	nonce := startedNonce(t, session, hosts, time.Now().Unix())

	session.mu.Lock()
	delete(session.nonceStates, nonce)
	session.mu.Unlock()

	exec := time.Duration(session.sm.Config().ExecutionTimeout) * time.Second
	headers := &countingHeaders{}
	session.StateMachine().SetHeaderTimes(headers)
	stamp := time.Now().Add(-exec + time.Millisecond)
	setConfirmedHeightTime(t, session, headers, nonce, stamp.UnixMilli())

	reason, deadline := session.TimeoutDeadline(nonce, time.Now())

	require.Equal(t, "execution", reason, "the record carries the receipt this session forgot")
	require.WithinDuration(t, time.UnixMilli(stamp.UnixMilli()).Add(exec), deadline, 20*time.Millisecond)
	require.Positive(t, headers.calls.Load())
}

// Test flow:
//  1. Prepare an inference that no host has confirmed.
//  2. Expect a refusal deadline counted from the send time, the only reason the chain admits for it.
func TestTimeoutDeadlineKeepsARecordWithoutTheStampRefused(t *testing.T) {
	// This deadline used to include TimeoutBuffer. Adding the buffer back makes
	// the equality fail.
	prev := TimeoutBuffer
	TimeoutBuffer = 5 * time.Second
	t.Cleanup(func() { TimeoutBuffer = prev })

	session, _, _ := setupSession(t, 3, 1_000_000, 0)
	nonce := prepareNonce(t, session)
	sendTime := time.Now()

	reason, deadline := session.TimeoutDeadline(nonce, sendTime)

	require.Equal(t, "refused", reason)
	require.Equal(t, sendTime.Add(time.Duration(session.sm.Config().RefusalTimeout)*time.Second), deadline)
}

// countingHeaders counts map reads. ms == 0 is an empty map. AwaitHeaderTimeMs
// and GetBlockHeader count calls the execution trigger must not make.
type countingHeaders struct {
	calls     atomic.Int32
	awaited   atomic.Int32
	blockGets atomic.Int32
	ms        atomic.Int64
	byHeight  map[uint64]int64
}

func (c *countingHeaders) HeaderTimeMs(height uint64, _ []byte) (int64, error) {
	c.calls.Add(1)
	if c.byHeight != nil {
		if ms, ok := c.byHeight[height]; ok && ms > 0 {
			return ms, nil
		}
		return 0, types.ErrHeaderPending
	}
	if ms := c.ms.Load(); ms != 0 {
		return ms, nil
	}
	return 0, types.ErrHeaderPending
}

// StoredAt is the height-keyed read the execution deadline uses. It does not
// fetch. A height that is not in the map stays a miss.
func (c *countingHeaders) StoredAt(height uint64) (headertime.Record, bool) {
	ms, err := c.HeaderTimeMs(height, []byte{1})
	if err != nil || ms <= 0 {
		return headertime.Record{}, false
	}
	return headertime.Record{TimeMs: ms}, true
}

// setConfirmedHeightTime installs time(ConfirmedAtHeight). Other heights miss.
func setConfirmedHeightTime(t *testing.T, session *Session, headers *countingHeaders, nonce uint64, ms int64) {
	t.Helper()
	rec, ok := session.sm.GetInference(nonce)
	require.True(t, ok)
	require.NotZero(t, rec.ConfirmedAtHeight)
	headers.byHeight = map[uint64]int64{rec.ConfirmedAtHeight: ms}
}

func (c *countingHeaders) AwaitHeaderTimeMs(context.Context, uint64, []byte) (int64, error) {
	c.awaited.Add(1)
	return 0, types.ErrHeaderPending
}

func (c *countingHeaders) GetBlockHeader(context.Context, int64) error {
	c.blockGets.Add(1)
	return types.ErrHeaderPending
}

func TestRefusalOpensOnTheGatewayWallClock(t *testing.T) {
	prev := TimeoutBuffer
	TimeoutBuffer = 7 * time.Second
	t.Cleanup(func() { TimeoutBuffer = prev })

	session, hosts, _, _ := setupArmedSession(t, 3, 1_000_000, 0)
	nonce := prepareNonce(t, session)
	rec, ok := session.sm.GetInference(nonce)
	require.True(t, ok)
	require.Equal(t, types.StatusPending, rec.Status)
	require.NotZero(t, rec.StartedAtHeight, "the pending inference carries a start stamp")

	refusal := time.Duration(session.sm.Config().RefusalTimeout) * time.Second
	sendTime := time.Unix(1_700_000_000, 0)
	headers := &countingHeaders{}
	session.StateMachine().SetHeaderTimes(headers)

	reason, deadline := session.TimeoutDeadline(nonce, sendTime)
	require.Equal(t, "refused", reason)
	require.Equal(t, sendTime.Add(refusal), deadline)
	require.Zero(t, headers.calls.Load(), "an empty map is not read")
	require.False(t, refusalPast(deadline.Add(-time.Millisecond), deadline), "one millisecond earlier is not past the deadline")
	require.True(t, refusalPast(deadline.Add(time.Millisecond), deadline))

	// A header time for the start stamp, far enough ahead that the floor check
	// would call the refusal due now. The wall-clock deadline does not move,
	// and the map is still not read.
	headers.ms.Store(time.Now().Add(24 * time.Hour).UnixMilli())
	headers.calls.Store(0)
	reason, filled := session.TimeoutDeadline(nonce, sendTime)
	require.Equal(t, "refused", reason)
	require.Equal(t, deadline, filled)
	require.Zero(t, headers.calls.Load())

	session.StateMachine().SetHeaderTimes(headers)
	headers.ms.Store(0)

	var opened atomic.Int32
	for i := range session.clients {
		session.clients[i] = &verifyingHost{HostClient: session.clients[i], mockTimeoutVerifier: &mockTimeoutVerifier{
			accept: true, signer: hosts[i], group: session.group, slotIdx: i,
			onVerify: func() { opened.Add(1) },
		}}
	}

	earlySend := time.Now().Add(-refusal + time.Second)
	result, needsWait, err := session.VoteTimeout(context.Background(), nonce, earlySend, nil, types.TimeoutReason_TIMEOUT_REASON_REFUSED)
	require.NoError(t, err)
	require.True(t, needsWait)
	require.Equal(t, "skipped", result.Outcome)
	require.Equal(t, "deadline_not_due", result.DetailReason)
	require.Zero(t, opened.Load(), "one second early, which includes one millisecond early, does not open")

	headers.calls.Store(0)
	headers.ms.Store(0)
	pastSend := time.Now().Add(-refusal - time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	waitReason, ready, skipped, err := session.WaitTimeoutDeadline(ctx, nonce, pastSend)
	require.NoError(t, err)
	require.True(t, ready, "skipped outcome=%s detail=%s", skipped.Outcome, skipped.DetailReason)
	require.Equal(t, types.TimeoutReason_TIMEOUT_REASON_REFUSED, waitReason)

	result, _, _ = session.VoteTimeout(ctx, nonce, pastSend, nil, types.TimeoutReason_TIMEOUT_REASON_REFUSED)
	require.Positive(t, opened.Load(), "a wall clock one millisecond past the deadline opens the refusal")
	require.Equal(t, "refused", result.Reason)
	require.NotEqual(t, "skipped", result.Outcome)
}

// Test flow:
//  1. Pending inference with a start stamp; forget the live sendTime (restart).
//  2. Empty map → refusal stays closed (poll), not ErrTimeoutNotArmed.
//  3. Seed time(StartedAtHeight) past RefusalTimeout → deadline opens from the map.
func TestRefusalDeadlineRestoresFromTheHeaderMapAfterRestart(t *testing.T) {
	session, hosts, _, _ := setupArmedSession(t, 3, 1_000_000, 0)
	nonce := prepareNonce(t, session)
	rec, ok := session.sm.GetInference(nonce)
	require.True(t, ok)
	require.NotZero(t, rec.StartedAtHeight)

	refusal := time.Duration(session.sm.Config().RefusalTimeout) * time.Second
	headers := &countingHeaders{}
	session.StateMachine().SetHeaderTimes(headers)

	reason, deadline := session.TimeoutDeadline(nonce, time.Time{})
	require.Equal(t, "refused", reason)
	require.True(t, deadline.After(time.Now()), "a map miss does not open the refusal")
	require.False(t, session.timeoutUnarmed(nonce), "header_pending is not unarmed")

	stamp := time.Now().Add(-refusal - time.Millisecond)
	headers.byHeight = map[uint64]int64{rec.StartedAtHeight: stamp.UnixMilli()}
	headers.calls.Store(0)

	reason, deadline = session.TimeoutDeadline(nonce, time.Time{})
	require.Equal(t, "refused", reason)
	require.True(t, refusalPast(time.Now(), deadline), "map stamp past RefusalTimeout opens now")
	require.Positive(t, headers.calls.Load(), "restart refusal reads StoredAt")

	var opened atomic.Int32
	for i := range session.clients {
		session.clients[i] = &verifyingHost{HostClient: session.clients[i], mockTimeoutVerifier: &mockTimeoutVerifier{
			accept: true, signer: hosts[i], group: session.group, slotIdx: i,
			onVerify: func() { opened.Add(1) },
		}}
	}
	result, needsWait, _ := session.VoteTimeout(context.Background(), nonce, time.Time{}, nil, types.TimeoutReason_TIMEOUT_REASON_REFUSED)
	require.False(t, needsWait)
	require.Positive(t, opened.Load())
	require.Equal(t, "refused", result.Reason)
	require.NotEqual(t, "skipped", result.Outcome)
}

func TestExecutionOpensOnTheConfirmedHeight(t *testing.T) {
	prev := TimeoutBuffer
	TimeoutBuffer = 7 * time.Second
	t.Cleanup(func() { TimeoutBuffer = prev })

	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	config := testutil.DefaultConfig(len(hosts))
	config.ExecutionTimeout = 5
	session, _, rig := armedSessionFor(t, testutil.MakeGroup(hosts), hosts, config, 1_000_000, 0)
	nonce := startedNonce(t, session, hosts, time.Now().Unix())
	pending := prepareNonce(t, session)
	exec := time.Duration(session.sm.Config().ExecutionTimeout) * time.Second
	refusal := time.Duration(session.sm.Config().RefusalTimeout) * time.Second

	var opened atomic.Int32
	for i := range session.clients {
		session.clients[i] = &verifyingHost{HostClient: session.clients[i], mockTimeoutVerifier: &mockTimeoutVerifier{
			accept: false, signer: hosts[i], group: session.group, slotIdx: i,
			onVerify: func() { opened.Add(1) },
		}}
	}
	started, ok := session.sm.GetInference(nonce)
	require.True(t, ok)
	stamp, ok := session.StateMachine().LookupHeaderTimeMs(started.ConfirmedAtHeight, []byte{0xaa})
	require.True(t, ok)
	stored, ok := rig.gateway.StoredAt(started.ConfirmedAtHeight)
	require.True(t, ok)
	require.Equal(t, headertime.OriginDAPI, stored.Origin)
	require.Equal(t, stamp, stored.TimeMs)

	reason, deadline := session.TimeoutDeadline(nonce, time.Now())
	require.Equal(t, "execution", reason)
	want := time.UnixMilli(stamp).Add(exec)
	require.WithinDuration(t, want, deadline, 20*time.Millisecond)
	require.False(t, refusalPast(want.Add(-time.Millisecond), want))
	require.NotEqual(t, want.Add(TimeoutBuffer), deadline)

	if time.Now().Add(time.Millisecond).Before(want) {
		result, needsWait, err := session.VoteTimeout(context.Background(), nonce, time.Now(), nil, types.TimeoutReason_TIMEOUT_REASON_EXECUTION)
		require.NoError(t, err)
		require.True(t, needsWait)
		require.Equal(t, "skipped", result.Outcome)
		require.Equal(t, "deadline_not_due", result.DetailReason)
		require.Zero(t, opened.Load())
	}
	if d := time.Until(want); d > 0 {
		time.Sleep(d + 15*time.Millisecond)
	}
	opened.Store(0)
	result, _, _ := session.VoteTimeout(context.Background(), nonce, time.Now(), nil, types.TimeoutReason_TIMEOUT_REASON_EXECUTION)
	require.Positive(t, opened.Load())
	require.Equal(t, "execution", result.Reason)
	require.NotEqual(t, "skipped", result.Outcome)

	// confirmed_at is already past and the map has no time: do not open, and log.
	headers := &countingHeaders{}
	session.StateMachine().SetHeaderTimes(headers)
	session.mu.Lock()
	session.nonceStates[nonce].confirmedAt = time.Now().Add(-exec - time.Second).Unix()
	session.nonceStates[nonce].feedBehindLogged = false
	session.mu.Unlock()
	var logs bytes.Buffer
	prevLog := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(prevLog)
	opened.Store(0)
	result, needsWait, err := session.VoteTimeout(context.Background(), nonce, time.Now(), nil, types.TimeoutReason_TIMEOUT_REASON_EXECUTION)
	require.NoError(t, err)
	require.True(t, needsWait)
	require.Equal(t, "skipped", result.Outcome)
	require.Zero(t, opened.Load())
	require.Contains(t, logs.String(), "gateway block feed is behind the chain")
	require.Zero(t, headers.awaited.Load())
	require.Zero(t, headers.blockGets.Load())

	// The poller's stamp is already past the interval. Putting that map back opens
	// the challenge without a header fetch.
	session.StateMachine().SetHeaderTimes(rig.gateway)
	opened.Store(0)
	result, _, _ = session.VoteTimeout(context.Background(), nonce, time.Now(), nil, types.TimeoutReason_TIMEOUT_REASON_EXECUTION)
	require.Positive(t, opened.Load())
	require.Equal(t, "execution", result.Reason)
	require.NotEqual(t, "skipped", result.Outcome)
	require.Zero(t, headers.awaited.Load(), "the gap did not call AwaitHeaderTimeMs")
	require.Zero(t, headers.blockGets.Load(), "the gap did not call GetBlockHeader")

	// A refusal in the same test still opens from the wall clock on an empty map.
	headers.byHeight = nil
	headers.ms.Store(0)
	opened.Store(0)
	pastSend := time.Now().Add(-refusal - time.Millisecond)
	result, _, _ = session.VoteTimeout(context.Background(), pending, pastSend, nil, types.TimeoutReason_TIMEOUT_REASON_REFUSED)
	require.Positive(t, opened.Load())
	require.Equal(t, "refused", result.Reason)
	require.NotEqual(t, "skipped", result.Outcome)
}

// TestExecutionOpensWhenConfirmSitsBelowTheFloor is a confirm stamped at 102
// after an ack has already raised F to 105. The floor index keeps running
// maxima only, so 102 is not an entry. The header map still has that height.
// ExecutionTimeout is 5s and the stored time is already older than that, so
// the challenge is due. An empty map stays closed and the existing sweep
// retries it; putting the row back opens the challenge.
func TestExecutionOpensWhenConfirmSitsBelowTheFloor(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	config := testutil.DefaultConfig(len(hosts))
	config.ExecutionTimeout = 5
	session, _, rig := armedSessionFor(t, testutil.MakeGroup(hosts), hosts, config, 1_000_000, 0)
	for i := range session.clients {
		session.clients[i] = noReceiptClient{HostClient: session.clients[i]}
	}

	nonce := prepareNonce(t, session)
	require.NoError(t, session.SendPendingDiff(context.Background()))
	pending, ok := session.sm.GetInference(nonce)
	require.True(t, ok)
	require.Equal(t, types.StatusPending, pending.Status)
	require.NotZero(t, pending.StartedAtHeight)
	require.Less(t, pending.StartedAtHeight, uint64(102))

	const confirmHeight = uint64(102)
	hash := []byte{0xaa}
	past := time.Now().Add(-6 * time.Second)
	hdr := blocks.HashOnlyHeader(int64(confirmHeight), past, "mockchain", hash)
	rig.gateway.Observe(hdr, headertime.OriginDAPI, true)
	for _, src := range rig.hosts {
		src.Observe(hdr, headertime.OriginDAPI, true)
	}

	rig.raise(t, session, 105)
	floor, _, known := session.StateMachine().HeightSyncFloorAsOf(session.Nonce() + 1)
	require.True(t, known)
	require.GreaterOrEqual(t, floor, uint64(105))

	sig := testutil.SignExecutorReceipt(t, hosts[pending.ExecutorSlot], session.escrowID, nonce,
		pending.PromptHash, pending.Model, pending.InputLength, pending.MaxTokens,
		testutil.ReceiptStamp{Height: confirmHeight, Hash: hash})
	require.NoError(t, session.ProcessResponse(int(nonce%uint64(len(hosts))), &host.HostResponse{
		Nonce: nonce, Receipt: sig, ConfirmedAt: time.Now().Unix(),
		ObservedHeight: confirmHeight, ObservedBlockHash: hash,
	}, nonce))
	require.NoError(t, session.SendPendingDiff(context.Background()))

	started, ok := session.sm.GetInference(nonce)
	require.True(t, ok)
	require.Equal(t, types.StatusStarted, started.Status)
	require.Equal(t, confirmHeight, started.ConfirmedAtHeight)
	_, indexed := session.StateMachine().FloorPointAtHeight(confirmHeight)
	require.False(t, indexed, "102 never raised F, so the floor index has no row")
	stored, ok := rig.gateway.StoredAt(confirmHeight)
	require.True(t, ok)
	require.Equal(t, past.UnixMilli(), stored.TimeMs)

	exec := time.Duration(config.ExecutionTimeout) * time.Second
	require.True(t, time.UnixMilli(stored.TimeMs).Add(exec).Before(time.Now()))
	reason, deadline := session.TimeoutDeadline(nonce, time.Now())
	require.Equal(t, "execution", reason)
	// A due trigger is placed one millisecond in the past so the vote gate opens.
	// A miss would wake in the future, on the feed poll.
	require.True(t, deadline.Before(time.Now()))

	session.mu.Lock()
	session.nonceStates[nonce].confirmedAt = time.Now().Add(-exec - time.Second).Unix()
	session.nonceStates[nonce].feedBehindLogged = false
	session.mu.Unlock()

	var opened atomic.Int32
	for i, c := range session.clients {
		inner := c
		if wrapped, ok := c.(noReceiptClient); ok {
			inner = wrapped.HostClient
		}
		session.clients[i] = &verifyingHost{HostClient: inner, mockTimeoutVerifier: &mockTimeoutVerifier{
			accept: false, signer: hosts[i], group: session.group, slotIdx: i,
			onVerify: func() { opened.Add(1) },
		}}
	}

	headers := &countingHeaders{}
	session.StateMachine().SetHeaderTimes(headers)
	var logs bytes.Buffer
	prevLog := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(prevLog)
	result, needsWait, err := session.VoteTimeout(context.Background(), nonce, time.Now(), nil, types.TimeoutReason_TIMEOUT_REASON_EXECUTION)
	require.NoError(t, err)
	require.True(t, needsWait)
	require.Equal(t, "skipped", result.Outcome)
	require.Zero(t, opened.Load())
	require.Contains(t, logs.String(), "gateway block feed is behind the chain")
	require.Zero(t, headers.awaited.Load())
	require.Zero(t, headers.blockGets.Load())

	session.StateMachine().SetHeaderTimes(rig.gateway)
	opened.Store(0)
	result, _, _ = session.VoteTimeout(context.Background(), nonce, time.Now(), nil, types.TimeoutReason_TIMEOUT_REASON_EXECUTION)
	require.Positive(t, opened.Load())
	require.Equal(t, "execution", result.Reason)
	require.NotEqual(t, "skipped", result.Outcome)
	require.Zero(t, headers.awaited.Load(), "the gap did not call AwaitHeaderTimeMs")
	require.Zero(t, headers.blockGets.Load(), "the gap did not call GetBlockHeader")
}

func TestTimeoutDeadlineDoesNotOverflowWhenRemainingDurationIsSaturated(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	config := testutil.DefaultConfig(len(hosts))
	config.RefusalTimeout, config.ExecutionTimeout = math.MaxInt64, math.MaxInt64
	session, _, _ := armedSessionFor(t, testutil.MakeGroup(hosts), hosts, config, 1_000_000, 0)
	pending := prepareNonce(t, session)
	reason, deadline := session.TimeoutDeadline(pending, time.Now())
	require.Equal(t, "refused", reason)
	require.True(t, deadline.After(time.Now().Add(time.Hour)))
	started := startedNonce(t, session, hosts, time.Now().Unix())
	reason, deadline = session.TimeoutDeadline(started, time.Now())
	require.Equal(t, "execution", reason)
	require.True(t, deadline.After(time.Now().Add(time.Hour)))
}
