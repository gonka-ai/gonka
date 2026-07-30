package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"devshard/accounting"
	"devshard/user"

	"github.com/stretchr/testify/require"
)

const lifecycleEpoch = 77

// shrinkTimeoutBuffer keeps protocol timeout waits short enough for a test.
func shrinkTimeoutBuffer(t *testing.T) {
	t.Helper()
	saved := user.TimeoutBuffer
	user.TimeoutBuffer = 200 * time.Millisecond
	t.Cleanup(func() { user.TimeoutBuffer = saved })
}

func lifecycleEpochFunc(context.Context) (uint64, error) { return lifecycleEpoch, nil }

// TestGatewayAccountingLifecycleWithDeadHosts drives a real request through
// Redundancy against two dead hosts, so the winner finishes on the third nonce
// while the first two require protocol timeouts. It then checks the accounting
// book against devshard state, the API, and a store round trip.
func TestGatewayAccountingLifecycleWithDeadHosts(t *testing.T) {
	zeroReceiptTimeout(t)
	shrinkTimeoutBuffer(t)
	env := setupTestProxy(t, 4, nil, true)

	dbPath := filepath.Join(t.TempDir(), "accounting.db")
	service, err := accounting.OpenService(dbPath, 0, time.Hour)
	require.NoError(t, err)
	recorder := newGatewayAccountingRecorder(service)
	require.NotNil(t, recorder)
	t.Cleanup(func() { _ = recorder.close() })

	// buildRuntime does this in gateway mode; the bare harness does not.
	env.proxy.redundancy.devshardID = "escrow-proxy"
	env.session.SetEpochID(lifecycleEpoch)
	recorder.attachRuntime(&devshardRuntime{
		id:      "escrow-proxy",
		model:   "llama",
		proxy:   env.proxy,
		session: env.session,
	})

	// nonce 1 -> host 1, nonce 2 -> host 2 (both dead), nonce 3 -> host 3.
	env.killables[1].Kill()
	env.killables[2].Kill()

	var buf bytes.Buffer
	require.NoError(t, env.proxy.redundancy.RunInference(context.Background(), defaultParams(), &buf, nil))

	// Timeouts for the dead hosts are submitted after the user response, so
	// wait for the protocol to record both misses.
	require.Eventually(t, func() bool {
		return protocolMissedTotal(env) == 2
	}, 20*time.Second, 50*time.Millisecond, "both dead-host nonces must be timed out")

	records := waitForAppliedTimeouts(t, service.Book, 2)
	assertLifecycleAccounting(t, env, records)

	// The private API must serve the same numbers.
	handler := accounting.NewHandler(service.Book, lifecycleEpochFunc)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/epochs/current/participants", nil)
	recorderHTTP := httptest.NewRecorder()
	handler.ServeHTTP(recorderHTTP, request)
	require.Equal(t, http.StatusOK, recorderHTTP.Code)
	var payload struct {
		SchemaVersion int                            `json:"schema_version"`
		EpochIndex    uint64                         `json:"epoch_index"`
		Participants  []accounting.ParticipantRecord `json:"participants"`
	}
	require.NoError(t, json.Unmarshal(recorderHTTP.Body.Bytes(), &payload))
	require.Equal(t, accounting.SchemaVersion, payload.SchemaVersion)
	require.EqualValues(t, lifecycleEpoch, payload.EpochIndex)
	assertLifecycleAccounting(t, env, payload.Participants)

	// A snapshot plus reopen must preserve every counter.
	require.NoError(t, service.Flush(context.Background()))
	reopened, err := accounting.OpenService(dbPath, 0, time.Hour)
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close() })
	assertLifecycleAccounting(t, env, reopened.Book.Query(accounting.QueryFilter{EpochIndex: lifecycleEpoch}))
}

