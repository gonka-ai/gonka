package accounting

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/scheduler"
	"devshard/types"
)

// An epoch's worth of traffic.
const (
	benchEscrows         = 20
	benchGroupSize       = 16
	benchNoncesPerEscrow = 2000
	benchParticipants    = 40
)

func benchEscrowID(index int) string {
	return "escrow-" + string(rune('a'+index/26)) + string(rune('a'+index%26))
}

func benchParticipant(index int) string {
	return "gonka1participant" + string(rune('a'+index/26)) + string(rune('a'+index%26))
}

func benchHostStats() types.HostStats {
	return types.HostStats{Missed: 3, Invalid: 1, Cost: 900000, RequiredValidations: 12, CompletedValidations: 11}
}

// benchBook fills a book the way a live epoch does, with every nonce classified.
func benchBook(b *testing.B) *Book {
	b.Helper()
	book := NewBook(func() time.Time { return time.Unix(1700000000, 0).UTC() })
	terminals := []string{
		engine.TerminalWon.String(), engine.TerminalLost.String(), TerminalWarmupProbe,
		TerminalClientCancelled, TerminalUnnamed, "http_error",
	}
	for escrowIndex := range benchEscrows {
		escrowID := benchEscrowID(escrowIndex)
		slots := make([]types.SlotAssignment, 0, benchGroupSize)
		for slotID := range benchGroupSize {
			slots = append(slots, types.SlotAssignment{
				SlotID:           uint32(slotID),
				ValidatorAddress: benchParticipant((escrowIndex*3 + slotID) % benchParticipants),
			})
		}
		model := "model-a"
		if escrowIndex%2 == 1 {
			model = "model-b"
		}
		if err := book.OpenEscrow(EscrowMetadata{
			EscrowID: escrowID, Model: model, CreationEpoch: 41, Slots: slots,
		}); err != nil {
			b.Fatalf("OpenEscrow(%s): %v", escrowID, err)
		}
		if err := book.ObserveLatestNonce(escrowID, benchNoncesPerEscrow); err != nil {
			b.Fatalf("ObserveLatestNonce(): %v", err)
		}
		for slotID := range uint32(benchGroupSize) {
			if err := book.ObserveHostStats(escrowID, slotID, benchHostStats()); err != nil {
				b.Fatalf("ObserveHostStats(): %v", err)
			}
		}

		attempts := make([]Attempt, 0, benchNoncesPerEscrow)
		for nonce := uint64(1); nonce <= benchNoncesPerEscrow; nonce++ {
			attempt := Attempt{
				Nonce:        nonce,
				RequestID:    "request-" + string(rune('a'+int(nonce%16))),
				Sent:         true,
				Acknowledged: nonce%11 != 0,
				Usage:        UsageWinner,
				Terminal:     terminals[nonce%uint64(len(terminals))],
				Phase:        PhaseNormal,
				SlowReceipt:  nonce%23 == 0,
				SlowChunk:    nonce%29 == 0,
				ClockDrifted: nonce%31 == 0,
				SlowDecode:   nonce%37 == 0,
			}
			if nonce%3 == 0 {
				attempt.Usage = UsageLoser
			}
			if nonce%13 == 0 {
				attempt.Phase = PhasePoC
			}
			attempts = append(attempts, attempt)
		}
		if err := book.RecordRace(escrowID, attempts); err != nil {
			b.Fatalf("RecordRace(): %v", err)
		}
		inferences := make(map[uint64]*types.InferenceRecord, benchNoncesPerEscrow/4)
		for nonce := uint64(4); nonce <= benchNoncesPerEscrow; nonce += 4 {
			record := &types.InferenceRecord{
				ReservedCost: 1200, ActualCost: 900, InputTokens: 400, OutputTokens: 250,
				Status: types.StatusFinished, ExecutorSlot: uint32(nonce % benchGroupSize),
			}
			if nonce%600 == 0 {
				record.Status = types.StatusChallenged
			}
			inferences[nonce] = record
		}
		if err := book.ObserveInferences(escrowID, inferences); err != nil {
			b.Fatalf("ObserveInferences(): %v", err)
		}
		for nonce := uint64(1); nonce <= benchNoncesPerEscrow; nonce++ {
			switch {
			case nonce%7 == 0 && nonce%2 == 0:
				if err := book.RecordTimeout(escrowID, nonce, engine.TimeoutKindExecution,
					engine.TimeoutActionCompleted, engine.TimeoutReasonNotApplied); err != nil {
					b.Fatalf("RecordTimeout(): %v", err)
				}
			case nonce%53 == 0:
				if err := book.RecordGhost(escrowID, nonce, scheduler.GhostReasonWindowFull); err != nil {
					b.Fatalf("RecordGhost(): %v", err)
				}
			}
			if nonce%97 == 0 {
				if err := book.RecordAppliedTimeout(escrowID, nonce); err != nil {
					b.Fatalf("RecordAppliedTimeout(): %v", err)
				}
			}
		}
	}
	return book
}

func BenchmarkQuery(b *testing.B) {
	book := benchBook(b)
	b.ReportAllocs()
	for b.Loop() {
		if records := book.Query(QueryFilter{}); len(records) == 0 {
			b.Fatal("Query() found nothing")
		}
	}
}

func BenchmarkQueryOneParticipant(b *testing.B) {
	book := benchBook(b)
	filter := QueryFilter{EpochIndex: 41, Participant: benchParticipant(0)}
	b.ReportAllocs()
	for b.Loop() {
		if records := book.Query(filter); len(records) == 0 {
			b.Fatal("Query() found nothing")
		}
	}
}

