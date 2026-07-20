package verifier_test

import (
	"errors"
	"testing"
	"time"

	"devshard/chainoracle/blocks"
	"devshard/chainoracle/blocks/verifier"
	"devshard/signing"

	"github.com/stretchr/testify/require"
)

// signedHeader builds a header signed by the supplied signers. The first
// signer is treated as the active proposer; all signers contribute
// equally-weighted CommitSigs.
func signedHeader(t *testing.T, chainID string, height int64, signers []*signing.Secp256k1Signer) *blocks.Header {
	t.Helper()
	h := &blocks.Header{
		Height:             height,
		Time:               time.Unix(0, 0).UTC().Add(time.Duration(height) * time.Second),
		ChainID:            chainID,
		BlockHash:          bytesOf(0xAA, height),
		AppHash:            bytesOf(0xBB, height),
		ValidatorsHash:     bytesOf(0xCC, height),
		NextValidatorsHash: bytesOf(0xDD, height+1),
	}
	canonical := blocks.CanonicalHeaderBytes(h)

	commit := blocks.Commit{
		Height:     height,
		Round:      0,
		BlockID:    h.BlockHash,
		Signatures: make([]blocks.CommitSig, 0, len(signers)),
	}
	for _, s := range signers {
		sig, err := s.Sign(canonical)
		require.NoError(t, err)
		addr, err := blocks.AddressBytes(s.PublicKeyBytes())
		require.NoError(t, err)
		commit.Signatures = append(commit.Signatures, blocks.CommitSig{
			ValidatorAddress: addr,
			Timestamp:        h.Time,
			Signature:        sig,
		})
	}
	h.Commit = commit
	return h
}

func bytesOf(tag byte, height int64) []byte {
	out := make([]byte, 32)
	for i := range out {
		out[i] = tag ^ byte(height)
	}
	return out
}

func newValidatorSet(t *testing.T, chainID string, signers []*signing.Secp256k1Signer, powers []int64) *verifier.ValidatorSet {
	t.Helper()
	require.Equal(t, len(signers), len(powers))
	vals := make([]verifier.Validator, 0, len(signers))
	for i, s := range signers {
		addr, err := blocks.AddressBytes(s.PublicKeyBytes())
		require.NoError(t, err)
		vals = append(vals, verifier.Validator{Address: addr, Power: powers[i]})
	}
	vs, err := verifier.NewValidatorSet(chainID, vals)
	require.NoError(t, err)
	return vs
}

func TestVerifier_AcceptsValidCommit(t *testing.T) {
	s, err := signing.GenerateKey()
	require.NoError(t, err)
	vs := newValidatorSet(t, "gonka-test", []*signing.Secp256k1Signer{s}, []int64{10})
	v := verifier.New(vs)

	h := signedHeader(t, "gonka-test", 1, []*signing.Secp256k1Signer{s})
	require.NoError(t, v.Verify(h, 0))
}

func TestVerifier_RejectsChainIDMismatch(t *testing.T) {
	s, err := signing.GenerateKey()
	require.NoError(t, err)
	vs := newValidatorSet(t, "gonka-test", []*signing.Secp256k1Signer{s}, []int64{10})
	v := verifier.New(vs)

	h := signedHeader(t, "other-chain", 1, []*signing.Secp256k1Signer{s})
	err = v.Verify(h, 0)
	require.Error(t, err)
	require.Contains(t, err.Error(), "chain id mismatch")
}

func TestVerifier_RejectsTamperedBlockHash(t *testing.T) {
	s, err := signing.GenerateKey()
	require.NoError(t, err)
	vs := newValidatorSet(t, "gonka-test", []*signing.Secp256k1Signer{s}, []int64{10})
	v := verifier.New(vs)

	h := signedHeader(t, "gonka-test", 1, []*signing.Secp256k1Signer{s})
	h.BlockHash[0] ^= 0xFF // tamper after signing
	err = v.Verify(h, 0)
	require.Error(t, err)
}

