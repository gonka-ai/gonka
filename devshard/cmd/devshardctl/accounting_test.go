package main

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/accounting"
	"devshard/host"
	"devshard/internal/statetest"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
	"devshard/user"
)

func TestGatewayAccountingAdapterRecordsEvents(t *testing.T) {
	tracker := newGatewayAccountingTestTracker(t)
	recorder := accounting.NewRecorder(tracker, nil)
	require.NotNil(t, recorder)
	registerGatewayAccountingTestEscrow(t, tracker, "e1", 1, "m")
	require.NoError(t, tracker.RecordDiff("e1", 1, true))
	recorder.Ghost("e1", 1, "participant_throttled_no_send", "probe", false)
	require.NoError(t, tracker.RecordDiff("e1", 2, true))
	recorder.RealSend("e1", 2, time.Now().Add(-time.Second), "shadow")
	recorder.TimeoutResult("e1", 2, "refused", "failed", "insufficient_votes", "no_receipt", "")

	records := tracker.Query(accounting.QueryFilter{EpochIndex: 1})
	var ghost, unfinished uint64
	for _, record := range records {
		ghost += record.Dispositions[accounting.DispositionGhost]
		unfinished += record.Dispositions[accounting.DispositionUnfinishedRefused]
	}
	require.Equal(t, uint64(1), ghost)
	require.Equal(t, uint64(1), unfinished)
}

func TestGatewayTimeoutFailureActionKeepsUnknownVisible(t *testing.T) {
	action, reason := gatewayTimeoutFailureAction(user.TimeoutResult{}, false)
	require.Equal(t, "failed", action)
	require.Equal(t, "unknown", reason)
}

func TestHandleTimeoutClassifiesVoteFailuresWithoutExposingWeights(t *testing.T) {
	oldBuffer := user.TimeoutBuffer
	user.TimeoutBuffer = 0
	t.Cleanup(func() { user.TimeoutBuffer = oldBuffer })

	t.Run("insufficient votes", func(t *testing.T) {
		env := setupTestProxy(t, 3, nil, false)
		prepared, err := env.session.PrepareInference(defaultParams())
		require.NoError(t, err)
		result, err := env.session.HandleTimeout(
			context.Background(),
			prepared.Nonce(),
			time.Now().Add(-2*time.Second),
			&host.InferencePayload{},
		)
		require.Error(t, err)
		require.Equal(t, "insufficient_votes", result.Outcome)
	})

	t.Run("verifier errors", func(t *testing.T) {
		clients := []user.HostClient{
			timeoutErrorClient{},
			timeoutErrorClient{},
			timeoutErrorClient{},
		}
		env := setupTestProxyWithClients(t, clients)
		prepared, err := env.session.PrepareInference(defaultParams())
		require.NoError(t, err)
		result, err := env.session.HandleTimeout(
			context.Background(),
			prepared.Nonce(),
			time.Now().Add(-2*time.Second),
			&host.InferencePayload{},
		)
		require.Error(t, err)
		require.Equal(t, "vote_collection_failed", result.Outcome)
	})

	t.Run("canceled wait", func(t *testing.T) {
		env := setupTestProxy(t, 3, nil, false)
		prepared, err := env.session.PrepareInference(defaultParams())
		require.NoError(t, err)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		result, err := env.session.HandleTimeout(
			ctx,
			prepared.Nonce(),
			time.Now(),
			&host.InferencePayload{},
		)
		require.Error(t, err)
		require.Equal(t, "skipped", result.Outcome)
		require.Equal(t, "context_canceled", result.DetailReason)
		require.Empty(t, result.Reason,
			"the deadline was never reached, so this must not count as an inference timeout")
	})
}

type timeoutErrorClient struct{}

func (timeoutErrorClient) Send(
	context.Context,
	host.HostRequest,
	io.Writer,
	func(*host.HostResponse),
) (*host.HostResponse, error) {
	return nil, errors.New("send not expected")
}

