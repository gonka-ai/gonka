package accounting

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

func missOn(t *testing.T, tracker *Tracker, escrowID string, nonce uint64) {
	t.Helper()
	require.NoError(t, tracker.RecordProtocol(escrowID, nonce, AssignedNonceSlot(nonce, 2), ProtocolTimeoutApplied, types.HostStats{}))
}

// A count says a participant took misses. It cannot say which request produced them, which is what
// makes one reproducible.
func TestEvents_NameTheNonceAndTheRequestBehindAMiss(t *testing.T) {
	tracker := newTestTracker(t)
	registerEscrow(t, tracker, "e1", 9, "m1")
	require.NoError(t, tracker.RecordDiff("e1", 1, true))
	require.NoError(t, tracker.RecordRequestID("e1", 1, "req-42"))

	missOn(t, tracker, "e1", 1)

	events := tracker.Events(QueryFilter{EpochIndex: 9})
	require.Len(t, events, 1)
	require.Equal(t, "e1", events[0].EscrowID)
	require.Equal(t, uint64(1), events[0].Nonce)
	require.Equal(t, "req-42", events[0].RequestID)
	require.Equal(t, ProtocolTimeoutApplied, events[0].Kind)
	require.Equal(t, "p1", events[0].Participant, "slot 1 of a two-slot group")
}

// An invalid costs a participant just as a miss does, so it is on the same feed.
func TestEvents_CoverInvalidsToo(t *testing.T) {
	tracker := newTestTracker(t)
	registerEscrow(t, tracker, "e1", 9, "m1")
	require.NoError(t, tracker.RecordDiff("e1", 1, true))

	require.NoError(t, tracker.RecordProtocol("e1", 1, 1, ProtocolInvalidated, types.HostStats{}))

	events := tracker.Events(QueryFilter{EpochIndex: 9})
	require.Len(t, events, 1)
	require.Equal(t, ProtocolInvalidated, events[0].Kind)
}

// Receipts and finishes are the normal path; on the feed they would bury the two verdicts that cost.
func TestEvents_IgnoreTheOrdinaryProtocolPath(t *testing.T) {
	tracker := newTestTracker(t)
	registerEscrow(t, tracker, "e1", 9, "m1")
	require.NoError(t, tracker.RecordDiff("e1", 1, true))

	require.NoError(t, tracker.RecordProtocol("e1", 1, 1, ProtocolReceiptApplied, types.HostStats{}))
	require.NoError(t, tracker.RecordProtocol("e1", 1, 1, ProtocolFinishApplied, types.HostStats{}))

	require.Empty(t, tracker.Events(QueryFilter{EpochIndex: 9}))
}

// An unbounded feed would bring back the growth the rollup just removed.
func TestEvents_RingKeepsTheNewestAndStaysBounded(t *testing.T) {
	tracker := newTestTracker(t)
	registerEscrow(t, tracker, "e1", 9, "m1")
	total := uint64(maxProtocolEventsPerEscrow + 20)
	for nonce := uint64(1); nonce <= total; nonce++ {
		require.NoError(t, tracker.RecordDiff("e1", nonce, true))
		missOn(t, tracker, "e1", nonce)
	}

	events := tracker.Events(QueryFilter{EpochIndex: 9})

	require.Len(t, events, maxProtocolEventsPerEscrow)
	nonces := make(map[uint64]bool, len(events))
	for _, event := range events {
		nonces[event.Nonce] = true
	}
	require.True(t, nonces[total], "the newest miss must survive")
	require.False(t, nonces[1], "the oldest miss must be dropped")
}

func TestEvents_FilterByParticipant(t *testing.T) {
	tracker := newTestTracker(t)
	registerEscrow(t, tracker, "e1", 9, "m1")
	require.NoError(t, tracker.RecordDiff("e1", 1, true))
	require.NoError(t, tracker.RecordDiff("e1", 2, true))
	missOn(t, tracker, "e1", 1)
	missOn(t, tracker, "e1", 2)

	events := tracker.Events(QueryFilter{EpochIndex: 9, Participant: "p0"})

	require.Len(t, events, 1)
	require.Equal(t, "p0", events[0].Participant)
}

