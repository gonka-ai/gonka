package accounting

import (
	"devshard/types"
)

// nonceCost is one nonce's money as the escrow recorded it. See docs/accounting.md, "Money and tokens".
type nonceCost struct {
	reserved    uint64
	actual      uint64
	inputLength uint64
	maxTokens   uint64
	input       uint64
	output      uint64
	status      types.InferenceStatus
}

// SlotMoney is one slot's share of the escrow's money. See docs/accounting.md, "Money and tokens".
type SlotMoney struct {
	Reserved              uint64 `json:"reserved_cost,omitempty"`
	Actual                uint64 `json:"actual_cost,omitempty"`
	Refunded              uint64 `json:"refunded_cost,omitempty"`
	EstimatedInput        uint64 `json:"estimated_input_tokens,omitempty"`
	EstimatedError        uint64 `json:"estimated_error_tokens,omitempty"`
	TokenizedInput        uint64 `json:"tokenized_input_tokens,omitempty"`
	TokenizedError        uint64 `json:"tokenized_error_tokens,omitempty"`
	TokenizedShareUnknown bool   `json:"tokenized_unknown,omitempty"`
	MaxTokens             uint64 `json:"max_tokens,omitempty"`
	Input                 uint64 `json:"input_tokens,omitempty"`
	CountedNonces         uint64 `json:"counted_nonces,omitempty"`
}

func (m *SlotMoney) add(other SlotMoney) {
	m.Reserved += other.Reserved
	m.Actual += other.Actual
	m.Refunded += other.Refunded
	m.EstimatedInput += other.EstimatedInput
	m.EstimatedError += other.EstimatedError
	m.TokenizedInput += other.TokenizedInput
	m.TokenizedError += other.TokenizedError
	m.TokenizedShareUnknown = m.TokenizedShareUnknown || other.TokenizedShareUnknown
	m.MaxTokens += other.MaxTokens
	m.Input += other.Input
	m.CountedNonces += other.CountedNonces
}

func (c nonceCost) refunded() uint64 {
	switch c.status {
	case types.StatusTimedOut, types.StatusInvalidated:
		return c.reserved
	case types.StatusPending, types.StatusStarted:
		return 0
	}
	if c.actual > c.reserved {
		return 0
	}
	return c.reserved - c.actual
}

// foldMoney gives every slot its share of the escrow: what the chain says now, over the fold last saved for it.
func (e *escrowLedger) foldMoney() []SlotMoney {
	money := make([]SlotMoney, len(e.metadata.Slots))
	for nonce, cost := range e.costs {
		e.foldCost(&money[e.slotOf(nonce)], nonce, cost)
	}
	for slotID, saved := range e.folded {
		if int(slotID) < len(money) {
			money[slotID].add(saved)
		}
	}
	return money
}

// addProduced counts an attempt's tokens once, on the slot that produced them. See docs/accounting.md, "Money and tokens".
func (e *escrowLedger) addProduced(nonce uint64, record *nonceRecord, tokens int64) {
	if record.outputAdded || tokens <= 0 {
		return
	}
	record.outputAdded = true
	e.produced[e.slotOf(nonce)] += uint64(tokens)
}

func (e *escrowLedger) foldCost(money *SlotMoney, nonce uint64, cost nonceCost) {
	money.Reserved += cost.reserved
	money.Actual += cost.actual
	money.Refunded += cost.refunded()
	if e.isGhost(nonce) {
		return
	}
	tokens, tokenized := e.estimatedInput(nonce, cost)
	if cost.status != types.StatusFinished && cost.status != types.StatusValidated {
		money.EstimatedError += tokens
		if tokenized {
			money.TokenizedError += tokens
		}
		return
	}
	money.CountedNonces++
	money.Input += cost.input
	money.EstimatedInput += tokens
	if tokenized {
		money.TokenizedInput += tokens
	}
	money.MaxTokens += cost.maxTokens
}

func costOf(record *types.InferenceRecord) nonceCost {
	return nonceCost{
		reserved:    record.ReservedCost,
		actual:      record.ActualCost,
		inputLength: record.InputLength,
		maxTokens:   record.MaxTokens,
		input:       record.InputTokens,
		output:      record.OutputTokens,
		status:      record.Status,
	}
}

// NoncesToFold lists the nonces whose cost an earlier reading left and the escrow no longer holds live: the ones the next reading folds.
func (b *Book) NoncesToFold(escrowID string, inferences map[uint64]*types.InferenceRecord) []uint64 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	escrow, known := b.escrows[escrowID]
	if !known {
		return nil
	}
	var leaving []uint64
	for nonce := range escrow.costs {
		if _, live := inferences[nonce]; !live {
			leaving = append(leaving, nonce)
		}
	}
	return leaving
}

// ObserveSealedRecords replaces each cost still held with the record its escrow kept at seal, and adds none. See docs/accounting.md, "Money and tokens".
func (b *Book) ObserveSealedRecords(escrowID string, records map[uint64]types.InferenceRecord) error {
	if len(records) == 0 {
		return nil
	}
	return b.withEscrow(escrowID, func(escrow *escrowLedger) error {
		for nonce, record := range records {
			if _, held := escrow.costs[nonce]; held {
				escrow.costs[nonce] = costOf(&record)
			}
		}
		return nil
	})
}
