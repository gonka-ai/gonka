package accounting

// DiffFactKind names what a composed diff told the ledger.
type DiffFactKind uint8

const (
	DiffFactValidation DiffFactKind = iota + 1
	DiffFactInvalidVerdict
	DiffFactAppliedTimeout
	DiffFactServiceNonce
)

// DiffFact is one ledger fact read off a composed diff, a copy so nothing of the session crosses goroutines.
type DiffFact struct {
	Kind          DiffFactKind
	Nonce         uint64
	ValidatorSlot uint32
	Purpose       ServicePurpose
}