func (timeoutErrorClient) VerifyTimeout(
	context.Context,
	uint64,
	types.TimeoutReason,
	*host.InferencePayload,
	[]types.Diff,
	host.TimeoutArtifacts,
) (bool, []byte, uint32, []*types.DevshardTx, string, error) {
	return false, nil, 0, nil, "", errors.New("verifier unavailable")
}

func (timeoutErrorClient) VerifyErrorMiss(
	context.Context,
	uint64,
	[]types.Diff,
	host.TimeoutArtifacts,
) (bool, []byte, uint32, []*types.DevshardTx, string, error) {
	return false, nil, 0, nil, "", errors.New("verifier unavailable")
}

func TestAccountingObserverTracksCommittedSessionDiffs(t *testing.T) {
	env := setupTestProxy(t, 3, nil, true)
	tracker, err := accounting.OpenTracker(filepath.Join(t.TempDir(), "accounting.db"), 0, time.Hour)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tracker.Close()) })
	recorder := accounting.NewRecorder(tracker, nil)
	recorder.Attach(accounting.RuntimeMetadata{
		EscrowID:      "escrow-proxy",
		CreationEpoch: 21,
		Model:         "llama",
		TimeoutBuffer: user.TimeoutBuffer,
	}, env.session, env.sm)

	_, err = env.session.SendInference(context.Background(), defaultParams())
	require.NoError(t, err)
	recorder.RealSend("escrow-proxy", 1, time.Now(), "")
	recorder.Usage("escrow-proxy", 1, 1, "")
	_, err = env.session.PrepareInference(defaultParams())
	require.NoError(t, err)

	var finished uint64
	for _, record := range tracker.Query(accounting.QueryFilter{EpochIndex: 21}) {
		finished += record.Dispositions[accounting.DispositionFinishedUsed]
	}
	require.Equal(t, uint64(1), finished)
}

func TestAccountingObserverSyncsActiveProtocolMisses(t *testing.T) {
	env := setupTestProxy(t, 3, nil, true)
	tracker, err := accounting.OpenTracker(filepath.Join(t.TempDir(), "accounting.db"), 0, time.Hour)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tracker.Close()) })
	recorder := accounting.NewRecorder(tracker, nil)
	recorder.Attach(accounting.RuntimeMetadata{
		EscrowID:      "escrow-proxy",
		CreationEpoch: 22,
		Model:         "llama",
	}, env.session, env.sm)

	oldBuffer := user.TimeoutBuffer
	user.TimeoutBuffer = 0
	t.Cleanup(func() { user.TimeoutBuffer = oldBuffer })
	params := defaultParams()
	prepared, err := env.session.PrepareInference(params)
	require.NoError(t, err)
	sentAt := time.Now().Add(-2 * time.Second)
	recorder.RealSend("escrow-proxy", prepared.Nonce(), sentAt, "")

	// StartedAt is stamped when the request arrives, so it never postdates the send; backdating one
	// without the other would ask verifiers to judge a deadline that has not run yet.
	result, err := env.session.HandleTimeout(context.Background(), prepared.Nonce(), sentAt, &host.InferencePayload{
		Prompt:      params.Prompt,
		Model:       params.Model,
		InputLength: params.InputLength,
		MaxTokens:   params.MaxTokens,
		StartedAt:   sentAt.Unix(),
	})
	require.Error(t, err)
	require.True(t, result.Applied)
	recorder.TimeoutResult("escrow-proxy", prepared.Nonce(), result.Reason, "completed", "none", "", "")

	var applied, missed, crossCheckErrors uint64
	for _, record := range tracker.Query(accounting.QueryFilter{EpochIndex: 22}) {
		applied += record.TimeoutOutcomes[accounting.TimeoutApplied]
		missed += record.ProtocolMisses
		crossCheckErrors += record.CrossChecks.ErrorCount
	}
	require.Equal(t, uint64(1), applied)
	require.Equal(t, applied, missed)
	require.Zero(t, crossCheckErrors)
}

