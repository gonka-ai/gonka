package settlement

import "fmt"

const (
	quorumNumerator   = 2
	quorumDenominator = 3
)

// SignatureSource is a session's settlement signatures at a nonce, re-checked now or as held; *user.Session satisfies it.
type SignatureSource interface {
	SettlementSignatures(nonce uint64) (map[uint32][]byte, error)
	SignaturesAt(nonce uint64) map[uint32][]byte
}

// Signatures is the signature set a settle posts, with the re-check that fell short when the set is posted unchecked.
type Signatures struct {
	BySlot    map[uint32][]byte
	Shortfall error
}

func quorum(slotCount int) int {
	return quorumNumerator*slotCount/quorumDenominator + 1
}

// ChooseSignatures picks the signatures a settle at nonce posts for an escrow of slotCount slots and errors when it must post nothing. See README.md.
func ChooseSignatures(source SignatureSource, nonce uint64, slotCount int) (Signatures, error) {
	checked, shortfall := source.SettlementSignatures(nonce)
	if shortfall == nil {
		return Signatures{BySlot: checked}, nil
	}
	held := source.SignaturesAt(nonce)
	if len(held) < quorum(slotCount) {
		return Signatures{}, fmt.Errorf("%d of %d slots signed, %d needed: %w", len(held), slotCount, quorum(slotCount), shortfall)
	}
	return Signatures{BySlot: held, Shortfall: shortfall}, nil
}