// TestGatewayAccountingRecordsGhostDispatch burns a nonce through the real
// ghost dispatcher the picker uses and checks the recorded policy context.
func TestGatewayAccountingRecordsGhostDispatch(t *testing.T) {
	env := setupTestProxy(t, 3, nil, true)
	env.proxy.redundancy.picker.stop()
	env.proxy.redundancy.devshardID = "escrow-proxy"

	service, err := accounting.OpenService(filepath.Join(t.TempDir(), "accounting.db"), 0, time.Hour)
	require.NoError(t, err)
	recorder := newGatewayAccountingRecorder(service)
	require.NotNil(t, recorder)
	t.Cleanup(func() { _ = recorder.close() })

	env.session.SetEpochID(lifecycleEpoch)
	recorder.attachRuntime(&devshardRuntime{
		id:      "escrow-proxy",
		model:   "llama",
		proxy:   env.proxy,
		session: env.session,
	})

	prepared := prepareForGhost(t, env.session, "llama")
	env.proxy.redundancy.runGhostProbe(prepared, ghostPoC, ghostPoC.reason())

	records := service.Book.Query(accounting.QueryFilter{EpochIndex: lifecycleEpoch})
	var ghosts []accounting.CounterRecord
	for _, record := range records {
		require.Zero(t, record.Unclassified, "the burned nonce must be classified")
		for _, counter := range record.Counters {
			if counter.Disposition == accounting.DispositionGhost {
				ghosts = append(ghosts, counter)
			}
		}
	}
	require.Len(t, ghosts, 1)
	require.EqualValues(t, 1, ghosts[0].Count)
	require.Equal(t, accounting.NoSendPoCUnavailable, ghosts[0].NoSendReason)
	require.Empty(t, ghosts[0].DetailReason,
		"a recognized no_send_reason must not be duplicated into detail_reason")
	require.Equal(t, accounting.QuarantineNone, ghosts[0].QuarantineMode)
	require.Equal(t, accounting.PhaseNormal, ghosts[0].DispatchPhase)
}

func protocolMissedTotal(env *testProxyEnv) uint64 {
	var total uint64
	for _, stats := range env.sm.SnapshotStateNoInferences().HostStats {
		if stats != nil {
			total += uint64(stats.Missed)
		}
	}
	return total
}

// waitForAppliedTimeouts waits for the gateway side of the timeout to land in
// the book: HandleTimeout records the outcome after the protocol diff applies.
func waitForAppliedTimeouts(t *testing.T, book *accounting.Book, want uint64) []accounting.ParticipantRecord {
	t.Helper()
	var records []accounting.ParticipantRecord
	require.Eventually(t, func() bool {
		records = book.Query(accounting.QueryFilter{EpochIndex: lifecycleEpoch})
		var applied uint64
		for _, record := range records {
			applied += record.TimeoutOutcomes[accounting.TimeoutApplied]
		}
		return applied == want
	}, 20*time.Second, 50*time.Millisecond, "gateway must record both applied timeouts")
	return records
}

func assertLifecycleAccounting(t *testing.T, env *testProxyEnv, records []accounting.ParticipantRecord) {
	t.Helper()
	require.NotEmpty(t, records)

	var (
		assigned      uint64
		dispositions  = make(map[accounting.Disposition]uint64)
		outcomes      = make(map[accounting.TimeoutOutcome]uint64)
		misses        uint64
		inFlight      uint64
		unclassified  uint64
		crossCheckSum uint64
	)
	for _, record := range records {
		require.Equal(t, "llama", record.Model)
		assigned += record.AssignedNonces
		misses += record.ProtocolMisses
		inFlight += record.InFlight
		unclassified += record.Unclassified
		crossCheckSum += record.CrossChecks.ErrorCount
		for disposition, count := range record.Dispositions {
			dispositions[disposition] += count
		}
		for outcome, count := range record.TimeoutOutcomes {
			outcomes[outcome] += count
		}
	}

	require.EqualValues(t, env.session.Nonce(), assigned,
		"assigned nonces must cover every consumed nonce")
	require.Equal(t, uint64(1), dispositions[accounting.DispositionFinishedUsed],
		"the winner is the only used finish")
	require.Equal(t, uint64(2), dispositions[accounting.DispositionUnfinishedRefused],
		"both dead hosts refused their nonce")
	require.Equal(t, uint64(2), outcomes[accounting.TimeoutApplied])
	require.Len(t, outcomes, 1, "no timeout may end in a non-applied outcome here")
	require.Equal(t, uint64(2), misses)
	require.Equal(t, protocolMissedTotal(env), misses,
		"protocol misses must match HostStats.Missed")
	require.Zero(t, crossCheckSum, "applied timeouts must match HostStats.Missed per slot")
	require.Zero(t, inFlight, "no attempt may stay in flight after the request settles")
	require.Zero(t, unclassified, "every consumed nonce must carry a disposition")

	var classified uint64
	for _, count := range dispositions {
		classified += count
	}
	require.Equal(t, assigned, classified+inFlight+unclassified,
		"dispositions must partition the consumed nonces")
}