func TestAccountingProductionPendingClassification(t *testing.T) {
	env := setupTestProxy(t, 3, nil, true)
	tracker, err := accounting.OpenTracker(filepath.Join(t.TempDir(), "accounting.db"), 0, time.Hour)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tracker.Close()) })
	recorder := accounting.NewRecorder(tracker, nil)
	recorder.Attach(accounting.RuntimeMetadata{
		EscrowID:      "escrow-proxy",
		CreationEpoch: 23,
		Model:         "llama",
		TimeoutBuffer: user.TimeoutBuffer,
	}, env.session, env.sm)

	prepared, err := env.session.PrepareInference(defaultParams())
	require.NoError(t, err)
	record := accountingRecordForParticipant(t, tracker, 23, env.group[prepared.HostIdx()].ValidatorAddress)
	require.Equal(t, uint64(1), record.PendingClassification)
	require.Zero(t, record.InFlight)

	recorder.RealSend("escrow-proxy", prepared.Nonce(), time.Now(), "")
	record = accountingRecordForParticipant(t, tracker, 23, env.group[prepared.HostIdx()].ValidatorAddress)
	require.Zero(t, record.PendingClassification)
	require.Equal(t, uint64(1), record.InFlight)
}

func TestAccountingProductionGhostFact(t *testing.T) {
	env := setupTestProxy(t, 3, nil, true)
	env.proxy.redundancy.picker.stop()
	tracker, err := accounting.OpenTracker(filepath.Join(t.TempDir(), "accounting.db"), 0, time.Hour)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tracker.Close()) })
	recorder := accounting.NewRecorder(tracker, nil)
	recorder.Attach(accounting.RuntimeMetadata{
		EscrowID:      "escrow-proxy",
		CreationEpoch: 24,
		Model:         "llama",
		TimeoutBuffer: user.TimeoutBuffer,
	}, env.session, env.sm)
	env.proxy.redundancy.accounting = recorder
	env.proxy.redundancy.devshardID = "escrow-proxy"
	metrics := NewDevshardMetrics()
	env.proxy.redundancy.metrics = metrics

	prepared := prepareForGhost(t, env.session, "llama")
	env.proxy.redundancy.runGhostProbe(
		prepared,
		ghostThrottled,
		ghostThrottled.reason(),
	)

	record := accountingRecordForParticipant(t, tracker, 24, env.group[prepared.HostIdx()].ValidatorAddress)
	require.Equal(t, uint64(1), record.Dispositions[accounting.DispositionGhost])
	require.Equal(t, accounting.NoSendParticipantThrottled, record.Counters[0].Key.NoSendReason)

	families, err := metrics.registry.Gather()
	require.NoError(t, err)
	requireMetricCounterValue(t, families, "devshard_gateway_slot_decisions_total", map[string]string{
		"participant_key": env.session.HostParticipantKey(prepared.HostIdx()),
		"model":           "llama",
		"escrow_id":       "escrow-proxy",
		"decision":        "ghost_no_send",
		"reason":          "participant_throttled_no_send",
		"quarantine_mode": "none",
	}, 1)
}

func TestAccountingStateDivergenceRemainsUnknownPolicy(t *testing.T) {
	env := setupTestProxy(t, 3, nil, true)
	env.proxy.redundancy.picker.stop()
	tracker, err := accounting.OpenTracker(filepath.Join(t.TempDir(), "accounting.db"), 0, time.Hour)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tracker.Close()) })
	recorder := accounting.NewRecorder(tracker, nil)
	recorder.Attach(accounting.RuntimeMetadata{
		EscrowID:      "escrow-proxy",
		CreationEpoch: 27,
		Model:         "llama",
		TimeoutBuffer: user.TimeoutBuffer,
	}, env.session, env.sm)
	env.proxy.redundancy.accounting = recorder
	env.proxy.redundancy.devshardID = "escrow-proxy"

	prepared := prepareForGhost(t, env.session, "llama")
	env.proxy.redundancy.runGhostProbe(
		prepared,
		ghostStateDiverged,
		"escrow_state_root_diverged",
	)

	record := accountingRecordForParticipant(t, tracker, 27, env.group[prepared.HostIdx()].ValidatorAddress)
	require.Equal(t, uint64(1), record.Dispositions[accounting.DispositionGhost])
	require.Equal(t, accounting.NoSendUnknown, record.Counters[0].Key.NoSendReason)
	require.Equal(t, "escrow_state_root_diverged", record.Counters[0].Key.DetailReason)
	require.Equal(t, uint64(1), record.UnknownReasonTotal)
}