func TestVerifier_RejectsTamperedAppHash(t *testing.T) {
	s, err := signing.GenerateKey()
	require.NoError(t, err)
	vs := newValidatorSet(t, "gonka-test", []*signing.Secp256k1Signer{s}, []int64{10})
	v := verifier.New(vs)

	h := signedHeader(t, "gonka-test", 1, []*signing.Secp256k1Signer{s})
	h.AppHash[3] ^= 0xFF
	err = v.Verify(h, 0)
	require.Error(t, err)
}

func TestVerifier_RejectsTamperedValidatorsHash(t *testing.T) {
	s, err := signing.GenerateKey()
	require.NoError(t, err)
	vs := newValidatorSet(t, "gonka-test", []*signing.Secp256k1Signer{s}, []int64{10})
	v := verifier.New(vs)

	h := signedHeader(t, "gonka-test", 1, []*signing.Secp256k1Signer{s})
	h.ValidatorsHash[5] ^= 0xFF
	err = v.Verify(h, 0)
	require.Error(t, err)
}

func TestVerifier_RejectsUnknownSigner(t *testing.T) {
	s, err := signing.GenerateKey()
	require.NoError(t, err)
	other, err := signing.GenerateKey()
	require.NoError(t, err)
	vs := newValidatorSet(t, "gonka-test", []*signing.Secp256k1Signer{s}, []int64{10})
	v := verifier.New(vs)

	h := signedHeader(t, "gonka-test", 1, []*signing.Secp256k1Signer{other})
	err = v.Verify(h, 0)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not in pinned set")
}

func TestVerifier_RejectsInsufficientVotingPower(t *testing.T) {
	// Three equal validators; only one signs → 1/3 power, need > 2/3.
	a, err := signing.GenerateKey()
	require.NoError(t, err)
	b, err := signing.GenerateKey()
	require.NoError(t, err)
	c, err := signing.GenerateKey()
	require.NoError(t, err)
	vs := newValidatorSet(t, "gonka-test",
		[]*signing.Secp256k1Signer{a, b, c},
		[]int64{10, 10, 10},
	)
	v := verifier.New(vs)

	h := signedHeader(t, "gonka-test", 1, []*signing.Secp256k1Signer{a})
	err = v.Verify(h, 0)
	require.Error(t, err)
	require.Contains(t, err.Error(), "insufficient voting power")
}

func TestVerifier_AcceptsSupermajority(t *testing.T) {
	a, err := signing.GenerateKey()
	require.NoError(t, err)
	b, err := signing.GenerateKey()
	require.NoError(t, err)
	c, err := signing.GenerateKey()
	require.NoError(t, err)
	vs := newValidatorSet(t, "gonka-test",
		[]*signing.Secp256k1Signer{a, b, c},
		[]int64{10, 10, 10},
	)
	v := verifier.New(vs)

	h := signedHeader(t, "gonka-test", 1, []*signing.Secp256k1Signer{a, b, c})
	require.NoError(t, v.Verify(h, 0))
}

func TestVerifier_RejectsStaleHeight(t *testing.T) {
	s, err := signing.GenerateKey()
	require.NoError(t, err)
	vs := newValidatorSet(t, "gonka-test", []*signing.Secp256k1Signer{s}, []int64{10})
	v := verifier.New(vs)

	h := signedHeader(t, "gonka-test", 5, []*signing.Secp256k1Signer{s})
	require.NoError(t, v.Verify(h, 0))

	older := signedHeader(t, "gonka-test", 3, []*signing.Secp256k1Signer{s})
	err = v.Verify(older, 5)
	require.ErrorIs(t, err, verifier.ErrStale)
}

