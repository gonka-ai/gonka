package accounting

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"devshard/types"
)

// The ledger's one arithmetic promise: every nonce the chain assigned to a slot is accounted for
// exactly once, in exactly one bucket. buildSlotRecord derives Unclassified and Overclassified from
// this difference, so a break here is a wrong number everywhere downstream.
func requireSlotIdentity(t *testing.T, record ParticipantRecord, where string) {
	t.Helper()
	for _, slot := range record.Slots {
		var dispositions uint64
		for _, count := range slot.Dispositions {
			dispositions += count
		}
		accounted := dispositions + slot.InFlight + slot.PendingClassification
		require.Equal(t, slot.AssignedNonces, accounted+slot.Unclassified-slot.Overclassified,
			"%s: slot %d of %s does not add up: dispositions=%d in_flight=%d pending=%d unclassified=%d overclassified=%d assigned=%d",
			where, slot.SlotID, slot.EscrowID, dispositions, slot.InFlight, slot.PendingClassification,
			slot.Unclassified, slot.Overclassified, slot.AssignedNonces)
	}
}

func requireCountedOnce(t *testing.T, tr *Tracker, escrowID string, want uint64) {
	t.Helper()
	var total uint64
	for _, record := range tr.Query(QueryFilter{EpochIndex: 7}) {
		for _, counter := range record.Counters {
			if counter.EscrowID == escrowID {
				total += counter.Count
			}
		}
	}
	require.Equal(t, want, total, "every nonce leaves exactly one counter, across every participant")
}

// Delivery reason joined the counter key in this range, which moves a nonce from one bucket to
// another as facts arrive. Every arrival order has to leave the same single count behind.
func TestEveryEventOrderingKeepsTheLedgerBalanced(t *testing.T) {
	deliver := func(tr *Tracker, nonce uint64) error {
		return tr.RecordUsage("e1", nonce, UsageWinner, "content")
	}
	finish := func(tr *Tracker, nonce uint64) error {
		return tr.RecordProtocol("e1", nonce, AssignedNonceSlot(nonce, 2), ProtocolFinishApplied, types.HostStats{})
	}
	timeout := func(tr *Tracker, nonce uint64) error {
		return tr.RecordTimeout(TimeoutRecord{
			EscrowID: "e1", Nonce: nonce, Kind: TimeoutRefused, Phase: PhaseNormal, Outcome: TimeoutApplied,
		})
	}
	receipt := func(tr *Tracker, nonce uint64) error {
		return tr.RecordProtocol("e1", nonce, AssignedNonceSlot(nonce, 2), ProtocolReceiptApplied, types.HostStats{})
	}

	orderings := []struct {
		name  string
		steps []func(*Tracker, uint64) error
	}{
		{"usage then finish", []func(*Tracker, uint64) error{deliver, finish}},
		{"finish then usage", []func(*Tracker, uint64) error{finish, deliver}},
		{"receipt, usage, finish", []func(*Tracker, uint64) error{receipt, deliver, finish}},
		{"receipt, finish, usage", []func(*Tracker, uint64) error{receipt, finish, deliver}},
		{"usage twice", []func(*Tracker, uint64) error{deliver, deliver, finish}},
		{"finish twice", []func(*Tracker, uint64) error{deliver, finish, finish}},
		{"timeout then a late finish", []func(*Tracker, uint64) error{timeout, deliver, finish}},
	}
	for _, ordering := range orderings {
		t.Run(ordering.name, func(t *testing.T) {
			tr := newTestTracker(t)
			registerEscrow(t, tr, "e1", 7, "m")
			const nonce = uint64(2)
			require.NoError(t, tr.RecordDiff("e1", nonce, true))
			require.NoError(t, tr.RecordRealSend("e1", nonce, accountingTestNow.Add(-2*time.Minute), PhaseNormal, QuarantineNone))
			for _, step := range ordering.steps {
				require.NoError(t, step(tr, nonce))
			}

			for _, record := range tr.Query(QueryFilter{EpochIndex: 7}) {
				requireSlotIdentity(t, record, ordering.name)
			}
			requireCountedOnce(t, tr, "e1", 1)
		})
	}
}

