// Package verifier validates a blocks.Header against a pinned chain
// ID and validator set.
//
// The same verifier runs in every runtime (producer + consumer) so
// tampering is caught at ingest regardless of who served the header.
package verifier

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"devshard/chainoracle/blocks"

	"github.com/ethereum/go-ethereum/crypto"
)

// ErrStale signals that a header is older than the last verified height.
// Consumers may use this to reject out-of-order headers without treating
// them as an integrity violation.
var ErrStale = errors.New("blockoracle: stale header")

// Validator describes one participant of the validator set pinned in a
// consumer. Power must be positive; addresses are the 20-byte form
// produced by blocks.AddressBytes.
type Validator struct {
	Address []byte
	Power   int64
}

// ValidatorSet is the consumer-side pinned set used to verify commit
// signatures. It is immutable after construction.
type ValidatorSet struct {
	ChainID    string
	validators []Validator
	byAddress  map[string]Validator // key = hex(Address)
	totalPower int64
}

// NewValidatorSet copies the input slice and pre-computes the lookup
// index. It returns an error if any validator is malformed or the set is
// empty.
func NewValidatorSet(chainID string, validators []Validator) (*ValidatorSet, error) {
	if chainID == "" {
		return nil, errors.New("validator set: empty chain id")
	}
	if len(validators) == 0 {
		return nil, errors.New("validator set: empty")
	}
	vs := &ValidatorSet{
		ChainID:    chainID,
		validators: make([]Validator, 0, len(validators)),
		byAddress:  make(map[string]Validator, len(validators)),
	}
	for i, v := range validators {
		if len(v.Address) != 20 {
			return nil, fmt.Errorf("validator[%d]: address must be 20 bytes, got %d", i, len(v.Address))
		}
		if v.Power <= 0 {
			return nil, fmt.Errorf("validator[%d]: power must be > 0", i)
		}
		cp := Validator{Address: append([]byte(nil), v.Address...), Power: v.Power}
		key := hex.EncodeToString(cp.Address)
		if _, dup := vs.byAddress[key]; dup {
			return nil, fmt.Errorf("validator[%d]: duplicate address %s", i, key)
		}
		vs.byAddress[key] = cp
		vs.validators = append(vs.validators, cp)
		vs.totalPower += v.Power
	}
	return vs, nil
}

// TotalPower returns the sum of voting power across the pinned validators.
func (vs *ValidatorSet) TotalPower() int64 { return vs.totalPower }

// Len returns the number of pinned validators.
func (vs *ValidatorSet) Len() int { return len(vs.validators) }

// Verifier enforces chain-id equality, commit-signature recovery, and the
// strict > 2/3 voting-power rule. It is safe for concurrent use.
type Verifier struct {
	set *ValidatorSet
}

// New constructs a Verifier bound to a fixed validator set.
func New(set *ValidatorSet) *Verifier { return &Verifier{set: set} }

// Verify enforces all header-integrity invariants:
//
//   - ChainID matches the pinned value.
//   - The canonical digest over header fields is well-formed.
//   - Every CommitSig in header.Commit recovers to a pinned validator and
//     signs the canonical digest.
//   - Accumulated voting power of verified signatures is strictly > 2/3
//     of the pinned total.
//
// Callers that track a "last verified height" should pass lastHeight > 0
// to reject replays; pass 0 on the first call.
func (v *Verifier) Verify(h *blocks.Header, lastHeight int64) error {
	if h == nil {
		return errors.New("verify: nil header")
	}
	if h.ChainID != v.set.ChainID {
		return fmt.Errorf("verify: chain id mismatch: got %q want %q", h.ChainID, v.set.ChainID)
	}
	if h.Height <= 0 {
		return fmt.Errorf("verify: non-positive height %d", h.Height)
	}
	if lastHeight > 0 && h.Height <= lastHeight {
		return fmt.Errorf("%w: got %d, last verified %d", ErrStale, h.Height, lastHeight)
	}
	if h.Commit.Height != h.Height {
		return fmt.Errorf("verify: commit height %d != header height %d", h.Commit.Height, h.Height)
	}
	if len(h.Commit.Signatures) == 0 {
		return errors.New("verify: empty commit")
	}

	canonical := blocks.CanonicalHeaderBytes(h)
	digest := sha256.Sum256(canonical)

	var accumulated int64
	seen := make(map[string]struct{}, len(h.Commit.Signatures))
	for i, sig := range h.Commit.Signatures {
		if len(sig.Signature) != 65 {
			return fmt.Errorf("verify: commit[%d]: signature must be 65 bytes, got %d", i, len(sig.Signature))
		}
		pubkey, err := crypto.Ecrecover(digest[:], sig.Signature)
		if err != nil {
			return fmt.Errorf("verify: commit[%d]: ecrecover: %w", i, err)
		}
		recovered, err := blocks.AddressBytes(pubkey)
		if err != nil {
			return fmt.Errorf("verify: commit[%d]: address: %w", i, err)
		}
		if !bytes.Equal(recovered, sig.ValidatorAddress) {
			return fmt.Errorf("verify: commit[%d]: signer address mismatch: recovered %x, claimed %x",
				i, recovered, sig.ValidatorAddress)
		}
		key := hex.EncodeToString(recovered)
		val, ok := v.set.byAddress[key]
		if !ok {
			return fmt.Errorf("verify: commit[%d]: validator %s not in pinned set", i, key)
		}
		if _, dup := seen[key]; dup {
			return fmt.Errorf("verify: commit[%d]: duplicate signature from %s", i, key)
		}
		seen[key] = struct{}{}
		accumulated += val.Power
	}

	// Strict > 2/3 rule: 3 * accumulated > 2 * total.
	if 3*accumulated <= 2*v.set.totalPower {
		return fmt.Errorf("verify: insufficient voting power: %d of %d (need > 2/3)",
			accumulated, v.set.totalPower)
	}
	return nil
}

// ChainID returns the chain id this verifier is pinned to.
func (v *Verifier) ChainID() string { return v.set.ChainID }