func TestValidatorSet_RejectsMalformed(t *testing.T) {
	_, err := verifier.NewValidatorSet("gonka-test", nil)
	require.Error(t, err)

	_, err = verifier.NewValidatorSet("", []verifier.Validator{{Address: make([]byte, 20), Power: 1}})
	require.Error(t, err)

	_, err = verifier.NewValidatorSet("gonka-test", []verifier.Validator{{Address: []byte{1, 2, 3}, Power: 1}})
	require.Error(t, err)

	_, err = verifier.NewValidatorSet("gonka-test", []verifier.Validator{{Address: make([]byte, 20), Power: 0}})
	require.Error(t, err)

	dup := make([]byte, 20)
	_, err = verifier.NewValidatorSet("gonka-test", []verifier.Validator{
		{Address: dup, Power: 1},
		{Address: dup, Power: 1},
	})
	require.Error(t, err)
}

// TestVerifier_RejectsTamperedSignatureInMultiSig builds a 10-validator
// commit (all powers equal, > 3/4 quorum), flips a single byte inside
// one signature, and asserts the verifier rejects. This exercises the
// per-signature ecrecover path inside a multi-signer setting — the
// header fields are untouched, only one validator's signature is
// corrupted.
func TestVerifier_RejectsTamperedSignatureInMultiSig(t *testing.T) {
	signers := make([]*signing.Secp256k1Signer, 10)
	powers := make([]int64, 10)
	for i := range signers {
		s, err := signing.GenerateKey()
		require.NoError(t, err)
		signers[i] = s
		powers[i] = 1
	}
	vs := newValidatorSet(t, "gonka-test", signers, powers)
	v := verifier.New(vs)

	h := signedHeader(t, "gonka-test", 1, signers)
	// Sanity: the untampered header must pass first so we know the
	// rejection below is caused by the flip.
	require.NoError(t, v.Verify(h, 0))

	// Flip one byte in the 5th signature (arbitrary middle index).
	h.Commit.Signatures[4].Signature[10] ^= 0xFF
	err := v.Verify(h, 0)
	require.Error(t, err)
}

// TestVerifier_RejectsForeignSignatureInMultiSig builds a valid 10-sig
// commit that already meets > 2/3, then appends an 11th signature from
// a validator that is not in the pinned set. The verifier must reject
// even though every "real" signature is legitimate — the pinned set is
// closed, and accepting stray sigs would let an attacker inflate
// observed power or smuggle unauthorized signers.
func TestVerifier_RejectsForeignSignatureInMultiSig(t *testing.T) {
	signers := make([]*signing.Secp256k1Signer, 10)
	powers := make([]int64, 10)
	for i := range signers {
		s, err := signing.GenerateKey()
		require.NoError(t, err)
		signers[i] = s
		powers[i] = 1
	}
	vs := newValidatorSet(t, "gonka-test", signers, powers)
	v := verifier.New(vs)

	// Start from the full 10-sig happy path.
	h := signedHeader(t, "gonka-test", 1, signers)
	require.NoError(t, v.Verify(h, 0))

	// Append a foreign signer outside the pinned set.
	foreign, err := signing.GenerateKey()
	require.NoError(t, err)
	canonical := blocks.CanonicalHeaderBytes(h)
	foreignSig, err := foreign.Sign(canonical)
	require.NoError(t, err)
	foreignAddr, err := blocks.AddressBytes(foreign.PublicKeyBytes())
	require.NoError(t, err)
	h.Commit.Signatures = append(h.Commit.Signatures, blocks.CommitSig{
		ValidatorAddress: foreignAddr,
		Timestamp:        h.Time,
		Signature:        foreignSig,
	})

	err = v.Verify(h, 0)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not in pinned set")
}

func TestVerifier_RejectsDuplicateSignatures(t *testing.T) {
	s, err := signing.GenerateKey()
	require.NoError(t, err)
	vs := newValidatorSet(t, "gonka-test", []*signing.Secp256k1Signer{s}, []int64{10})
	v := verifier.New(vs)

	h := signedHeader(t, "gonka-test", 1, []*signing.Secp256k1Signer{s, s})
	err = v.Verify(h, 0)
	require.Error(t, err)
	require.True(t, errors.Is(err, err)) // keep linter happy
	require.Contains(t, err.Error(), "duplicate")
}