// The same nonce reclassified through every disposition it can reach must never leave a stale count
// behind: this is the double-count that a remove-then-add can lose.
func TestReclassificationNeverLeavesAStaleCount(t *testing.T) {
	tr := newTestTracker(t)
	registerEscrow(t, tr, "e1", 7, "m")
	const nonce = uint64(2)
	require.NoError(t, tr.RecordDiff("e1", nonce, true))
	require.NoError(t, tr.RecordRealSend("e1", nonce, accountingTestNow.Add(-2*time.Minute), PhaseNormal, QuarantineNone))
	require.NoError(t, tr.RecordTimeout(TimeoutRecord{
		EscrowID: "e1", Nonce: nonce, Kind: TimeoutRefused, Phase: PhaseNormal, Outcome: TimeoutApplied,
	}))

	afterTimeout := tr.Query(QueryFilter{EpochIndex: 7, Participant: "p0"})[0]
	requireSlotIdentity(t, afterTimeout, "after timeout")

	var counted uint64
	for _, counter := range afterTimeout.Counters {
		counted += counter.Count
	}
	require.Equal(t, uint64(1), counted, "one nonce must leave exactly one counter, whatever it passed through")
}

// A snapshot written before DeliveryReason joined the key has to load with its counts intact. The
// field is absent from the stored JSON, and absent must read as "not known", never as a new bucket.
func TestSnapshotWrittenBeforeDeliveryReasonLoadsUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounting.db")
	before, err := OpenTracker(path, 0, 0)
	require.NoError(t, err)
	before.now = func() time.Time { return accountingTestNow }
	registerEscrow(t, before, "e1", 7, "m")
	for nonce := uint64(2); nonce <= 8; nonce += 2 {
		require.NoError(t, before.RecordDiff("e1", nonce, true))
		require.NoError(t, before.RecordRealSend("e1", nonce, accountingTestNow.Add(-2*time.Minute), PhaseNormal, QuarantineNone))
		require.NoError(t, before.RecordTimeout(TimeoutRecord{
			EscrowID: "e1", Nonce: nonce, Kind: TimeoutRefused, Phase: PhaseNormal, Outcome: TimeoutApplied,
		}))
	}
	require.NoError(t, before.Flush(context.Background()))
	dispositionsBefore := tallyDispositions(before.Query(QueryFilter{EpochIndex: 7}))
	require.NoError(t, before.Close())

	require.NoError(t, stripDeliveryReasonFromStore(path))

	after, err := OpenTracker(path, 0, 0)
	require.NoError(t, err)
	after.now = func() time.Time { return accountingTestNow }
	t.Cleanup(func() { require.NoError(t, after.Close()) })

	records := after.Query(QueryFilter{EpochIndex: 7})
	require.Equal(t, dispositionsBefore, tallyDispositions(records), "an older snapshot must not change any total")
	for _, record := range records {
		requireSlotIdentity(t, record, "after loading an older snapshot")
		for _, counter := range record.Counters {
			require.Empty(t, counter.Key.DeliveryReason, "an absent delivery reason must stay absent")
		}
	}
}

// The delivery metric is a second view of counters the disposition metric already publishes, so the
// two must agree on the total rather than each inventing one.
func TestDeliveryMetricAgreesWithTheDispositionsItSplits(t *testing.T) {
	tr := newTestTracker(t)
	registerEscrow(t, tr, "e1", 7, "m")
	for nonce := uint64(2); nonce <= 12; nonce += 2 {
		require.NoError(t, tr.RecordDiff("e1", nonce, true))
		require.NoError(t, tr.RecordRealSend("e1", nonce, accountingTestNow, PhaseNormal, QuarantineNone))
		reason := "content"
		if nonce%4 == 0 {
			reason = "empty_stream"
		}
		require.NoError(t, tr.RecordUsage("e1", nonce, UsageWinner, reason))
		require.NoError(t, tr.RecordProtocol("e1", nonce, AssignedNonceSlot(nonce, 2), ProtocolFinishApplied, types.HostStats{}))
	}

	registry := prometheus.NewRegistry()
	require.NoError(t, registry.Register(NewCollector(tr, func(context.Context) (uint64, error) { return 7, nil })))
	families, err := registry.Gather()
	require.NoError(t, err)

	totals := map[string]float64{}
	for _, family := range families {
		for _, metric := range family.GetMetric() {
			totals[family.GetName()] += metric.GetGauge().GetValue()
		}
	}
	require.Equal(t, float64(6), totals["devshard_accounting_disposition"])
	require.Equal(t, totals["devshard_accounting_disposition"], totals["devshard_accounting_delivery"],
		"every delivered nonce carries a reason, so the split must cover the same total")
}

