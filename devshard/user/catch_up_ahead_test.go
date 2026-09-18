package user

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/transport"
	"devshard/types"
)

type sizeRecordingClient struct {
	InProcessClient
	mu             sync.Mutex
	bodySizes      []int
	nonceRuns      [][]uint64
	promptedNonces []uint64
	promptDiffs    []types.Diff
	sendErr        error
	sendSignal     chan struct{}
	sendRelease    chan struct{}
	staleNonce     bool
	corruptRoot    bool
}

func (c *sizeRecordingClient) Send(ctx context.Context, req host.HostRequest, stream io.Writer, receiptHandler func(*host.HostResponse)) (*host.HostResponse, error) {
	wire, err := transport.HostRequestToJSON(req)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return nil, err
	}
	nonces := make([]uint64, 0, len(req.Diffs))
	for _, diff := range req.Diffs {
		nonces = append(nonces, diff.Nonce)
	}

	c.mu.Lock()
	c.bodySizes = append(c.bodySizes, len(body))
	c.nonceRuns = append(c.nonceRuns, nonces)
	if req.Payload != nil {
		c.promptedNonces = append(c.promptedNonces, req.Nonce)
		c.promptDiffs = append([]types.Diff(nil), req.Diffs...)
	}
	sendErr := c.sendErr
	signal, release := c.sendSignal, c.sendRelease
	c.mu.Unlock()

	if signal != nil {
		signal <- struct{}{}
	}
	if release != nil {
		<-release
	}
	if sendErr != nil {
		return nil, sendErr
	}
	resp, err := c.InProcessClient.Send(ctx, req, stream, receiptHandler)
	c.mu.Lock()
	stale := c.staleNonce
	c.mu.Unlock()
	if err != nil || resp == nil {
		return resp, err
	}
	c.mu.Lock()
	corrupt := c.corruptRoot
	c.mu.Unlock()
	switch {
	case stale:
		answer := *resp
		answer.Nonce, answer.StateHash, answer.StateSig = 0, nil, nil
		return &answer, nil
	case corrupt && len(resp.StateHash) > 0:
		answer := *resp
		answer.StateHash = append([]byte(nil), resp.StateHash...)
		answer.StateHash[0] ^= 0xff
		return &answer, nil
	}
	return resp, err
}

func (c *sizeRecordingClient) recorded() ([]int, []uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]int(nil), c.bodySizes...), append([]uint64(nil), c.promptedNonces...)
}

func (c *sizeRecordingClient) recordedPromptDiffs() []types.Diff {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]types.Diff(nil), c.promptDiffs...)
}

func (c *sizeRecordingClient) failSends(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sendErr = err
}

func (c *sizeRecordingClient) park(t *testing.T) (signal chan struct{}, releaseAll func()) {
	t.Helper()
	signal, release := make(chan struct{}, 1), make(chan struct{})
	c.mu.Lock()
	c.sendSignal, c.sendRelease = signal, release
	c.mu.Unlock()

	var once sync.Once
	releaseAll = func() {
		once.Do(func() {
			c.mu.Lock()
			c.sendSignal, c.sendRelease = nil, nil
			c.mu.Unlock()
			close(release)
			for len(signal) > 0 {
				<-signal
			}
		})
	}
	t.Cleanup(releaseAll)
	return signal, releaseAll
}

func (c *sizeRecordingClient) clearRecordings() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bodySizes, c.nonceRuns, c.promptedNonces, c.promptDiffs = nil, nil, nil, nil
}

func (c *sizeRecordingClient) recordedNonceRuns() [][]uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][]uint64(nil), c.nonceRuns...)
}

func budgetOf(t *testing.T, session *Session) int {
	t.Helper()
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.catchUpBudgetLocked()
}

func promptOfSize(totalBytes int) []byte {
	prefix := `{"model":"llama","messages":[{"role":"user","content":"`
	suffix := fmt.Sprintf(`"}],"max_tokens":%d}`, testutil.TestMaxTokens)
	padding := totalBytes - len(prefix) - len(suffix)
	if padding < 1 {
		padding = 1
	}
	return []byte(prefix + strings.Repeat("x", padding) + suffix)
}

