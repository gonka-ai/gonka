package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDevshardEscrowParams_RoundtripsSessionConfigFields verifies that the
// new governance-configurable session-config fields survive a marshal /
// unmarshal cycle in their protobuf wire representation.
func TestDevshardEscrowParams_RoundtripsSessionConfigFields(t *testing.T) {
	p := &DevshardEscrowParams{
		MinAmount:          1_000_000,
		MaxAmount:          10_000_000,
		MaxEscrowsPerEpoch: 50,
		GroupSize:          16,
		TokenPrice:         3,
		MaxNonce:           50_000,
		RefusalTimeout:     90,
		ExecutionTimeout:   1800,
		ValidationRate:     2500,
		CreateDevshardFee:  25_000,
		FeePerNonce:        500,
	}

	buf, err := p.Marshal()
	require.NoError(t, err)

	var got DevshardEscrowParams
	require.NoError(t, got.Unmarshal(buf))

	assert.Equal(t, int64(90), got.RefusalTimeout)
	assert.Equal(t, int64(1800), got.ExecutionTimeout)
	assert.Equal(t, uint32(2500), got.ValidationRate)
	assert.Equal(t, uint64(3), got.TokenPrice)
	assert.Equal(t, uint64(25_000), got.CreateDevshardFee)
	assert.Equal(t, uint64(500), got.FeePerNonce)
	assert.Equal(t, uint32(50_000), got.MaxNonce)
}

// TestDevshardEscrowParams_ZeroSessionConfigFieldsOmitted verifies proto3
// zero-omission: a message with all session-config fields zero produces the
// same bytes as a message that doesn't have those fields set at all (backward
// compatibility with chains that wrote DevshardEscrowParams before these
// fields existed).
func TestDevshardEscrowParams_ZeroSessionConfigFieldsOmitted(t *testing.T) {
	withZeros := &DevshardEscrowParams{
		MinAmount: 1, MaxAmount: 2, MaxEscrowsPerEpoch: 1, GroupSize: 1, TokenPrice: 1, MaxNonce: 1,
	}
	bytesA, err := withZeros.Marshal()
	require.NoError(t, err)

	pre := &DevshardEscrowParams{
		MinAmount: 1, MaxAmount: 2, MaxEscrowsPerEpoch: 1, GroupSize: 1, TokenPrice: 1, MaxNonce: 1,
	}
	bytesB, err := pre.Marshal()
	require.NoError(t, err)

	assert.Equal(t, bytesB, bytesA, "zero session-config fields must not appear on the wire")
}

// TestDevshardEscrow_RoundtripsSessionConfigFields verifies the same wire-level
// invariant for the per-escrow captured copy of the session-config fields.
func TestDevshardEscrow_RoundtripsSessionConfigFields(t *testing.T) {
	e := &DevshardEscrow{
		Creator:           "inference1abc",
		Amount:            5_000_000_000,
		Slots:             []string{"valA"},
		EpochIndex:        10,
		AppHash:           "deadbeef",
		Settled:           false,
		TokenPrice:        7,
		ModelId:           "Qwen3-235B-A22B-Instruct-2507-FP8",
		MaxNonce:          50_000,
		RefusalTimeout:    90,
		ExecutionTimeout:  1800,
		ValidationRate:    2500,
		CreateDevshardFee: 25_000,
		FeePerNonce:       500,
	}

	buf, err := e.Marshal()
	require.NoError(t, err)

	var got DevshardEscrow
	require.NoError(t, got.Unmarshal(buf))

	assert.Equal(t, uint32(50_000), got.MaxNonce)
	assert.Equal(t, int64(90), got.RefusalTimeout)
	assert.Equal(t, int64(1800), got.ExecutionTimeout)
	assert.Equal(t, uint32(2500), got.ValidationRate)
	assert.Equal(t, uint64(7), got.TokenPrice)
	assert.Equal(t, "Qwen3-235B-A22B-Instruct-2507-FP8", got.ModelId)
	assert.Equal(t, uint64(25_000), got.CreateDevshardFee)
	assert.Equal(t, uint64(500), got.FeePerNonce)
}

// TestDefaultDevshardEscrowParams_PopulatesSessionConfigDefaults verifies that
// the helper returns the canonical compiled defaults for the new fields. These
// defaults must equal the values that devshard/types/DefaultSessionConfig
// uses, preserving the "single source of truth" invariant for new chains.
func TestDefaultDevshardEscrowParams_PopulatesSessionConfigDefaults(t *testing.T) {
	p := DefaultDevshardEscrowParams()

	assert.Equal(t, DefaultDevshardRefusalTimeout, p.RefusalTimeout)
	assert.Equal(t, DefaultDevshardExecutionTimeout, p.ExecutionTimeout)
	assert.Equal(t, DefaultDevshardValidationRate, p.ValidationRate)
	assert.Equal(t, DefaultDevshardCreateDevshardFee, p.CreateDevshardFee)
	assert.Equal(t, DefaultDevshardFeePerNonce, p.FeePerNonce)
}

// TestDevshardEscrowParams_Validate_RejectsNegativeAndOutOfRange ensures the
// Validate hook used by msg-server params updates rejects invalid governance
// settings that would otherwise corrupt SessionConfig.
func TestDevshardEscrowParams_Validate_RejectsNegativeAndOutOfRange(t *testing.T) {
	base := DefaultDevshardEscrowParams()
	require.NoError(t, base.Validate())

	negRefusal := *base
	negRefusal.RefusalTimeout = -1
	assert.Error(t, negRefusal.Validate(), "negative refusal_timeout must be rejected")

	negExec := *base
	negExec.ExecutionTimeout = -1
	assert.Error(t, negExec.Validate(), "negative execution_timeout must be rejected")

	tooHighRate := *base
	tooHighRate.ValidationRate = 10001
	assert.Error(t, tooHighRate.Validate(), "validation_rate > 10000 (basis points) must be rejected")
}