// The request identifier only exists in the gateway's log context. Unless the dispatch path hands it
// to accounting, a later miss can name its nonce but never the request that produced it.
func TestAccountingCarriesTheRequestIDFromTheDispatchContext(t *testing.T) {
	env := setupTestProxy(t, 3, nil, true)
	tracker, err := accounting.OpenTracker(filepath.Join(t.TempDir(), "accounting.db"), 0, time.Hour)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tracker.Close()) })
	recorder := accounting.NewRecorder(tracker, nil)
	recorder.Attach(accounting.RuntimeMetadata{
		EscrowID:      "escrow-proxy",
		CreationEpoch: 25,
		Model:         "llama",
		TimeoutBuffer: user.TimeoutBuffer,
	}, env.session, env.sm)
	env.proxy.redundancy.accounting = recorder

	params := defaultParams()
	prepared, err := env.session.PrepareInference(params)
	require.NoError(t, err)
	attempt := &inflight{
		escrowID: "escrow-proxy",
		nonce:    prepared.Nonce(),
		hostIdx:  prepared.HostIdx(),
		sendTime: time.Now(),
	}
	ctx, requestID := ensureRequestLogContext(context.Background())

	env.proxy.redundancy.recordGatewayAttemptStarted(ctx, attempt, params)
	require.NoError(t, tracker.RecordProtocol("escrow-proxy", attempt.nonce,
		uint32(attempt.hostIdx), accounting.ProtocolTimeoutApplied, types.HostStats{}))

	events := tracker.Events(accounting.QueryFilter{EpochIndex: 25})
	require.Len(t, events, 1)
	require.Equal(t, attempt.nonce, events[0].Nonce)
	require.Equal(t, requestID, events[0].RequestID)
}

func TestAccountingProductionUsedAndUnusedAttempts(t *testing.T) {
	env := setupTestProxy(t, 3, nil, true)
	tracker, err := accounting.OpenTracker(filepath.Join(t.TempDir(), "accounting.db"), 0, time.Hour)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tracker.Close()) })
	recorder := accounting.NewRecorder(tracker, nil)
	recorder.Attach(accounting.RuntimeMetadata{
		EscrowID:      "escrow-proxy",
		CreationEpoch: 25,
		Model:         "llama",
		TimeoutBuffer: user.TimeoutBuffer,
	}, env.session, env.sm)
	env.proxy.redundancy.accounting = recorder
	params := defaultParams()

	prepared1, err := env.session.PrepareInference(params)
	require.NoError(t, err)
	attempt1 := &inflight{
		escrowID: "escrow-proxy",
		nonce:    prepared1.Nonce(),
		hostIdx:  prepared1.HostIdx(),
		sendTime: time.Now(),
	}
	env.proxy.redundancy.recordGatewayAttemptStarted(context.Background(), attempt1, params)
	response1, err := env.session.SendOnly(context.Background(), prepared1, nil, nil)
	require.NoError(t, err)
	require.NoError(t, env.session.ProcessResponse(prepared1.HostIdx(), response1, prepared1.Nonce()))

	prepared2, err := env.session.PrepareInference(params)
	require.NoError(t, err)
	attempt2 := &inflight{
		escrowID: "escrow-proxy",
		nonce:    prepared2.Nonce(),
		hostIdx:  prepared2.HostIdx(),
		sendTime: time.Now(),
	}
	env.proxy.redundancy.recordGatewayAttemptStarted(context.Background(), attempt2, params)
	response2, err := env.session.SendOnly(context.Background(), prepared2, nil, nil)
	require.NoError(t, err)
	require.NoError(t, env.session.ProcessResponse(prepared2.HostIdx(), response2, prepared2.Nonce()))
	_, err = env.session.PrepareInference(params)
	require.NoError(t, err)

	env.proxy.redundancy.recordGatewayAttemptTerminal(attempt1, params, prepared1.Nonce(), true, nil)
	env.proxy.redundancy.recordGatewayAttemptTerminal(attempt2, params, prepared1.Nonce(), true, nil)

	var used, unused uint64
	for _, record := range tracker.Query(accounting.QueryFilter{EpochIndex: 25}) {
		used += record.Dispositions[accounting.DispositionFinishedUsed]
		unused += record.Dispositions[accounting.DispositionFinishedUnused]
	}
	require.Equal(t, uint64(1), used)
	require.Equal(t, uint64(1), unused)
}