func inferenceParamsFor(promptBytes []byte) InferenceParams {
	return InferenceParams{
		Model: "llama", Prompt: promptBytes,
		InputLength: uint64(len(promptBytes)), MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
}

func backloggedSession(t *testing.T, promptBytes []byte, diffsThatFit int) (*Session, *PreparedInference, *sizeRecordingClient) {
	t.Helper()
	session, _, _ := setupSession(t, 3, 1_000_000, 10)
	for i := 0; i < 12; i++ {
		_, err := session.sendPendingDiff(context.Background(), nil, nil)
		require.NoError(t, err)
	}

	prepared, err := session.PrepareInference(inferenceParamsFor(promptBytes))
	require.NoError(t, err)

	recorder := &sizeRecordingClient{InProcessClient: *session.clients[prepared.hostIdx].(*InProcessClient)}
	session.mu.Lock()
	session.clients[prepared.hostIdx] = recorder
	session.hostSyncNonce[prepared.hostIdx] = 0
	session.catchUpBudgetBytes = transport.EncodedPromptSize(promptBytes, "llama") +
		diffsThatFit*transport.EncodedDiffSize(session.diffs[0])
	session.mu.Unlock()

	return session, prepared, recorder
}

func TestTheInferenceDrainsTheBacklogBeforeThePrompt(t *testing.T) {
	session, prepared, recorder := backloggedSession(t, promptOfSize(100), 4)

	_, err := session.SendOnly(context.Background(), prepared, nil, nil)
	require.NoError(t, err)

	sizes, prompted := recorder.recorded()
	require.Greater(t, len(sizes), 1, "a backlog over the budget has to travel in more than one body")
	for _, bodyBytes := range sizes {
		require.LessOrEqual(t, bodyBytes, budgetOf(t, session),
			"no body the gateway builds may exceed what the host will read")
	}
	require.Equal(t, []uint64{prepared.diff.Nonce}, prompted,
		"the prompt rides the last request, never a catch-up chunk")
}

func TestTheDrainLeavesRoomForThePrompt(t *testing.T) {
	promptBytes := promptOfSize(4096)
	session, prepared, recorder := backloggedSession(t, promptBytes, 4)

	session.mu.Lock()
	tail := session.diffsUpToNonceLocked(prepared.hostIdx, prepared.diff.Nonce)
	tailBytes := 0
	for _, diff := range tail {
		tailBytes += transport.EncodedDiffSize(diff)
	}
	session.catchUpBudgetBytes = tailBytes + transport.EncodedPromptSize(promptBytes, "llama") - 1
	budgetBytes := session.catchUpBudgetBytes
	session.mu.Unlock()

	_, err := session.SendOnly(context.Background(), prepared, nil, nil)
	require.NoError(t, err)

	sizes, prompted := recorder.recorded()
	require.Greater(t, len(sizes), 1, "the prompt's own bytes must be what forces the drain")
	require.Equal(t, []uint64{prepared.diff.Nonce}, prompted)
	require.LessOrEqual(t, sizes[len(sizes)-1], budgetBytes,
		"the body carrying the prompt is the one that must fit")
}

func TestThePromptRidesTheFreshTailNotTheSnapshot(t *testing.T) {
	session, prepared, recorder := backloggedSession(t, promptOfSize(100), 64)

	_, err := session.SendOnly(context.Background(), prepared, nil, nil)
	require.NoError(t, err)

	tail := recorder.recordedPromptDiffs()
	require.NotEmpty(t, tail)
	require.Equal(t, uint64(1), tail[0].Nonce,
		"the tail must start where the host's cursor is, which the fixture reset to zero")
	require.Equal(t, prepared.diff.Nonce, tail[len(tail)-1].Nonce,
		"the tail still has to end at the nonce the host is asked to sign")
}

func TestTheTailCarriesTheTargetDiffEvenWhenTheCursorPassedIt(t *testing.T) {
	session, prepared, recorder := backloggedSession(t, promptOfSize(100), 64)

	require.NoError(t, session.sendCatchUp(context.Background(), prepared.hostIdx))
	session.mu.Lock()
	cursor := session.hostSyncNonce[prepared.hostIdx]
	session.mu.Unlock()
	require.GreaterOrEqual(t, cursor, prepared.diff.Nonce, "fixture must leave the cursor at or past the target")

	resp, err := session.SendOnly(context.Background(), prepared, nil, nil)
	require.NoError(t, err)

	tail := recorder.recordedPromptDiffs()
	require.NotEmpty(t, tail, "an empty body can never authorize execution")
	require.Equal(t, prepared.diff.Nonce, tail[len(tail)-1].Nonce,
		"the target's own diff has to be in the body the host reads")
	require.NotEmpty(t, resp.Receipt, "with the target diff present the host signs the receipt")
}

func TestAPromptOverTheBudgetNeverSpendsANonce(t *testing.T) {
	session, _, _ := setupSession(t, 3, 1_000_000, 10)
	promptBytes := promptOfSize(8192)

	session.mu.Lock()
	session.catchUpBudgetBytes = transport.EncodedPromptSize(promptBytes, "llama") + minimumDiffReserveBytes
	nonceBefore, diffsBefore, statesBefore := session.nonce, len(session.diffs), len(session.nonceStates)
	session.mu.Unlock()

	_, err := session.PrepareInference(inferenceParamsFor(promptBytes))

	require.ErrorIs(t, err, ErrPromptTooLargeForHost)
	require.ErrorIs(t, err, ErrRequestTooLargeForHost)
	session.mu.Lock()
	require.Equal(t, nonceBefore, session.nonce,
		"a nonce spent here reserves the user's money until a timeout vote releases it")
	require.Len(t, session.diffs, diffsBefore, "a refused prompt must leave no diff behind")
	require.Len(t, session.nonceStates, statesBefore, "nor an outcome to track")
	session.mu.Unlock()
}

func TestAPromptOneByteUnderTheBoundaryIsAccepted(t *testing.T) {
	session, _, _ := setupSession(t, 3, 1_000_000, 10)
	promptBytes := promptOfSize(8192)

	session.mu.Lock()
	session.catchUpBudgetBytes = transport.EncodedPromptSize(promptBytes, "llama") + minimumDiffReserveBytes + 1
	session.mu.Unlock()

	prepared, err := session.PrepareInference(inferenceParamsFor(promptBytes))

	require.NoError(t, err, "the guard must refuse only what genuinely cannot fit")
	require.NotNil(t, prepared)
}

func TestAPromptOverTheBudgetIsNeverSent(t *testing.T) {
	session, prepared, recorder := backloggedSession(t, promptOfSize(4096), 4)
	session.mu.Lock()
	session.catchUpBudgetBytes = 2048
	session.mu.Unlock()

	_, err := session.SendOnly(context.Background(), prepared, nil, nil)
	require.ErrorIs(t, err, ErrPromptTooLargeForHost)

	sizes, _ := recorder.recorded()
	require.Empty(t, sizes, "the gateway must not spend a host round trip on a body it measured as too large")
}

func TestADeadHostDuringTheDrainFailsTheAttempt(t *testing.T) {
	session, prepared, recorder := backloggedSession(t, promptOfSize(100), 4)
	recorder.failSends(fmt.Errorf("connection refused"))

	_, err := session.SendOnly(context.Background(), prepared, nil, nil)

	require.Error(t, err)
	require.NotErrorIs(t, err, ErrRequestTooLargeForHost,
		"a host that dropped the connection is a host failure, not a body the gateway refused to send")
}

func TestAWaiterNeitherSendsNorHoldsWhileTheGateIsTaken(t *testing.T) {
	session, prepared, recorder := backloggedSession(t, promptOfSize(100), 4)
	signal, releaseAll := recorder.park(t)

	holderCtx, cancelHolder := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelHolder()
	holderDone := make(chan struct{})
	go func() {
		defer close(holderDone)
		_, _ = session.catchUpTailForHost(holderCtx, prepared.hostIdx, prepared.diff.Nonce, 0, 0)
	}()
	select {
	case <-signal:
	case <-holderCtx.Done():
		t.Fatal("the holding drain never reached the host")
	}

	waiterCtx, cancelWaiter := context.WithCancel(context.Background())
	waiterErr := make(chan error, 1)
	go func() {
		_, err := session.catchUpTailForHost(waiterCtx, prepared.hostIdx, prepared.diff.Nonce, 0, 0)
		waiterErr <- err
	}()

	select {
	case <-signal:
		t.Fatal("a second drain reached the host while the first one held the gate")
	case <-time.After(250 * time.Millisecond):
	}

	cancelWaiter()
	select {
	case err := <-waiterErr:
		require.ErrorIs(t, err, ErrCatchUpNotStarted,
			"a speculative loser must unwind instead of holding on a drain it no longer needs")
		require.ErrorIs(t, err, context.Canceled, "and it must say why it left")
	case <-time.After(10 * time.Second):
		t.Fatal("the waiter never unwound after its context was cancelled")
	}

	releaseAll()
	select {
	case <-holderDone:
	case <-time.After(10 * time.Second):
		t.Fatal("the holding drain never finished after the gate was released")
	}

	session.mu.Lock()
	session.hostSyncNonce[prepared.hostIdx] = 0
	session.mu.Unlock()
	runsBefore := len(recorder.recordedNonceRuns())
	drainCtx, cancelDrain := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelDrain()
	_, err := session.catchUpTailForHost(drainCtx, prepared.hostIdx, prepared.diff.Nonce, 0, 0)
	require.NoError(t, err, "a released gate lets the next drain through")
	require.Greater(t, len(recorder.recordedNonceRuns()), runsBefore,
		"and that drain had work to do, so it proves the gate was free rather than empty")
}

func TestTheHeartbeatDrainsTheBacklogAndStillDelivers(t *testing.T) {
	session, _, _ := setupSession(t, 3, 1_000_000, 10)
	for i := 0; i < 12; i++ {
		_, err := session.sendPendingDiff(context.Background(), nil, nil)
		require.NoError(t, err)
	}

	session.mu.Lock()
	diff, hostIdx, err := session.composeDiffLocked(nil)
	require.NoError(t, err)
	recorder := &sizeRecordingClient{InProcessClient: *session.clients[hostIdx].(*InProcessClient)}
	session.clients[hostIdx] = recorder
	session.hostSyncNonce[hostIdx] = 0
	session.catchUpBudgetBytes = 4 * transport.EncodedDiffSize(session.diffs[0])
	budgetBytes := session.catchUpBudgetBytes
	session.mu.Unlock()

	require.NoError(t, session.sendComposedDiff(context.Background(), composedDiff{diff: diff, hostIdx: hostIdx}))

	sizes, _ := recorder.recorded()
	require.Greater(t, len(sizes), 1, "a backlog over the budget has to travel in more than one body")
	for _, bodyBytes := range sizes {
		require.LessOrEqual(t, bodyBytes, budgetBytes,
			"a heartbeat body is bounded by the same host cap as an inference body")
	}

	session.mu.Lock()
	defer session.mu.Unlock()
	require.Equal(t, diff.Nonce, session.hostSyncNonce[hostIdx],
		"the composed diff has to reach the host, not just the catch-up chunks")
}

func TestTheCatchUpSkipsForwardWhenTheHostIsAlreadyAhead(t *testing.T) {
	session, prepared, recorder := backloggedSession(t, promptOfSize(100), 4)

	require.NoError(t, session.sendCatchUp(context.Background(), prepared.hostIdx))
	require.Greater(t, len(recorder.recordedNonceRuns()), 1, "fixture must need more than one chunk")

	recorder.clearRecordings()
	session.mu.Lock()
	session.hostSyncNonce[prepared.hostIdx] = 0
	session.mu.Unlock()

	require.NoError(t, session.sendCatchUp(context.Background(), prepared.hostIdx))

	require.Len(t, recorder.recordedNonceRuns(), 1,
		"a host that answers with a later nonce must not be walked through every remaining chunk")
}

func TestATailBiggerThanTheBudgetIsRefusedNotShipped(t *testing.T) {
	session, prepared, recorder := backloggedSession(t, promptOfSize(100), 64)
	require.NoError(t, session.sendCatchUp(context.Background(), prepared.hostIdx))
	recorder.clearRecordings()

	session.mu.Lock()
	session.catchUpBudgetBytes = 128
	session.mu.Unlock()

	_, err := session.catchUpTailForHost(context.Background(), prepared.hostIdx, prepared.diff.Nonce, 0, 0)

	require.ErrorIs(t, err, ErrTailTooLargeForHost)
	require.ErrorIs(t, err, ErrRequestTooLargeForHost)
	require.Empty(t, recorder.recordedNonceRuns(), "a body that cannot fit must not be sent anyway")
}

func TestADiffBiggerThanTheBudgetIsRefusedNotShipped(t *testing.T) {
	session, prepared, recorder := backloggedSession(t, promptOfSize(100), 64)

	session.mu.Lock()
	session.hostSyncNonce[prepared.hostIdx] = 0
	session.catchUpBudgetBytes = 128
	session.mu.Unlock()

	_, err := session.catchUpTailForHost(context.Background(), prepared.hostIdx, prepared.diff.Nonce, 0, 0)

	require.ErrorIs(t, err, ErrTailTooLargeForHost)
	require.Empty(t, recorder.recordedNonceRuns(), "a chunk the host would refuse must not leave the gateway")
}

func TestTheDrainBypassesTheParticipantBudget(t *testing.T) {
	session, prepared, recorder := backloggedSession(t, promptOfSize(100), 4)
	refusing := &admissionRefusingClient{InProcessClient: recorder.InProcessClient}
	session.mu.Lock()
	session.clients[prepared.hostIdx] = refusing
	session.mu.Unlock()

	_, err := session.catchUpTailForHost(context.Background(), prepared.hostIdx, prepared.diff.Nonce, 0, 0)

	require.NoError(t, err)
	require.Zero(t, refusing.refusals,
		"a host locked out by the budget can never catch up, so the drain has to bypass it")
}

func TestAHeartbeatWhoseDrainFailsDeliversNothing(t *testing.T) {
	session, _, _ := setupSession(t, 3, 1_000_000, 10)
	for i := 0; i < 12; i++ {
		_, err := session.sendPendingDiff(context.Background(), nil, nil)
		require.NoError(t, err)
	}

	session.mu.Lock()
	diff, hostIdx, err := session.composeDiffLocked(nil)
	require.NoError(t, err)
	recorder := &sizeRecordingClient{InProcessClient: *session.clients[hostIdx].(*InProcessClient)}
	recorder.failSends(fmt.Errorf("connection refused"))
	session.clients[hostIdx] = recorder
	session.hostSyncNonce[hostIdx] = 0
	session.catchUpBudgetBytes = 4 * transport.EncodedDiffSize(session.diffs[0])
	session.mu.Unlock()

	require.NoError(t, session.sendComposedDiff(context.Background(), composedDiff{diff: diff, hostIdx: hostIdx}),
		"a dead host is not a heartbeat failure; the next turn retries")

	sizes, _ := recorder.recorded()
	require.Len(t, sizes, 1,
		"the failed drain chunk is the only body that may leave the gateway: the composed diff must not follow it")
}

func TestAPromptOverTheBudgetIsRefusedOnACurrentHost(t *testing.T) {
	session, prepared, recorder := backloggedSession(t, promptOfSize(100), 64)
	require.NoError(t, session.sendCatchUp(context.Background(), prepared.hostIdx))
	recorder.clearRecordings()

	session.mu.Lock()
	budgetBytes := session.catchUpBudgetBytes
	session.mu.Unlock()

	_, err := session.catchUpTailForHost(context.Background(), prepared.hostIdx, prepared.diff.Nonce, budgetBytes, 0)

	require.ErrorIs(t, err, ErrPromptTooLargeForHost)
	require.Empty(t, recorder.recordedNonceRuns())
}

func TestAHostThatNeverAdvancesStopsTheDrain(t *testing.T) {
	session, prepared, recorder := backloggedSession(t, promptOfSize(100), 4)
	recorder.mu.Lock()
	recorder.staleNonce = true
	recorder.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := session.catchUpTailForHost(ctx, prepared.hostIdx, prepared.diff.Nonce, 0, 0)

	require.ErrorContains(t, err, "stalled at nonce",
		"a host whose answer never advances would otherwise be asked forever")
	require.NotErrorIs(t, err, context.DeadlineExceeded, "the loop must stop itself, not time out")
	require.Len(t, recorder.recordedNonceRuns(), 1, "one unanswered chunk is enough to give up")
}

func TestTheFinalizeCatchUpWaitsOnTheSameGate(t *testing.T) {
	session, prepared, recorder := backloggedSession(t, promptOfSize(100), 4)
	signal, releaseAll := recorder.park(t)

	holderCtx, cancelHolder := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelHolder()
	holderDone := make(chan struct{})
	go func() {
		defer close(holderDone)
		_, _ = session.catchUpTailForHost(holderCtx, prepared.hostIdx, prepared.diff.Nonce, 0, 0)
	}()
	select {
	case <-signal:
	case <-holderCtx.Done():
		t.Fatal("the holding drain never reached the host")
	}

	waiterCtx, cancelWaiter := context.WithCancel(context.Background())
	waiterErr := make(chan error, 1)
	go func() { waiterErr <- session.sendCatchUp(waiterCtx, prepared.hostIdx) }()
	select {
	case <-signal:
		t.Fatal("the finalize catch-up reached the host while a drain held the gate")
	case <-time.After(250 * time.Millisecond):
	}

	cancelWaiter()
	select {
	case err := <-waiterErr:
		require.ErrorIs(t, err, ErrCatchUpNotStarted,
			"a slot it never reached must not be reported as caught up")
	case <-time.After(10 * time.Second):
		t.Fatal("the finalize catch-up never unwound")
	}

	releaseAll()
	select {
	case <-holderDone:
	case <-time.After(10 * time.Second):
		t.Fatal("the holding drain never finished after the gate was released")
	}
}

func TestTheFinalizeCatchUpRefusesADiffOverTheBudget(t *testing.T) {
	session, prepared, recorder := backloggedSession(t, promptOfSize(100), 64)
	session.mu.Lock()
	session.hostSyncNonce[prepared.hostIdx] = 0
	session.catchUpBudgetBytes = 128
	session.mu.Unlock()

	err := session.sendCatchUp(context.Background(), prepared.hostIdx)

	require.ErrorIs(t, err, ErrTailTooLargeForHost,
		"both catch-up paths have to refuse the same body")
	require.Empty(t, recorder.recordedNonceRuns())
}

func TestTheHeartbeatDoesNotWaitOutAnInferenceDrain(t *testing.T) {
	session, prepared, recorder := backloggedSession(t, promptOfSize(100), 4)
	session.mu.Lock()
	session.heartbeatGateWaitOverride = time.Millisecond
	var diff types.Diff
	var hostIdx int
	for attempt := 0; attempt <= len(session.group); attempt++ {
		var err error
		diff, hostIdx, err = session.composeDiffLocked(nil)
		require.NoError(t, err)
		if hostIdx == prepared.hostIdx {
			break
		}
	}
	require.Equal(t, prepared.hostIdx, hostIdx, "fixture needs the heartbeat bound to the busy slot")
	session.mu.Unlock()

	signal, releaseAll := recorder.park(t)
	holderCtx, cancelHolder := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelHolder()
	holderDone := make(chan struct{})
	go func() {
		defer close(holderDone)
		_, _ = session.catchUpTailForHost(holderCtx, prepared.hostIdx, prepared.diff.Nonce, 0, 0)
	}()
	select {
	case <-signal:
	case <-holderCtx.Done():
		t.Fatal("the holding drain never reached the host")
	}

	done := make(chan error, 1)
	go func() {
		done <- session.sendComposedDiff(context.Background(), composedDiff{diff: diff, hostIdx: hostIdx})
	}()
	select {
	case err := <-done:
		require.NoError(t, err, "a busy slot is skipped, not an error")
	case <-time.After(5 * time.Second):
		t.Fatal("the cadence loop queued behind an inference drain")
	}

	releaseAll()
	select {
	case <-holderDone:
	case <-time.After(10 * time.Second):
		t.Fatal("the holding drain never finished after the gate was released")
	}
}

func TestADivergedHostFailsTheDrainByHashNotBySize(t *testing.T) {
	session, prepared, recorder := backloggedSession(t, promptOfSize(100), 4)
	recorder.mu.Lock()
	recorder.corruptRoot = true
	recorder.mu.Unlock()

	_, err := session.SendOnly(context.Background(), prepared, nil, nil)

	require.ErrorIs(t, err, types.ErrStateHashMismatch,
		"the gateway's own guards key off this sentinel travelling out of the drain")
	require.NotErrorIs(t, err, ErrRequestTooLargeForHost, "a disagreement is not an oversize refusal")
}

func TestATargetDiffOverTheBudgetRefusesTheTail(t *testing.T) {
	promptBytes := promptOfSize(100)
	session, prepared, recorder := backloggedSession(t, promptBytes, 64)
	require.NoError(t, session.sendCatchUp(context.Background(), prepared.hostIdx))
	recorder.clearRecordings()

	session.mu.Lock()
	tail := session.diffsUpToNonceLocked(prepared.hostIdx, prepared.diff.Nonce)
	require.Len(t, tail, 1, "fixture needs a current host")
	session.catchUpBudgetBytes = transport.EncodedPromptSize(promptBytes, "llama") +
		transport.EncodedDiffSize(tail[0]) - 1
	session.mu.Unlock()

	_, err := session.SendOnly(context.Background(), prepared, nil, nil)

	require.ErrorIs(t, err, ErrTailTooLargeForHost,
		"another nonce composes a different diff, so this must stay escalatable")
	require.Empty(t, recorder.recordedNonceRuns())
}

func TestTheHeartbeatGateWaitIsBoundedByDefault(t *testing.T) {
	session, _, _ := setupSession(t, 3, 1_000_000, 10)

	require.LessOrEqual(t, session.heartbeatGateWait(), 2*time.Second,
		"an unbounded default stalls the whole cadence loop behind one busy slot")
	require.Positive(t, session.heartbeatGateWait())
}

func TestAVerifierBodyIsDrainedBeforeItIsBuilt(t *testing.T) {
	session, prepared, recorder := backloggedSession(t, promptOfSize(100), 4)
	verifierIdx := prepared.hostIdx

	session.mu.Lock()
	backlogBefore := len(session.diffsForHost(verifierIdx))
	budgetBytes := session.catchUpBudgetLocked()
	session.mu.Unlock()
	require.Greater(t, backlogBefore, 4, "fixture needs a backlog that does not fit one body")

	tail, ok := session.verifierCatchUpTail(context.Background(), verifierIdx, 0)

	require.True(t, ok)
	require.Less(t, len(tail), backlogBefore, "the verify body must carry less than the whole backlog")
	require.True(t, catchUpFitsOneBody(tail, 0, budgetBytes),
		"what is left has to fit the body the verifier will read")
	require.NotEmpty(t, recorder.recordedNonceRuns(), "the backlog moved by being sent, not by being dropped")
}

func TestAVerifierBodyLeavesRoomForItsReserve(t *testing.T) {
	session, prepared, recorder := backloggedSession(t, promptOfSize(100), 8)
	verifierIdx := prepared.hostIdx

	session.mu.Lock()
	budgetBytes := session.catchUpBudgetLocked()
	session.mu.Unlock()

	noReserveTail, ok := session.verifierCatchUpTail(context.Background(), verifierIdx, 0)
	require.True(t, ok)
	require.Greater(t, len(noReserveTail), 1, "fixture needs room to shrink")

	reserveBytes := budgetBytes - transport.EncodedDiffSize(noReserveTail[len(noReserveTail)-1]) - 1
	session.mu.Lock()
	session.hostSyncNonce[verifierIdx] = 0
	session.mu.Unlock()
	runsBefore := len(recorder.recordedNonceRuns())

	reservedTail, ok := session.verifierCatchUpTail(context.Background(), verifierIdx, reserveBytes)

	require.True(t, ok)
	require.Len(t, reservedTail, 1, "the reserve has to make the drain go further, or it is not being charged")
	require.True(t, catchUpFitsOneBody(reservedTail, reserveBytes, budgetBytes))
	require.Greater(t, len(recorder.recordedNonceRuns()), runsBefore)
}

func TestAVerifyBodyReserveCountsEveryFieldItCarries(t *testing.T) {
	payload := &host.InferencePayload{Prompt: promptOfSize(4096), Model: "llama"}
	artifacts := host.TimeoutArtifacts{FinishTx: make([]byte, 2048), ResponsePayload: make([]byte, 8192)}

	require.Equal(t,
		transport.EncodedPromptSize(payload.Prompt, payload.Model)+
			transport.EncodedBytesSize(artifacts.FinishTx)+
			transport.EncodedBytesSize(artifacts.ResponsePayload),
		verifyBodyReserveBytes(payload, artifacts))
	require.Equal(t,
		transport.EncodedBytesSize(artifacts.FinishTx)+transport.EncodedBytesSize(artifacts.ResponsePayload),
		verifyBodyReserveBytes(nil, artifacts))
	require.Equal(t, transport.EncodedPromptSize(payload.Prompt, payload.Model)+
		transport.EncodedBytesSize(nil)*2,
		verifyBodyReserveBytes(payload, host.TimeoutArtifacts{}))
}

func TestADrainThatFailsLeavesTheBodyAsItWas(t *testing.T) {
	session, prepared, recorder := backloggedSession(t, promptOfSize(100), 4)
	recorder.failSends(fmt.Errorf("connection refused"))

	session.mu.Lock()
	backlogBefore := len(session.diffsForHost(prepared.hostIdx))
	session.mu.Unlock()

	tail, ok := session.verifierCatchUpTail(context.Background(), prepared.hostIdx, 0)

	require.False(t, ok, "a failed drain must tell the caller to fall back")
	require.Nil(t, tail)
	require.Len(t, session.catchUpDiffsForVerifier(prepared.hostIdx), backlogBefore,
		"and it must not have changed what the fallback body carries")
}

type diffCapturingVerifier struct {
	nopErrorMiss
	mu       sync.Mutex
	captured []types.Diff
	calls    int
}

func (v *diffCapturingVerifier) VerifyTimeout(_ context.Context, _ uint64, _ types.TimeoutReason, _ *host.InferencePayload, diffs []types.Diff, _ host.TimeoutArtifacts) (bool, []byte, uint32, []*types.DevshardTx, string, error) {
	v.mu.Lock()
	v.captured = append([]types.Diff(nil), diffs...)
	v.calls++
	v.mu.Unlock()
	return false, nil, 0, nil, "", nil
}

func (v *diffCapturingVerifier) recorded() ([]types.Diff, int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]types.Diff(nil), v.captured...), v.calls
}