func TestEvents_ReachTheEndpoint(t *testing.T) {
	tracker := newTestTracker(t)
	registerEscrow(t, tracker, "e1", 9, "m1")
	require.NoError(t, tracker.RecordDiff("e1", 1, true))
	require.NoError(t, tracker.RecordRequestID("e1", 1, "req-7"))
	missOn(t, tracker, "e1", 1)

	handler := NewHandler(tracker, func(context.Context) (uint64, error) { return 9, nil }, nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/epochs/current/events", nil))
	require.Equal(t, http.StatusOK, recorder.Code)

	var body struct {
		Events []ProtocolEventRecord `json:"events"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	require.Len(t, body.Events, 1)
	require.Equal(t, "req-7", body.Events[0].RequestID)
}

// The gateway never calls RecordProtocol; a committed diff is what tells accounting a timeout was
// applied. Wiring the feed anywhere else leaves it empty in production while the unit tests pass.
func TestEvents_ReachTheFeedThroughACommittedDiff(t *testing.T) {
	tracker := newTestTracker(t)
	registerEscrow(t, tracker, "e1", 33, "m")
	require.NoError(t, tracker.RecordDiff("e1", 1, true))
	require.NoError(t, tracker.RecordRequestID("e1", 1, "req-committed"))
	state := &fakeProtocolView{
		phase:      types.PhaseActive,
		inferences: map[uint64]types.InferenceRecord{1: {ExecutorSlot: 1, Status: types.StatusTimedOut}},
		hostStats:  map[uint32]types.HostStats{1: {Missed: 1}},
	}
	recorder := NewRecorder(tracker, nil)

	recorder.committedDiff("e1", types.Diff{Nonce: 2, Txs: []*types.DevshardTx{{
		Tx: &types.DevshardTx_TimeoutInference{TimeoutInference: &types.MsgTimeoutInference{InferenceId: 1}},
	}}}, state)

	events := tracker.Events(QueryFilter{EpochIndex: 33})
	require.Len(t, events, 1)
	require.Equal(t, ProtocolTimeoutApplied, events[0].Kind)
	require.Equal(t, uint64(1), events[0].Nonce)
	require.Equal(t, uint32(1), events[0].SlotID)
	require.Equal(t, "p1", events[0].Participant)
	require.Equal(t, "req-committed", events[0].RequestID)
}

// A start inference is the ordinary path and carries no verdict against anyone.
func TestEvents_ACommittedStartLeavesNoEvent(t *testing.T) {
	tracker := newTestTracker(t)
	registerEscrow(t, tracker, "e1", 33, "m")
	state := &fakeProtocolView{
		phase:      types.PhaseActive,
		inferences: map[uint64]types.InferenceRecord{1: {ExecutorSlot: 1, Status: types.StatusStarted}},
	}
	recorder := NewRecorder(tracker, nil)

	recorder.committedDiff("e1", types.Diff{Nonce: 1, Txs: []*types.DevshardTx{{
		Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{InferenceId: 1}},
	}}}, state)

	require.Empty(t, tracker.Events(QueryFilter{EpochIndex: 33}))
}

// Scoping by path mirrors participants/{participant}, so a reader chasing one host does not have to
// filter the whole epoch's feed itself.
func TestEvents_ScopeToOneParticipantByPath(t *testing.T) {
	tracker := newTestTracker(t)
	registerEscrow(t, tracker, "e1", 9, "m1")
	require.NoError(t, tracker.RecordDiff("e1", 1, true))
	require.NoError(t, tracker.RecordDiff("e1", 2, true))
	missOn(t, tracker, "e1", 1)
	missOn(t, tracker, "e1", 2)

	handler := NewHandler(tracker, func(context.Context) (uint64, error) { return 9, nil }, nil)

	body := struct {
		Participant string                `json:"participant"`
		Events      []ProtocolEventRecord `json:"events"`
	}{}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/epochs/current/events/p0", nil))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))

	require.Equal(t, "p0", body.Participant, "the response names what it was scoped to")
	require.Len(t, body.Events, 1)
	require.Equal(t, "p0", body.Events[0].Participant)
}

// Nothing to report is the healthy answer for a host, not a missing resource.
func TestEvents_AParticipantWithNoMissesGetsAnEmptyFeed(t *testing.T) {
	tracker := newTestTracker(t)
	registerEscrow(t, tracker, "e1", 9, "m1")
	require.NoError(t, tracker.RecordDiff("e1", 1, true))
	missOn(t, tracker, "e1", 1)

	handler := NewHandler(tracker, func(context.Context) (uint64, error) { return 9, nil }, nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/epochs/current/events/p0", nil))

	require.Equal(t, http.StatusOK, recorder.Code)
	var body struct {
		Events []ProtocolEventRecord `json:"events"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	require.Empty(t, body.Events)
}

// A burned nonce has no client request behind it, and pretending otherwise would be a lie.
func TestEvents_TolerateAMissWithNoRequest(t *testing.T) {
	tracker := newTestTracker(t)
	registerEscrow(t, tracker, "e1", 9, "m1")
	require.NoError(t, tracker.RecordDiff("e1", 1, true))

	missOn(t, tracker, "e1", 1)

	events := tracker.Events(QueryFilter{EpochIndex: 9})
	require.Len(t, events, 1)
	require.Empty(t, events[0].RequestID)
}

// The feed answers "which request took this miss" long after the miss happened, so it has to survive
// a gateway restart. escrowState is never marshalled directly — the store copies field by field —
// so a json tag on it persists nothing.
func TestEvents_SurviveARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounting.db")
	before, err := OpenTracker(path, 0, 0)
	require.NoError(t, err)
	before.now = func() time.Time { return accountingTestNow }
	registerEscrow(t, before, "e1", 9, "m1")
	require.NoError(t, before.RecordDiff("e1", 1, true))
	require.NoError(t, before.RecordRequestID("e1", 1, "req-persisted"))
	missOn(t, before, "e1", 1)
	require.Len(t, before.Events(QueryFilter{EpochIndex: 9}), 1, "precondition: recorded before the restart")
	require.NoError(t, before.Flush(context.Background()))
	require.NoError(t, before.Close())

	after, err := OpenTracker(path, 0, 0)
	require.NoError(t, err)
	after.now = func() time.Time { return accountingTestNow }
	t.Cleanup(func() { require.NoError(t, after.Close()) })

	events := after.Events(QueryFilter{EpochIndex: 9})
	require.Len(t, events, 1)
	require.Equal(t, uint64(1), events[0].Nonce)
	require.Equal(t, "req-persisted", events[0].RequestID)
	require.Equal(t, ProtocolTimeoutApplied, events[0].Kind)
	require.Equal(t, "p1", events[0].Participant)
}