func TestAccountingObserverSyncsActiveInvalidation(t *testing.T) {
	hostSigners := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	userSigner := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hostSigners)
	config := testutil.DefaultConfig(len(group))
	config.VoteThreshold = 1
	sm := statetest.MustStateMachine(
		t,
		"escrow-invalid",
		config,
		group,
		1_000_000,
		userSigner.Address(),
		signing.NewSecp256k1Verifier(),
	)
	tracker, err := accounting.OpenTracker(filepath.Join(t.TempDir(), "accounting.db"), 0, time.Hour)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tracker.Close()) })
	recorder := accounting.NewRecorder(tracker, nil)
	observer := &accountingObserverTarget{}
	recorder.Attach(accounting.RuntimeMetadata{
		EscrowID:      "escrow-invalid",
		CreationEpoch: 26,
		Model:         "llama",
	}, observer, sm)
	apply := func(nonce uint64, txs ...*types.DevshardTx) {
		t.Helper()
		_, applied, err := sm.ApplyLocalBestEffort(nonce, txs)
		require.NoError(t, err)
		require.Len(t, applied, len(txs))
		observer.observer(types.Diff{Nonce: nonce, Txs: applied})
	}

	start := &types.MsgStartInference{
		InferenceId: 1,
		PromptHash:  []byte("prompt"),
		Model:       "llama",
		InputLength: 100,
		MaxTokens:   testutil.TestMaxTokens,
		StartedAt:   1000,
	}
	apply(1, &types.DevshardTx{Tx: &types.DevshardTx_StartInference{StartInference: start}})
	executorSlot := uint32(1)
	confirm := &types.MsgConfirmStart{
		InferenceId: 1,
		ExecutorSig: testutil.SignExecutorReceipt(
			t,
			hostSigners[executorSlot],
			"escrow-invalid",
			1,
			start.PromptHash,
			start.Model,
			start.InputLength,
			start.MaxTokens,
			start.StartedAt,
			1000,
		),
		ConfirmedAt: 1000,
	}
	apply(2, &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: confirm}})
	finish := &types.MsgFinishInference{
		InferenceId:  1,
		ResponseHash: []byte("response"),
		InputTokens:  80,
		OutputTokens: 40,
		ExecutorSlot: executorSlot,
		EscrowId:     "escrow-invalid",
	}
	finish.ProposerSig = testutil.SignProposerTx(t, hostSigners[executorSlot], finish)
	apply(3, &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: finish}})
	validation := &types.MsgValidation{
		InferenceId:   1,
		ValidatorSlot: 0,
		Valid:         false,
		EscrowId:      "escrow-invalid",
	}
	validation.ProposerSig = testutil.SignProposerTx(t, hostSigners[0], validation)
	apply(4, &types.DevshardTx{Tx: &types.DevshardTx_Validation{Validation: validation}})
	vote := &types.MsgValidationVote{
		InferenceId: 1,
		VoterSlot:   2,
		VoteValid:   false,
		EscrowId:    "escrow-invalid",
	}
	vote.ProposerSig = testutil.SignProposerTx(t, hostSigners[2], vote)
	apply(5, &types.DevshardTx{Tx: &types.DevshardTx_ValidationVote{ValidationVote: vote}})

	record := accountingRecordForParticipant(t, tracker, 26, group[executorSlot].ValidatorAddress)
	require.Equal(t, uint64(1), record.ProtocolInvalid)
	require.Equal(t, record.ProtocolInvalid, record.CrossChecks.RecordedInvalid)
	require.Zero(t, record.CrossChecks.ErrorCount)
}