func TestTheVoteBodyCarriesTheDrainedTail(t *testing.T) {
	session, prepared, _ := backloggedSession(t, promptOfSize(100), 4)
	verifierIdx := (prepared.hostIdx + 1) % len(session.group)
	verifierRecorder := &sizeRecordingClient{InProcessClient: *session.clients[verifierIdx].(*InProcessClient)}
	verifier := &diffCapturingVerifier{}

	session.mu.Lock()
	session.clients[verifierIdx] = verifierRecorder
	session.hostSyncNonce[verifierIdx] = 0
	budgetBytes := session.catchUpBudgetLocked()
	backlogBefore := len(session.diffsForHost(verifierIdx))
	session.mu.Unlock()
	require.Greater(t, backlogBefore, 4)

	payload := &host.InferencePayload{
		Prompt: promptOfSize(100), Model: "llama",
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
	_, _, _, _ = session.CollectTimeoutVotes(context.Background(), prepared.diff.Nonce,
		types.TimeoutReason_TIMEOUT_REASON_REFUSED, payload,
		map[int]TimeoutVerifier{verifierIdx: verifier}, nil)

	captured, calls := verifier.recorded()
	require.Equal(t, 1, calls, "the vote has to reach the verifier")
	require.NotEmpty(t, captured, "a verifier that knows nothing about the inference cannot vote")
	require.Less(t, len(captured), backlogBefore, "the body must carry the drained tail, not the whole backlog")
	require.True(t, catchUpFitsOneBody(captured, verifyBodyReserveBytes(payload, host.TimeoutArtifacts{}), budgetBytes),
		"and it has to fit beside what else the body carries")
	require.NotEmpty(t, verifierRecorder.recordedNonceRuns(), "the backlog moved by being sent")
}
