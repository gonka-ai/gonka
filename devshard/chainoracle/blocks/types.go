package blocks

import "time"

// Header is the authenticated mainnet block header exposed to consumers.
//
// The wire shape (see §3.3 in devshard/docs/testenv.md) is intentionally a
// superset of what devshardd currently needs so future consumers (e.g.
// settlement proofs, validator-set rotation) can use the same transport.
type Header struct {
	Height             int64
	Time               time.Time
	ChainID            string
	BlockHash          []byte
	AppHash            []byte
	ValidatorsHash     []byte
	NextValidatorsHash []byte
	Commit             Commit
}

// Commit aggregates validator signatures over a block.
type Commit struct {
	Height     int64
	Round      int32
	BlockID    []byte
	Signatures []CommitSig
}

// CommitSig is a single validator's signature inside a Commit.
type CommitSig struct {
	ValidatorAddress []byte
	Timestamp        time.Time
	Signature        []byte
}

// Proof is an IAVL Merkle proof over a specific key path at a given height.
type Proof struct {
	Path  string
	Value []byte
	Ops   [][]byte
}
