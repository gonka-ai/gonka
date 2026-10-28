package nonces

import (
	"testing"

	"devshard/cmd/gateway/accounting"
	"devshard/cmd/gateway/engine"
	"devshard/storage"
	"devshard/types"
)

const (
	benchTimedOutNonces     = 4000
	benchTimedOutSpacing    = 20
	benchSealedInsertBatch  = 1000
	benchSealedSessionEpoch = 1
)

type storedSealedSession struct {
	sealedSession
	store *storage.SQLite
}

func (s storedSealedSession) SealedInference(id uint64) (types.InferenceRecord, bool) {
	s.lookups[id]++
	row, kept, err := s.store.GetSealedInference(benchEscrow, id)
	if err != nil || !kept || !row.ObsPresent {
		return types.InferenceRecord{}, false
	}
	return types.InferenceRecord{Status: types.InferenceStatus(row.SealedStatus), ExecutorSlot: row.SealedExecutorSlot}, true
}

func (s storedSealedSession) totalLookups() int {
	total := 0
	for _, count := range s.lookups {
		total += count
	}
	return total
}

func benchTimedOutEscrow(b *testing.B, ledger *Recorder) (storedSealedSession, types.EscrowState) {
	b.Helper()
	store, err := storage.NewSQLite(b.TempDir())
	if err != nil {
		b.Fatalf("NewSQLite(): %v", err)
	}
	b.Cleanup(func() { _ = store.Close() })
	if err := store.CreateSession(storage.CreateSessionParams{EscrowID: benchEscrow, EpochID: benchSealedSessionEpoch, Version: "bench"}); err != nil {
		b.Fatalf("CreateSession(): %v", err)
	}
	rows := make([]storage.InferenceRow, 0, benchSealedInsertBatch)
	var latest uint64
	for index := range uint64(benchTimedOutNonces) {
		nonce := (index + 1) * benchTimedOutSpacing
		latest = nonce
		if err := ledger.service.Book.RecordAssigned(benchEscrow, nonce, "request-timed-out"); err != nil {
			b.Fatalf("RecordAssigned(): %v", err)
		}
		if err := ledger.service.Book.RecordRace(benchEscrow, []accounting.Attempt{{
			Nonce: nonce, RequestID: "request-timed-out", Sent: true, Acknowledged: true, Usage: accounting.UsageUnknown,
		}}); err != nil {
			b.Fatalf("RecordRace(): %v", err)
		}
		if err := ledger.service.Book.RecordTimeout(benchEscrow, nonce, engine.TimeoutKindExecution, engine.TimeoutActionCompleted, engine.TimeoutReasonNotApplied); err != nil {
			b.Fatalf("RecordTimeout(): %v", err)
		}
		rows = append(rows, storage.InferenceRow{
			InferenceID: nonce, SealedNonce: nonce + benchTimedOutSpacing, ObsPresent: true,
			SealedStatus: uint32(types.StatusTimedOut), SealedExecutorSlot: uint32(nonce % benchGroupSize),
		})
		if len(rows) == cap(rows) {
			if err := store.BulkInsertSealedInferences(benchEscrow, rows); err != nil {
				b.Fatalf("BulkInsertSealedInferences(): %v", err)
			}
			rows = rows[:0]
		}
	}
	return storedSealedSession{sealedSession: sealedSession{lookups: make(map[uint64]int, benchTimedOutNonces)}, store: store}, types.EscrowState{EscrowID: benchEscrow, LatestNonce: latest}
}

// Test flow:
//  1. Hold 4,000 dispatched nonces that timed out, each sealed TimedOut in a SQLite session store, and none live.
//  2. Close sealed nonces once, as the first reading does, then once per round, as every later reading does.
//  3. Report the sealed-record reads the first reading paid and those each later round paid.
func BenchmarkCloseSealedTimedOutNonces(b *testing.B) {
	ledger := benchRecorder(b)
	session, snapshot := benchTimedOutEscrow(b, ledger)
	ledger.closeSealed(benchEscrow, snapshot, newSealedRecords(session))
	firstReads := session.totalLookups()
	rounds := 0
	b.ReportAllocs()
	for b.Loop() {
		rounds++
		ledger.closeSealed(benchEscrow, snapshot, newSealedRecords(session))
	}
	b.ReportMetric(float64(firstReads), "first-reading-reads")
	b.ReportMetric(float64(session.totalLookups()-firstReads)/float64(rounds), "sealed-reads/op")
}
