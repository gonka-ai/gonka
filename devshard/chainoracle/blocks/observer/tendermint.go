package observer

import "context"

// TendermintConfig pins the real-chain observer's runtime parameters.
// Only the fields required to open a Tendermint RPC client are placed
// here; higher-level wiring (validator-set refresh cadence, commit
// selection strategy) is deferred until the real observer lands.
type TendermintConfig struct {
	ChainID    string
	RPCURL     string
	PollPeriod string // e.g. "500ms"
}

// NewTendermint is the production observer that streams commits from a
// real Tendermint RPC endpoint. Implementation is deferred to the
// follow-up PR that lands alongside the real decentralized-api wiring
// (see devshard/docs/testenv.md Phase 5).
//
// The constructor is present so compile-time references in real dapi
// do not regress as testenv code lands first.
func NewTendermint(_ context.Context, _ TendermintConfig) (Observer, error) {
	panic("observer: NewTendermint is not implemented yet; see devshard/docs/testenv.md Phase 5")
}