type accountingObserverTarget struct {
	observer func(types.Diff)
}

func (t *accountingObserverTarget) SetDiffObserver(observer func(types.Diff)) {
	t.observer = observer
}

func accountingRecordForParticipant(
	t *testing.T,
	tracker *accounting.Tracker,
	epoch uint64,
	participant string,
) accounting.ParticipantRecord {
	t.Helper()
	for _, record := range tracker.Query(accounting.QueryFilter{EpochIndex: epoch}) {
		if record.Participant == participant {
			return record
		}
	}
	t.Fatalf("missing participant %s", participant)
	return accounting.ParticipantRecord{}
}

func TestGatewayAccountingAdapterRecordsGhostPolicyDimensions(t *testing.T) {
	resetPoCPhaseStateForTest(t)
	tracker := newGatewayAccountingTestTracker(t)
	recorder := accounting.NewRecorder(tracker, nil)
	registerGatewayAccountingTestEscrow(t, tracker, "e1", 2, "m")

	cases := []struct {
		name       string
		reason     string
		quarantine string
		wantReason accounting.NoSendReason
		wantMode   accounting.QuarantineMode
	}{
		{
			name:       "probe quarantine",
			reason:     "participant_throttled_no_send",
			quarantine: "probe",
			wantReason: accounting.NoSendParticipantThrottled,
			wantMode:   accounting.QuarantineProbe,
		},
		{
			name:       "plain throttling",
			reason:     "participant_throttled_no_send",
			quarantine: "none",
			wantReason: accounting.NoSendParticipantThrottled,
			wantMode:   accounting.QuarantineNone,
		},
		{
			name:       "state divergence",
			reason:     "participant_state_diverged_no_send",
			quarantine: "none",
			wantReason: accounting.NoSendParticipantStateDiverged,
			wantMode:   accounting.QuarantineNone,
		},
		{
			name:       "state divergence, the spelling written before the rename",
			reason:     "participant_capability_no_send",
			quarantine: "none",
			wantReason: accounting.NoSendParticipantCapability,
			wantMode:   accounting.QuarantineNone,
		},
		{
			name:       "stale incompatible request",
			reason:     "no_compatible_request_after_stale",
			quarantine: "none",
			wantReason: accounting.NoSendNoCompatibleAfterStale,
			wantMode:   accounting.QuarantineNone,
		},
		{
			name:       "unlisted policy",
			reason:     "some_new_policy",
			quarantine: "none",
			wantReason: accounting.NoSendUnknown,
			wantMode:   accounting.QuarantineNone,
		},
	}

	for i, tc := range cases {
		nonce := uint64(i + 1)
		require.NoError(t, tracker.RecordDiff("e1", nonce, true), tc.name)
		recorder.Ghost("e1", nonce, tc.reason, tc.quarantine, false)
	}

	record := onlyGatewayAccountingRecord(t, tracker.Query(accounting.QueryFilter{EpochIndex: 2}))
	for _, tc := range cases {
		counter := requireGatewayAccountingCounter(t, record, func(key accounting.CounterKey) bool {
			return key.Disposition == accounting.DispositionGhost &&
				key.NoSendReason == tc.wantReason &&
				key.QuarantineMode == tc.wantMode
		})
		require.Equal(t, uint64(1), counter.Count, tc.name)
		if tc.wantReason == accounting.NoSendUnknown {
			require.Equal(t, "unknown", counter.Key.DetailReason)
		}
	}
	require.Equal(t, uint64(1), record.UnknownReasonTotal)
}