func tallyDispositions(records []ParticipantRecord) map[Disposition]uint64 {
	totals := make(map[Disposition]uint64)
	for _, record := range records {
		for disposition, count := range record.Dispositions {
			totals[disposition] += count
		}
	}
	return totals
}

// stripDeliveryReasonFromStore rewrites the stored blobs the way a gateway running the previous
// schema would have written them: no delivery_reason anywhere, and the older version in the meta row.
func stripDeliveryReasonFromStore(path string) error {
	return stripKeyFromStore(path, "delivery_reason")
}

func stripKeyFromStore(path, key string) error {
	store, err := OpenStore(path, 0)
	if err != nil {
		return err
	}
	defer store.Close()
	rows, err := store.db.Query(`SELECT escrow_id, payload FROM accounting_escrows`)
	if err != nil {
		return err
	}
	rewritten := map[string][]byte{}
	for rows.Next() {
		var escrowID string
		var raw []byte
		if err := rows.Scan(&escrowID, &raw); err != nil {
			rows.Close()
			return err
		}
		var generic any
		if err := json.Unmarshal(raw, &generic); err != nil {
			rows.Close()
			return err
		}
		stripped, err := json.Marshal(dropKey(generic, key))
		if err != nil {
			rows.Close()
			return err
		}
		rewritten[escrowID] = stripped
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for escrowID, payload := range rewritten {
		if _, err := store.db.Exec(`UPDATE accounting_escrows SET payload = ? WHERE escrow_id = ?`, payload, escrowID); err != nil {
			return err
		}
	}
	_, err = store.db.Exec(`UPDATE accounting_meta SET value = ? WHERE key = 'schema_version'`, SchemaVersion-1)
	return err
}

func dropKey(node any, name string) any {
	switch typed := node.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, value := range typed {
			if key == name {
				continue
			}
			out[key] = dropKey(value, name)
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, value := range typed {
			out = append(out, dropKey(value, name))
		}
		return out
	default:
		return node
	}
}

// An escrow belongs to the epoch it was created in. Re-registering it under a later one would carry
// every counter it already holds across the boundary, which is how a ledger reports one epoch's work
// as another's.
func TestReRegisteringAnEscrowCannotMoveItsCountersToAnotherEpoch(t *testing.T) {
	tr := newTestTracker(t)
	registerEscrow(t, tr, "e1", 7, "m")
	require.NoError(t, tr.RecordDiff("e1", 2, true))
	require.NoError(t, tr.RecordRealSend("e1", 2, accountingTestNow.Add(-2*time.Minute), PhaseNormal, QuarantineNone))
	require.NoError(t, tr.RecordTimeout(TimeoutRecord{
		EscrowID: "e1", Nonce: 2, Kind: TimeoutRefused, Phase: PhaseNormal, Outcome: TimeoutApplied,
	}))

	err := tr.RegisterEscrow(EscrowMetadata{
		EscrowID: "e1", CreationEpoch: 8, Model: "m", Phase: EscrowActive,
		RefusalTimeout: 60, ExecutionTimeout: 1200, TimeoutBufferSeconds: 5,
		Slots: []types.SlotAssignment{
			{SlotID: 0, ValidatorAddress: "p0"},
			{SlotID: 1, ValidatorAddress: "p1"},
		},
	})
	require.Error(t, err, "a second epoch for the same escrow must be refused, not accepted")

	require.Empty(t, tr.Query(QueryFilter{EpochIndex: 8}), "epoch 8 saw none of this work")
	requireCountedOnce(t, tr, "e1", 1)
	epoch7 := tr.Query(QueryFilter{EpochIndex: 7, Participant: "p0"})[0]
	require.Equal(t, uint64(1), epoch7.Dispositions[DispositionUnfinishedRefused])
}

// Overclassified is the ledger admitting it counted more than the chain handed out. No sequence of
// real events reaches it, which is why nothing else in this package covers the branch — and it is
// what the ledger_overcounted finding keys on, so it is worth pinning directly.
func TestOverclassifiedIsReportedRatherThanHiddenInUnclassified(t *testing.T) {
	meta := EscrowMetadata{
		EscrowID: "e1", CreationEpoch: 7, Model: "m", Phase: EscrowActive,
		RefusalTimeout: 60, ExecutionTimeout: 1200, TimeoutBufferSeconds: 5,
		Slots: []types.SlotAssignment{
			{SlotID: 0, ValidatorAddress: "p0"},
			{SlotID: 1, ValidatorAddress: "p1"},
		},
	}
	assigned, err := AssignedNoncesForSlot(2, 2, 0)
	require.NoError(t, err)
	require.Equal(t, uint64(1), assigned, "latest nonce 2 gives slot 0 exactly one nonce")

	view := &escrowView{
		id:     "e1",
		meta:   meta,
		latest: 2,
		counters: map[CounterKey]uint64{
			{SlotID: 0, Disposition: DispositionFinishedUsed}:      2,
			{SlotID: 0, Disposition: DispositionUnfinishedRefused}: 1,
		},
		hostStats:       map[uint32]types.HostStats{},
		challengeBySlot: map[uint32]uint64{},
		invalidBySlot:   map[uint32]uint64{},
	}

	record := buildSlotRecord(view, 0, accountingTestNow)
	require.Equal(t, uint64(2), record.Overclassified, "three counters against one assigned nonce is a surplus of two")
	require.Zero(t, record.Unclassified, "a surplus is not a shortfall")
	require.Zero(t, record.CrossCheckError,
		"a surplus is this ledger disagreeing with itself; the cross-check reports disagreement with the chain")
}

// A snapshot written before a per-slot map existed loads it as nil, and Go panics on a write to a nil
// map. The guards beside this one exist for the same reason; a new map without one crashes the
// gateway on the first event after an upgrade, not on the upgrade itself.
func TestSnapshotWithoutTheNewerSlotMapsStaysWritable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounting.db")
	before, err := OpenTracker(path, 0, 0)
	require.NoError(t, err)
	before.now = func() time.Time { return accountingTestNow }
	registerEscrow(t, before, "e1", 7, "m")
	require.NoError(t, before.RecordDiff("e1", 2, true))
	require.NoError(t, before.Flush(context.Background()))
	require.NoError(t, before.Close())

	require.NoError(t, stripKeyFromStore(path, "validated_by_slot"))
	require.NoError(t, stripKeyFromStore(path, "timed_out_by_slot"))

	after, err := OpenTracker(path, 0, 0)
	require.NoError(t, err)
	after.now = func() time.Time { return accountingTestNow }
	t.Cleanup(func() { require.NoError(t, after.Close()) })

	require.NoError(t, after.RecordValidatorWork("e1", []uint32{1}))
	require.Equal(t, uint64(1), recordFor(t, after, "p1").ValidationsPerformed)

	require.NoError(t, after.RecordCommittedState("e1", types.Diff{Nonce: 3, Txs: []*types.DevshardTx{{
		Tx: &types.DevshardTx_TimeoutInference{TimeoutInference: &types.MsgTimeoutInference{InferenceId: 2}},
	}}}, nil, EscrowActive, nil))
	require.Equal(t, uint64(1), recordFor(t, after, "p0").TimeoutsApplied)
}

