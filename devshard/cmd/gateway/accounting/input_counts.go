package accounting

import (
	"cmp"
	"slices"

	"devshard/cmd/gateway/filters"
)

type inputCount struct {
	tokens uint64
	source filters.InputTokensSource
}

// PersistedInputCount is the gateway's tokenizer count of one nonce's prompt, held until the nonce's cost is sealed. See docs/accounting.md, "Money and tokens".
type PersistedInputCount struct {
	Nonce  uint64                    `json:"nonce"`
	Tokens uint64                    `json:"tokens"`
	Source filters.InputTokensSource `json:"source"`
}

// RecordInputCount files the gateway's count of an assigned nonce's prompt, kept only when a tokenizer made it. See docs/accounting.md, "Money and tokens".
func (b *Book) RecordInputCount(escrowID string, nonce, tokens uint64, source filters.InputTokensSource) error {
	return b.withEscrow(escrowID, func(escrow *escrowLedger) error {
		escrow.recordInputCount(nonce, tokens, source)
		return nil
	})
}

func (e *escrowLedger) recordInputCount(nonce, tokens uint64, source filters.InputTokensSource) {
	if !source.Tokenized() || e.isGhost(nonce) || e.sealedBySweep(nonce) {
		return
	}
	e.inputCounts[nonce] = inputCount{tokens: tokens, source: source}
}

func (e *escrowLedger) dropSealedInputCounts() (dropped bool) {
	for nonce := range e.inputCounts {
		if e.sealedBySweep(nonce) {
			delete(e.inputCounts, nonce)
			dropped = true
		}
	}
	return dropped
}

func (e *escrowLedger) sealedBySweep(nonce uint64) bool {
	_, live := e.costs[nonce]
	return !live && nonce <= e.appliedSweepWatermark
}

func (e *escrowLedger) estimatedInput(nonce uint64, cost nonceCost) (tokens uint64, tokenized bool) {
	if counted, known := e.inputCounts[nonce]; known {
		return counted.tokens, true
	}
	return filters.EstimatedPromptTokens(int(cost.inputLength)), false
}

func (e *escrowLedger) persistedInputCounts() []PersistedInputCount {
	counts := make([]PersistedInputCount, 0, len(e.inputCounts))
	for nonce, counted := range e.inputCounts {
		counts = append(counts, PersistedInputCount{Nonce: nonce, Tokens: counted.tokens, Source: counted.source})
	}
	slices.SortFunc(counts, func(left, right PersistedInputCount) int { return cmp.Compare(left.Nonce, right.Nonce) })
	return counts
}

func (e *escrowLedger) restoreInputCounts(counts []PersistedInputCount) {
	for _, counted := range counts {
		e.inputCounts[counted.Nonce] = inputCount{tokens: counted.Tokens, source: counted.Source}
	}
}