func BenchmarkEpochs(b *testing.B) {
	book := benchBook(b)
	b.ReportAllocs()
	for b.Loop() {
		if summaries := book.Epochs(QueryFilter{}); len(summaries) == 0 {
			b.Fatal("Epochs() found nothing")
		}
	}
}

func BenchmarkFindings(b *testing.B) {
	records := benchBook(b).Query(QueryFilter{})
	widest := records[0]
	for _, record := range records {
		if len(record.Counters) > len(widest.Counters) {
			widest = record
		}
	}
	b.ReportAllocs()
	for b.Loop() {
		if findings := findingsFor(widest); len(findings) == 0 {
			b.Fatal("findingsFor() raised nothing")
		}
	}
}

func BenchmarkSnapshot(b *testing.B) {
	book := benchBook(b)
	b.ReportAllocs()
	for b.Loop() {
		if snapshot := book.Snapshot(); len(snapshot.Escrows) == 0 {
			b.Fatal("Snapshot() held nothing")
		}
	}
}

func benchStore(b *testing.B) *Store {
	b.Helper()
	store, err := OpenStore(filepath.Join(b.TempDir(), "accounting.db"))
	if err != nil {
		b.Fatalf("OpenStore(): %v", err)
	}
	b.Cleanup(func() { _ = store.Close() })
	return store
}

// BenchmarkSaveEveryEscrow times the write a start-up pays once: every escrow of the epoch.
func BenchmarkSaveEveryEscrow(b *testing.B) {
	book := benchBook(b)
	store := benchStore(b)
	escrowIDs := book.EscrowIDs()
	b.ReportAllocs()
	for b.Loop() {
		book.requeueUnsaved(unsavedChanges{escrowIDs: escrowIDs})
		if err := store.Save(context.Background(), book); err != nil {
			b.Fatalf("Save(): %v", err)
		}
	}
}

// BenchmarkSaveOneChangedEscrow times the steady write: one escrow moved since the last save.
func BenchmarkSaveOneChangedEscrow(b *testing.B) {
	book := benchBook(b)
	store := benchStore(b)
	if err := store.Save(context.Background(), book); err != nil {
		b.Fatalf("Save(): %v", err)
	}
	escrowID := benchEscrowID(0)
	b.ReportAllocs()
	for b.Loop() {
		book.requeueUnsaved(unsavedChanges{escrowIDs: []string{escrowID}})
		if err := store.Save(context.Background(), book); err != nil {
			b.Fatalf("Save(): %v", err)
		}
	}
}

// BenchmarkRecordRace times one race of a group's worth of attempts.
func BenchmarkRecordRace(b *testing.B) {
	book := benchBook(b)
	escrowID := benchEscrowID(0)
	attempts := make([]Attempt, benchGroupSize)
	terminals := [2]string{engine.TerminalWon.String(), engine.TerminalLost.String()}
	b.ReportAllocs()
	round := uint64(0)
	for b.Loop() {
		round++
		base := (round%63)*benchGroupSize + 1
		for index := range attempts {
			attempts[index] = Attempt{
				Nonce:        base + uint64(index),
				RequestID:    "request-live",
				Sent:         true,
				Acknowledged: true,
				Usage:        UsageWinner,
				Terminal:     terminals[round%2],
				Phase:        PhaseNormal,
			}
		}
		if err := book.RecordRace(escrowID, attempts); err != nil {
			b.Fatalf("RecordRace(): %v", err)
		}
	}
}

// BenchmarkUnfinishedNonces times the sweep's per-escrow read of unfinished nonces.
func BenchmarkUnfinishedNonces(b *testing.B) {
	book := benchBook(b)
	escrowID := benchEscrowID(0)
	b.ReportAllocs()
	for b.Loop() {
		if unfinished := book.UnfinishedNonces(escrowID); len(unfinished) == 0 {
			b.Fatal("UnfinishedNonces() found none")
		}
	}
}

// BenchmarkObserveInferences times the sweep's money pass over one escrow.
func BenchmarkObserveInferences(b *testing.B) {
	book := benchBook(b)
	escrowID := benchEscrowID(0)
	inferences := make(map[uint64]*types.InferenceRecord, benchNoncesPerEscrow)
	for nonce := uint64(1); nonce <= benchNoncesPerEscrow; nonce++ {
		inferences[nonce] = &types.InferenceRecord{
			ReservedCost: 1200, ActualCost: 900, InputTokens: 400, OutputTokens: 250,
			Status: types.StatusFinished, ExecutorSlot: uint32(nonce % benchGroupSize),
		}
	}
	b.ReportAllocs()
	for b.Loop() {
		if err := book.ObserveInferences(escrowID, inferences); err != nil {
			b.Fatalf("ObserveInferences(): %v", err)
		}
	}
}

func BenchmarkEvents(b *testing.B) {
	book := benchBook(b)
	b.ReportAllocs()
	for b.Loop() {
		if events := book.Events(QueryFilter{EpochIndex: 41}); len(events) == 0 {
			b.Fatal("Events() found none")
		}
	}
}

// BenchmarkServeParticipants times the whole participants route: query, findings and JSON encoding.
func BenchmarkServeParticipants(b *testing.B) {
	handler := NewHandler(benchBook(b), func(context.Context) (uint64, error) { return 41, nil }, nil)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/epochs/41/participants", nil)
	b.ReportAllocs()
	for b.Loop() {
		recorder := httptest.NewRecorder()
		recorder.Body = nil
		handler.ServeHTTP(discardingRecorder{recorder}, request)
		if recorder.Code != http.StatusOK {
			b.Fatalf("status = %d", recorder.Code)
		}
	}
}

type discardingRecorder struct{ *httptest.ResponseRecorder }

func (d discardingRecorder) Write(payload []byte) (int, error) { return io.Discard.Write(payload) }
