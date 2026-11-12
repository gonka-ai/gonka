package accounting

import (
	"encoding/json"
	"net/http"
	"testing"

	"devshard/types"
)

// Test flow:
//  1. Open two escrows for the same host address on two different models, each observing host stats, an inference record and a race attempt.
//  2. Request the current-epoch participants route.
//  3. Assert the response status is 200 and decode it.
//  4. Assert the host appears as two separate rows, one per model.
//  5. Assert each row's chain, reserved, actual and refunded costs and input/output tokens match what was observed.
func TestParticipantsEndpointReturnsCostPerParticipantAndModel(t *testing.T) {
	t.Parallel()
	book := NewBook(nil)
	for _, escrow := range []struct {
		id      string
		model   string
		address string
		cost    uint64
		nonce   uint64
		record  *types.InferenceRecord
	}{
		{
			id: "5", model: "Qwen/Test", address: "gonka1aaa", cost: 100, nonce: 2,
			record: &types.InferenceRecord{ReservedCost: 900, ActualCost: 400, InputTokens: 64, OutputTokens: 16, Status: types.StatusFinished},
		},
		{
			id: "6", model: "Kimi/Test", address: "gonka1aaa", cost: 70, nonce: 4,
			record: &types.InferenceRecord{ReservedCost: 500, ActualCost: 500, InputTokens: 32, OutputTokens: 8, Status: types.StatusFinished},
		},
	} {
		if err := book.OpenEscrow(EscrowMetadata{
			EscrowID:      escrow.id,
			Model:         escrow.model,
			CreationEpoch: testEpoch,
			Slots:         []types.SlotAssignment{{SlotID: 0, ValidatorAddress: escrow.address}},
		}); err != nil {
			t.Fatalf("OpenEscrow %s: %v", escrow.id, err)
		}
		if err := book.ObserveHostStats(escrow.id, 0, types.HostStats{Cost: escrow.cost}); err != nil {
			t.Fatalf("ObserveHostStats %s: %v", escrow.id, err)
		}
		if err := book.ObserveInferences(escrow.id, map[uint64]*types.InferenceRecord{escrow.nonce: escrow.record}); err != nil {
			t.Fatalf("ObserveInferences %s: %v", escrow.id, err)
		}
		if err := book.RecordRace(escrow.id, []Attempt{{
			Nonce: escrow.nonce, RequestID: "request-1", Sent: true,
			Usage: UsageWinner, OutputTokens: int64(escrow.record.OutputTokens),
		}}); err != nil {
			t.Fatalf("RecordRace %s: %v", escrow.id, err)
		}
		markFinished(t, book, escrow.id, escrow.nonce)
	}

	recorder := serve(t, book, "/api/v1/epochs/current/participants")

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body)
	}
	var body struct {
		Participants []struct {
			Participant  string `json:"participant"`
			Model        string `json:"model"`
			ChainCost    uint64 `json:"chain_cost"`
			ReservedCost uint64 `json:"reserved_cost"`
			ActualCost   uint64 `json:"actual_cost"`
			RefundedCost uint64 `json:"refunded_cost"`
			InputTokens  uint64 `json:"input_tokens"`
			OutputTokens uint64 `json:"output_tokens"`
		} `json:"participants"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding the reply: %v", err)
	}

	byModel := map[string]int{}
	for index, row := range body.Participants {
		byModel[row.Participant+"|"+row.Model] = index
	}
	qwen, served := byModel["gonka1aaa|Qwen/Test"]
	if !served {
		t.Fatalf("no row for gonka1aaa on Qwen/Test; got %+v", body.Participants)
	}
	kimi, alsoServed := byModel["gonka1aaa|Kimi/Test"]
	if !alsoServed {
		t.Fatalf("one host on two models must be two rows; got %+v", body.Participants)
	}

	for _, want := range []struct {
		name  string
		got   uint64
		value uint64
	}{
		{"qwen chain cost", body.Participants[qwen].ChainCost, 100},
		{"qwen reserved", body.Participants[qwen].ReservedCost, 900},
		{"qwen actual", body.Participants[qwen].ActualCost, 400},
		{"qwen refunded", body.Participants[qwen].RefundedCost, 500},
		{"qwen input tokens", body.Participants[qwen].InputTokens, 64},
		{"qwen output tokens", body.Participants[qwen].OutputTokens, 16},
		{"kimi chain cost", body.Participants[kimi].ChainCost, 70},
		{"kimi actual", body.Participants[kimi].ActualCost, 500},
		{"kimi refunded", body.Participants[kimi].RefundedCost, 0},
	} {
		if want.got != want.value {
			t.Errorf("%s = %d, want %d", want.name, want.got, want.value)
		}
	}
}

// Test flow:
//  1. Open an escrow with one slot and observe an inference record carrying input length, max tokens and token counts.
//  2. Request the current-epoch participants route and decode the response.
//  3. Assert one host row holding one slot comes back.
//  4. Assert both the host row and its slot carry the same estimated-input and max-tokens values under the keys an outside tracker reads.
func TestParticipantsEndpointServesWhatTheHostWasGiven(t *testing.T) {
	t.Parallel()
	book := NewBook(nil)
	if err := book.OpenEscrow(EscrowMetadata{
		EscrowID:      "5",
		Model:         "Qwen/Test",
		CreationEpoch: testEpoch,
		Slots:         []types.SlotAssignment{{SlotID: 0, ValidatorAddress: "gonka1aaa"}},
	}); err != nil {
		t.Fatalf("OpenEscrow: %v", err)
	}
	if err := book.ObserveInferences("5", map[uint64]*types.InferenceRecord{2: {
		InputLength: 2_048, MaxTokens: 256, InputTokens: 490, OutputTokens: 200, Status: types.StatusFinished,
	}}); err != nil {
		t.Fatalf("ObserveInferences: %v", err)
	}

	recorder := serve(t, book, "/api/v1/epochs/current/participants")

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body)
	}
	var body struct {
		Participants []struct {
			EstimatedInput uint64 `json:"estimated_input_tokens"`
			MaxTokens      uint64 `json:"max_tokens"`
			Slots          []struct {
				EstimatedInput uint64 `json:"estimated_input_tokens"`
				MaxTokens      uint64 `json:"max_tokens"`
			} `json:"slots"`
		} `json:"participants"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding the reply: %v", err)
	}

	if len(body.Participants) != 1 || len(body.Participants[0].Slots) != 1 {
		t.Fatalf("served %+v, want one host holding one slot", body.Participants)
	}
	host, slot := body.Participants[0], body.Participants[0].Slots[0]
	for _, field := range []struct {
		name string
		got  uint64
		want uint64
	}{
		{"host estimated_input_tokens", host.EstimatedInput, 512},
		{"host max_tokens", host.MaxTokens, 256},
		{"slot estimated_input_tokens", slot.EstimatedInput, 512},
		{"slot max_tokens", slot.MaxTokens, 256},
	} {
		if field.got != field.want {
			t.Errorf("%s = %d, want %d under exactly that key", field.name, field.got, field.want)
		}
	}
}
