package bridge

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"devshard/types"
)

func TestSessionConfigAtBind_EscrowLaneA(t *testing.T) {
	const groupSize = 16
	escrow := &EscrowInfo{
		TokenPrice:                1,
		CreateDevshardFee:         10_000,
		FeePerNonce:               1_000,
		InferenceSealGraceNonces:  55,
		InferenceSealGraceSeconds: 77,
		AutoSealEveryNNonces:      16,
		ValidationRate:            6000,
	}

	cfg := SessionConfigAtBind(groupSize, escrow)
	require.Equal(t, uint32(55), cfg.InferenceSealGraceNonces)
	require.Equal(t, uint32(77), cfg.InferenceSealGraceSeconds)
	require.Equal(t, uint32(16), cfg.AutoSealEveryNNonces)
	assert.Equal(t, uint32(6000), cfg.ValidationRate)
}

func TestSessionConfigAtBind_ZeroValidationRateUsesDefault(t *testing.T) {
	const groupSize = 16
	cfg := SessionConfigAtBind(groupSize, &EscrowInfo{TokenPrice: 1})
	assert.Equal(t, types.DefaultValidationRate, cfg.ValidationRate)
}

func TestSessionConfigAtBind_NilEscrowUsesDefaults(t *testing.T) {
	const groupSize = 16
	cfg := SessionConfigAtBind(groupSize, nil)
	assert.Equal(t, types.DefaultValidationRate, cfg.ValidationRate)
}
