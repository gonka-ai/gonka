package accounting

import (
	"testing"

	"devshard/types"
)

// benchEscrowMetadata mirrors a small production escrow: one model, two slots.
func benchEscrowMetadata(escrowID string) EscrowMetadata {
	return EscrowMetadata{
		EscrowID:      escrowID,
		CreationEpoch: 42,
		Model:         "Qwen/Qwen3-235B-A22B-Instruct-2507-FP8",
		Phase:         EscrowActive,
		Slots: []types.SlotAssignment{
			{SlotID: 0, ValidatorAddress: "gonka1participantzero"},
			{SlotID: 1, ValidatorAddress: "gonka1participantone"},
		},
	}
}

func benchApply(b *testing.B, book *Book, event Event) {
	if err := book.Apply(event); err != nil {
		b.Fatalf("apply %T: %v", event, err)
	}
}

// BenchmarkBookApplyFinishedNonce measures the events one finished inference
// nonce produces on the request path: diff, real send, receipt, finish, winner,
// plus the host stats that ride along with each protocol transition.
func BenchmarkBookApplyFinishedNonce(b *testing.B) {
	book := NewBook()
	benchApply(b, book, EscrowRegistered{Metadata: benchEscrowMetadata("escrow-bench")})
	stats := types.HostStats{Cost: 1}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		nonce := uint64(i + 1)
		benchApply(b, book, DiffApplied{EscrowID: "escrow-bench", Nonce: nonce, Inference: true})
		benchApply(b, book, RealSend{
			EscrowID:      "escrow-bench",
			Nonce:         nonce,
			DispatchPhase: PhaseNormal,
			Quarantine:    QuarantineNone,
		})
		benchApply(b, book, ProtocolTransition{EscrowID: "escrow-bench", Nonce: nonce, Kind: ProtocolReceiptApplied})
		benchApply(b, book, HostStatsObserved{EscrowID: "escrow-bench", SlotID: uint32(nonce % 2), Stats: stats})
		benchApply(b, book, ProtocolTransition{EscrowID: "escrow-bench", Nonce: nonce, Kind: ProtocolFinishApplied})
		benchApply(b, book, HostStatsObserved{EscrowID: "escrow-bench", SlotID: uint32(nonce % 2), Stats: stats})
		benchApply(b, book, Winner{EscrowID: "escrow-bench", Nonce: nonce})
	}
}

// BenchmarkBookQueryEpoch measures one API/Prometheus read over an escrow that
// already holds a full spread of counter keys.
func BenchmarkBookQueryEpoch(b *testing.B) {
	book := NewBook()
	benchApply(b, book, EscrowRegistered{Metadata: benchEscrowMetadata("escrow-bench")})
	for nonce := uint64(1); nonce <= 2000; nonce++ {
		benchApply(b, book, DiffApplied{EscrowID: "escrow-bench", Nonce: nonce, Inference: true})
		benchApply(b, book, RealSend{
			EscrowID:      "escrow-bench",
			Nonce:         nonce,
			DispatchPhase: PhaseNormal,
			Quarantine:    QuarantineNone,
		})
		benchApply(b, book, ProtocolTransition{EscrowID: "escrow-bench", Nonce: nonce, Kind: ProtocolFinishApplied})
		if nonce%3 == 0 {
			benchApply(b, book, Loser{EscrowID: "escrow-bench", Nonce: nonce})
			continue
		}
		benchApply(b, book, Winner{EscrowID: "escrow-bench", Nonce: nonce})
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if records := book.Query(QueryFilter{EpochIndex: 42}); len(records) != 2 {
			b.Fatalf("expected 2 participant records, got %d", len(records))
		}
	}
}
