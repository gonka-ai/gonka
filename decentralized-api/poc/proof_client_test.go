package poc

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateLeafCoverage_ExactMatch(t *testing.T) {
	requested := []uint32{0, 5, 10}
	proofs := []ProofItem{
		{LeafIndex: 0},
		{LeafIndex: 5},
		{LeafIndex: 10},
	}
	assert.NoError(t, validateLeafCoverage(requested, proofs))
}

func TestValidateLeafCoverage_OrderIndependent(t *testing.T) {
	requested := []uint32{0, 5, 10}
	proofs := []ProofItem{
		{LeafIndex: 10},
		{LeafIndex: 0},
		{LeafIndex: 5},
	}
	assert.NoError(t, validateLeafCoverage(requested, proofs))
}

func TestValidateLeafCoverage_FewerProofs(t *testing.T) {
	requested := []uint32{0, 5, 10}
	proofs := []ProofItem{
		{LeafIndex: 0},
		{LeafIndex: 5},
	}
	err := validateLeafCoverage(requested, proofs)
	assert.True(t, errors.Is(err, ErrIncompleteCoverage))
	assert.Contains(t, err.Error(), "expected 3 proofs, got 2")
}

func TestValidateLeafCoverage_ExtraProofs(t *testing.T) {
	requested := []uint32{0, 5}
	proofs := []ProofItem{
		{LeafIndex: 0},
		{LeafIndex: 5},
		{LeafIndex: 10},
	}
	err := validateLeafCoverage(requested, proofs)
	assert.True(t, errors.Is(err, ErrIncompleteCoverage))
	assert.Contains(t, err.Error(), "expected 2 proofs, got 3")
}

func TestValidateLeafCoverage_DuplicateLeafIndex(t *testing.T) {
	requested := []uint32{0, 5, 10}
	proofs := []ProofItem{
		{LeafIndex: 0},
		{LeafIndex: 5},
		{LeafIndex: 5}, // duplicate
	}
	err := validateLeafCoverage(requested, proofs)
	assert.True(t, errors.Is(err, ErrIncompleteCoverage))
	assert.Contains(t, err.Error(), "duplicate leaf index 5")
}

func TestValidateLeafCoverage_UnexpectedLeafIndex(t *testing.T) {
	requested := []uint32{0, 5, 10}
	proofs := []ProofItem{
		{LeafIndex: 0},
		{LeafIndex: 5},
		{LeafIndex: 99}, // not requested
	}
	err := validateLeafCoverage(requested, proofs)
	assert.True(t, errors.Is(err, ErrIncompleteCoverage))
	assert.Contains(t, err.Error(), "unexpected leaf index 99")
}

func TestValidateLeafCoverage_EmptyBoth(t *testing.T) {
	assert.NoError(t, validateLeafCoverage(nil, nil))
	assert.NoError(t, validateLeafCoverage([]uint32{}, []ProofItem{}))
}

func TestValidateLeafCoverage_EmptyRequestNonEmptyProofs(t *testing.T) {
	err := validateLeafCoverage(nil, []ProofItem{{LeafIndex: 0}})
	assert.True(t, errors.Is(err, ErrIncompleteCoverage))
}

func TestValidateLeafCoverage_SingleLeaf(t *testing.T) {
	assert.NoError(t, validateLeafCoverage([]uint32{42}, []ProofItem{{LeafIndex: 42}}))
}

func TestCheckDuplicateNonces_NoDuplicates(t *testing.T) {
	artifacts := []VerifiedArtifact{
		{Nonce: 1},
		{Nonce: 2},
		{Nonce: 3},
	}
	assert.NoError(t, CheckDuplicateNonces(artifacts))
}

func TestCheckDuplicateNonces_WithDuplicates(t *testing.T) {
	artifacts := []VerifiedArtifact{
		{Nonce: 1},
		{Nonce: 2},
		{Nonce: 1}, // duplicate
	}
	assert.True(t, errors.Is(CheckDuplicateNonces(artifacts), ErrDuplicateNonces))
}

func TestCheckDuplicateNonces_Empty(t *testing.T) {
	assert.NoError(t, CheckDuplicateNonces(nil))
	assert.NoError(t, CheckDuplicateNonces([]VerifiedArtifact{}))
}

func TestCheckDuplicateNonces_Single(t *testing.T) {
	assert.NoError(t, CheckDuplicateNonces([]VerifiedArtifact{{Nonce: 42}}))
}

func TestCheckDuplicateNonces_NegativeNonces(t *testing.T) {
	artifacts := []VerifiedArtifact{
		{Nonce: -1},
		{Nonce: -2},
		{Nonce: 0},
	}
	assert.NoError(t, CheckDuplicateNonces(artifacts))
}

func TestCheckDuplicateNonces_NegativeDuplicates(t *testing.T) {
	artifacts := []VerifiedArtifact{
		{Nonce: -1},
		{Nonce: -1},
	}
	assert.True(t, errors.Is(CheckDuplicateNonces(artifacts), ErrDuplicateNonces))
}