func TestGatewayAccountingAdapterRecordsPoCGhostPolicyDimensions(t *testing.T) {
	resetPoCPhaseStateForTest(t)
	setPoCPhaseState(true, "poc")
	tracker := newGatewayAccountingTestTracker(t)
	recorder := accounting.NewRecorder(tracker, currentPoCPhaseReason)
	registerGatewayAccountingTestEscrow(t, tracker, "e1", 3, "m")
	require.NoError(t, tracker.RecordDiff("e1", 1, true))

	recorder.Ghost("e1", 1, "poc_unavailable_host", "none", false)

	record := onlyGatewayAccountingRecord(t, tracker.Query(accounting.QueryFilter{EpochIndex: 3}))
	counter := requireGatewayAccountingCounter(t, record, func(key accounting.CounterKey) bool {
		return key.Disposition == accounting.DispositionGhost &&
			key.DispatchPhase == accounting.PhasePoC &&
			key.NoSendReason == accounting.NoSendPoCUnavailable &&
			key.QuarantineMode == accounting.QuarantineNone
	})
	require.Equal(t, uint64(1), counter.Count)
}

func TestGatewayAccountingAdapterRecordsRealSendPolicyDimensions(t *testing.T) {
	resetPoCPhaseStateForTest(t)
	tracker := newGatewayAccountingTestTracker(t)
	recorder := accounting.NewRecorder(tracker, nil)
	registerGatewayAccountingTestEscrow(t, tracker, "e1", 4, "m")

	require.NoError(t, tracker.RecordDiff("e1", 1, true))
	recorder.RealSend("e1", 1, time.Now(), "shadow")
	recorder.Usage("e1", 1, 1, "")
	require.NoError(t, tracker.RecordProtocol("e1", 1, 0, accounting.ProtocolFinishApplied, types.HostStats{}))

	require.NoError(t, tracker.RecordDiff("e1", 2, true))
	recorder.RealSend("e1", 2, time.Now(), "probation")
	recorder.Usage("e1", 2, 1, "")
	require.NoError(t, tracker.RecordProtocol("e1", 2, 0, accounting.ProtocolFinishApplied, types.HostStats{}))

	record := onlyGatewayAccountingRecord(t, tracker.Query(accounting.QueryFilter{EpochIndex: 4}))
	shadow := requireGatewayAccountingCounter(t, record, func(key accounting.CounterKey) bool {
		return key.Disposition == accounting.DispositionFinishedUsed &&
			key.QuarantineMode == accounting.QuarantineShadow
	})
	require.Equal(t, uint64(1), shadow.Count)
	probation := requireGatewayAccountingCounter(t, record, func(key accounting.CounterKey) bool {
		return key.Disposition == accounting.DispositionFinishedUnused &&
			key.QuarantineMode == accounting.QuarantineProbation
	})
	require.Equal(t, uint64(1), probation.Count)
}

func TestGatewayAccountingAdapterRecordsPoCRealSendPhaseContext(t *testing.T) {
	resetPoCPhaseStateForTest(t)
	setPoCPhaseState(true, "poc")
	tracker := newGatewayAccountingTestTracker(t)
	recorder := accounting.NewRecorder(tracker, currentPoCPhaseReason)
	registerGatewayAccountingTestEscrow(t, tracker, "e1", 5, "m")
	require.NoError(t, tracker.RecordDiff("e1", 1, true))

	recorder.RealSend("e1", 1, time.Now(), "none")
	recorder.Usage("e1", 1, 1, "")
	require.NoError(t, tracker.RecordProtocol("e1", 1, 0, accounting.ProtocolFinishApplied, types.HostStats{}))

	record := onlyGatewayAccountingRecord(t, tracker.Query(accounting.QueryFilter{EpochIndex: 5}))
	counter := requireGatewayAccountingCounter(t, record, func(key accounting.CounterKey) bool {
		return key.Disposition == accounting.DispositionFinishedUsed &&
			key.DispatchPhase == accounting.PhasePoC &&
			key.TimeoutEvaluationPhase == "" &&
			key.QuarantineMode == accounting.QuarantineNone
	})
	require.Equal(t, uint64(1), counter.Count)
}

