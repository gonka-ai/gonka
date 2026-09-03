package storage

import (
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

var errRebuildFailed = errors.New("rebuild failed")

func newGateTestStore(t *testing.T) *ObsRepairGate {
	t.Helper()
	inner := NewMemory()
	require.NoError(t, inner.CreateSession(CreateSessionParams{EscrowID: "escrow-1", EpochID: 1, Version: "test"}))
	return NewObsRepairGate(inner)
}

func gateObsForSlot(t *testing.T, store Storage, slotID uint32) SlotValidationObs {
	t.Helper()
	rows, err := store.GetValidationObservability("escrow-1")
	require.NoError(t, err)
	for _, r := range rows {
		if r.SlotID == slotID {
			return r
		}
	}
	return SlotValidationObs{SlotID: slotID}
}

// With no repair running the gate must be invisible.
func TestObsRepairGate_PassesThroughWhenIdle(t *testing.T) {
	gate := newGateTestStore(t)

	require.False(t, gate.RepairInProgress("escrow-1"))
	require.NoError(t, gate.RecordValidationsAppliedOnce("escrow-1", []ValidationObsEntry{{InferenceID: 7, SlotID: 2}}))

	require.Equal(t, uint32(1), gateObsForSlot(t, gate, 2).CompletedValidations)
}

// The rebuild's own writes must bypass the queue, or it would queue its work
// behind itself and deadlock its own flush.
func TestObsRepairGate_RebuildWritesBypassTheQueue(t *testing.T) {
	gate := newGateTestStore(t)

	err := gate.RepairValidationObs("escrow-1", func(inner Storage) error {
		require.True(t, gate.RepairInProgress("escrow-1"))
		return RebuildValidationObsFromDiffs(inner, "escrow-1", []types.DiffRecord{{
			Diff: types.Diff{Nonce: 1, Txs: []*types.DevshardTx{validationTx(7, 2)}},
		}}, nil)
	})
	require.NoError(t, err)

	require.False(t, gate.RepairInProgress("escrow-1"))
	require.Equal(t, uint32(1), gateObsForSlot(t, gate, 2).CompletedValidations)
}

// A live write during the window must land after the rebuild, not be lost and
// not be counted twice.
func TestObsRepairGate_QueuedWriteAppliedAfterRebuild(t *testing.T) {
	gate := newGateTestStore(t)

	err := gate.RepairValidationObs("escrow-1", func(inner Storage) error {
		if err := RebuildValidationObsFromDiffs(inner, "escrow-1", []types.DiffRecord{{
			Diff: types.Diff{Nonce: 1, Txs: []*types.DevshardTx{validationTx(7, 2)}},
		}}, nil); err != nil {
			return err
		}
		// Stands in for the live apply path writing while the rebuild runs.
		require.NoError(t, gate.RecordValidationsAppliedOnce("escrow-1",
			[]ValidationObsEntry{{InferenceID: 9, SlotID: 3}}))
		require.Equal(t, uint32(0), gateObsForSlot(t, gate, 3).CompletedValidations,
			"the queued write must not be visible yet")
		return nil
	})
	require.NoError(t, err)

	require.Equal(t, uint32(1), gateObsForSlot(t, gate, 2).CompletedValidations, "rebuilt row")
	require.Equal(t, uint32(1), gateObsForSlot(t, gate, 3).CompletedValidations, "queued row")
}

// The clear would otherwise wipe a live write, and re-recording a drained
// inference would double count it. Queueing must avoid both.
func TestObsRepairGate_QueuedWriteSurvivesTheClear(t *testing.T) {
	gate := newGateTestStore(t)

	err := gate.RepairValidationObs("escrow-1", func(inner Storage) error {
		// Live write arrives before the rebuild clears the tables.
		require.NoError(t, gate.RecordValidationsAppliedOnce("escrow-1",
			[]ValidationObsEntry{{InferenceID: 9, SlotID: 3}}))
		return RebuildValidationObsFromDiffs(inner, "escrow-1", []types.DiffRecord{{
			Diff: types.Diff{Nonce: 1, Txs: []*types.DevshardTx{validationTx(7, 2)}},
		}}, nil)
	})
	require.NoError(t, err)

	require.Equal(t, uint32(1), gateObsForSlot(t, gate, 3).CompletedValidations,
		"a write queued before the clear must still be applied once")
}

// Queued drains have to keep their order relative to the record they follow,
// otherwise the drain runs against a row that does not exist yet.
func TestObsRepairGate_QueuePreservesRecordThenDrainOrder(t *testing.T) {
	gate := newGateTestStore(t)

	err := gate.RepairValidationObs("escrow-1", func(inner Storage) error {
		require.NoError(t, gate.RecordValidationsAppliedOnce("escrow-1",
			[]ValidationObsEntry{{InferenceID: 9, SlotID: 3}}))
		require.NoError(t, gate.DrainInferenceValidationObs("escrow-1", 9))
		return nil
	})
	require.NoError(t, err)

	// The drain moved the row into sealed storage; the union still reports it
	// exactly once.
	require.Equal(t, uint32(1), gateObsForSlot(t, gate, 3).CompletedValidations)
}

func TestObsRepairGate_RejectsConcurrentRepairForSameEscrow(t *testing.T) {
	gate := newGateTestStore(t)

	err := gate.RepairValidationObs("escrow-1", func(Storage) error {
		return gate.RepairValidationObs("escrow-1", func(Storage) error {
			t.Fatal("a second repair must not run for the same escrow")
			return nil
		})
	})
	require.ErrorContains(t, err, "already running")
}

// Queueing is keyed per escrow, so a repair on one must not swallow another's
// live writes.
func TestObsRepairGate_OtherEscrowsAreUnaffected(t *testing.T) {
	inner := NewMemory()
	require.NoError(t, inner.CreateSession(CreateSessionParams{EscrowID: "escrow-1", EpochID: 1, Version: "test"}))
	require.NoError(t, inner.CreateSession(CreateSessionParams{EscrowID: "escrow-2", EpochID: 1, Version: "test"}))
	gate := NewObsRepairGate(inner)

	err := gate.RepairValidationObs("escrow-1", func(Storage) error {
		require.NoError(t, gate.RecordValidationsAppliedOnce("escrow-2",
			[]ValidationObsEntry{{InferenceID: 7, SlotID: 4}}))
		rows, err := gate.GetValidationObservability("escrow-2")
		require.NoError(t, err)
		require.Len(t, rows, 1, "another escrow's write must not be queued")
		return nil
	})
	require.NoError(t, err)
}

func TestObsRepairGate_QueueOverflowIsReported(t *testing.T) {
	gate := newGateTestStore(t)

	err := gate.RepairValidationObs("escrow-1", func(Storage) error {
		for i := range maxQueuedObsOps + 5 {
			require.NoError(t, gate.DrainInferenceValidationObs("escrow-1", uint64(i)))
		}
		return nil
	})
	require.ErrorContains(t, err, "dropped 5 queued writes")
}

// A rebuild failure must not strand the gate: the queue still has to flush and
// the escrow has to return to write-through.
func TestObsRepairGate_FlushesAndClosesAfterRebuildFailure(t *testing.T) {
	gate := newGateTestStore(t)

	err := gate.RepairValidationObs("escrow-1", func(Storage) error {
		require.NoError(t, gate.RecordValidationsAppliedOnce("escrow-1",
			[]ValidationObsEntry{{InferenceID: 9, SlotID: 3}}))
		return errRebuildFailed
	})
	require.ErrorIs(t, err, errRebuildFailed)

	require.False(t, gate.RepairInProgress("escrow-1"), "the gate must not stay open")
	require.Equal(t, uint32(1), gateObsForSlot(t, gate, 3).CompletedValidations,
		"queued writes are the only record of the window and must still be applied")
}

func TestObsRepairGate_ConcurrentWritersDuringRepair(t *testing.T) {
	gate := newGateTestStore(t)

	const writers = 8
	var wg sync.WaitGroup
	err := gate.RepairValidationObs("escrow-1", func(inner Storage) error {
		for i := range writers {
			wg.Add(1)
			go func(slot uint32) {
				defer wg.Done()
				_ = gate.RecordValidationsAppliedOnce("escrow-1",
					[]ValidationObsEntry{{InferenceID: uint64(slot) + 100, SlotID: slot}})
			}(uint32(i))
		}
		wg.Wait()
		return RebuildValidationObsFromDiffs(inner, "escrow-1", nil, nil)
	})
	require.NoError(t, err)

	for slot := range uint32(writers) {
		require.Equal(t, uint32(1), gateObsForSlot(t, gate, slot).CompletedValidations,
			"write from slot %d lost", slot)
	}
}