// The per-slot count has to survive a restart like every other tally beside it.
func TestValidationsPerformedSurviveARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounting.db")
	before, err := OpenTracker(path, 0, 0)
	require.NoError(t, err)
	before.now = func() time.Time { return accountingTestNow }
	registerEscrow(t, before, "e1", 7, "m")
	require.NoError(t, before.RecordDiff("e1", 2, true))
	require.NoError(t, before.RecordValidatorWork("e1", []uint32{1, 1}))
	require.NoError(t, before.RecordCommittedState("e1", types.Diff{Nonce: 4, Txs: []*types.DevshardTx{{
		Tx: &types.DevshardTx_TimeoutInference{TimeoutInference: &types.MsgTimeoutInference{InferenceId: 3}},
	}}}, nil, EscrowActive, nil))
	require.NoError(t, before.Flush(context.Background()))
	require.NoError(t, before.Close())

	after, err := OpenTracker(path, 0, 0)
	require.NoError(t, err)
	after.now = func() time.Time { return accountingTestNow }
	t.Cleanup(func() { require.NoError(t, after.Close()) })

	require.Equal(t, uint64(2), recordFor(t, after, "p1").ValidationsPerformed)
	require.Equal(t, uint64(1), recordFor(t, after, "p1").TimeoutsApplied)
}