func TestGatewayAccountingNoNonceConsumedHasNoAccounting(t *testing.T) {
	resetPoCPhaseStateForTest(t)
	setPoCPhaseState(true, "poc")
	tracker := newGatewayAccountingTestTracker(t)
	registerGatewayAccountingTestEscrow(t, tracker, "e1", 6, "m")

	record := onlyGatewayAccountingRecord(t, tracker.Query(accounting.QueryFilter{EpochIndex: 6}))
	require.Zero(t, record.AssignedNonces)
	require.Empty(t, record.Dispositions)
	require.Empty(t, record.Counters)
	require.Zero(t, record.InFlight)
	require.Zero(t, record.PendingClassification)
	require.Zero(t, record.Unclassified)
	require.Zero(t, record.Overclassified)
}

func newGatewayAccountingTestTracker(t *testing.T) *accounting.Tracker {
	t.Helper()
	tracker, err := accounting.OpenTracker(filepath.Join(t.TempDir(), "accounting.db"), 0, time.Hour)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tracker.Close()) })
	return tracker
}

func registerGatewayAccountingTestEscrow(t *testing.T, tracker *accounting.Tracker, id string, epoch uint64, model string) {
	t.Helper()
	require.NoError(t, tracker.RegisterEscrow(accounting.EscrowMetadata{
		EscrowID:      id,
		CreationEpoch: epoch,
		Model:         model,
		Phase:         accounting.EscrowActive,
		Slots: []types.SlotAssignment{
			{SlotID: 0, ValidatorAddress: "p0"},
		},
	}))
}

func onlyGatewayAccountingRecord(t *testing.T, records []accounting.ParticipantRecord) accounting.ParticipantRecord {
	t.Helper()
	require.Len(t, records, 1)
	return records[0]
}

func requireGatewayAccountingCounter(
	t *testing.T,
	record accounting.ParticipantRecord,
	matches func(accounting.CounterKey) bool,
) accounting.CounterRecord {
	t.Helper()
	for _, counter := range record.Counters {
		if matches(counter.Key) {
			return counter
		}
	}
	t.Fatalf("missing counter in %#v", record.Counters)
	return accounting.CounterRecord{}
}

// The chain counts a miss for an applied timeout whoever raised it, so a burn's timeout has to reach
// the ledger the same as any other. Asserted end to end through the recorder: a burn that declares a
// timeout stays live to receive it, and the applied outcome lands in the population the cross-check
// compares against the chain's own count.
func TestATimeoutRaisedOnABurnReachesTheLedger(t *testing.T) {
	tracker := newGatewayAccountingTestTracker(t)
	recorder := accounting.NewRecorder(tracker, nil)
	registerGatewayAccountingTestEscrow(t, tracker, "e1", 1, "m")

	require.NoError(t, tracker.RecordDiff("e1", 1, true))
	recorder.Ghost("e1", 1, "poc_unavailable_host", "none", true)
	recorder.TimeoutResult("e1", 1, "refused", "completed", "none", "", "")

	var applied uint64
	for _, record := range tracker.Query(accounting.QueryFilter{EpochIndex: 1}) {
		applied += record.TimeoutOutcomes[accounting.TimeoutApplied]
	}
	require.Equal(t, uint64(1), applied,
		"an applied timeout on a burned nonce must be counted, or the cross-check can never balance")
}
