package nonces

import (
	"testing"
	"time"

	"devshard/cmd/gateway/accounting"
	"devshard/types"
)

// One escrow of a live epoch: a full group, thousands of nonces still held in escrow state.
const (
	benchGroupSize  = 16
	benchInferences = 2000
	benchEscrow     = "escrow-1"
)

func benchRecorder(b *testing.B) *Recorder {
	b.Helper()
	service, err := accounting.NewService(accounting.Settings{
		Now: func() time.Time { return time.Unix(1700000000, 0).UTC() },
	})
	if err != nil {
		b.Fatalf("NewService(): %v", err)
	}
	b.Cleanup(func() { _ = service.Close() })
	slots := make([]types.SlotAssignment, 0, benchGroupSize)
	for slotID := range benchGroupSize {
		slots = append(slots, types.SlotAssignment{
			SlotID:           uint32(slotID),
			ValidatorAddress: "gonka1participant" + string(rune('a'+slotID)),
		})
	}
	if err := service.Book.OpenEscrow(accounting.EscrowMetadata{
		EscrowID: benchEscrow, Model: "model-a", CreationEpoch: 41, Slots: slots,
	}); err != nil {
		b.Fatalf("OpenEscrow(): %v", err)
	}
	return recorderFor(service)
}

func benchEscrowState() types.EscrowState {
	state := types.EscrowState{
		EscrowID:    benchEscrow,
		LatestNonce: benchInferences,
		Inferences:  make(map[uint64]*types.InferenceRecord, benchInferences),
		HostStats:   make(map[uint32]*types.HostStats, benchGroupSize),
	}
	for slotID := range uint32(benchGroupSize) {
		state.HostStats[slotID] = &types.HostStats{
			Missed: 3, Invalid: 1, Cost: 900000, RequiredValidations: 12, CompletedValidations: 11,
		}
	}
	for nonce := uint64(1); nonce <= benchInferences; nonce++ {
		record := &types.InferenceRecord{
			Status:       types.StatusFinished,
			ExecutorSlot: uint32(nonce % benchGroupSize),
			ReservedCost: 1200, ActualCost: 900, InputTokens: 400, OutputTokens: 250,
		}
		if nonce%17 == 0 {
			record.Status = types.StatusChallenged
		}
		state.Inferences[nonce] = record
	}
	return state
}

// What the ten-second sweep pays per escrow: every nonce's money and the challenges open against each slot.
func BenchmarkObserveEscrowState(b *testing.B) {
	ledger := benchRecorder(b)
	state := benchEscrowState()
	b.ReportAllocs()
	for b.Loop() {
		ledger.observeEscrowState(benchEscrow, state)
	}
}
