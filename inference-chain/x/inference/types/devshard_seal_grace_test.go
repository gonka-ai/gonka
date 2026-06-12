package types_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/inference/types"
)

// Tests for the devshard runtime seal-grace knob, which lives on
// DevshardEscrowParams.DefaultInferenceSealGraceNonces. Settlement does not depend
// on this value (it is a runtime-only RAM-eviction knob, not part of the
// state root), but governance can tune it via MsgUpdateParams.

func TestDefault_DevshardEscrowParams_HasInferenceSealGraceNonces(t *testing.T) {
	p := types.DefaultDevshardEscrowParams()
	require.NotNil(t, p)
	require.Equal(t,
		types.DefaultDevshardInferenceSealGraceNonces(types.DefaultDevshardGroupSize),
		p.DefaultInferenceSealGraceNonces,
		"default seal grace must derive from default group size",
	)
}

func TestDefaultParams_IncludesInferenceSealGraceNonces(t *testing.T) {
	p := types.DefaultParams()
	require.NotNil(t, p.DevshardEscrowParams,
		"DefaultParams must include DevshardEscrowParams")
	require.Equal(t,
		types.DefaultDevshardInferenceSealGraceNonces(types.DefaultDevshardGroupSize),
		p.DevshardEscrowParams.DefaultInferenceSealGraceNonces,
	)
	require.NoError(t, p.Validate())
}

func TestDefaultDevshardInferenceSealGraceNonces_FloorAndScaling(t *testing.T) {
	require.Equal(t, types.DevshardSealGraceFloor, types.DefaultDevshardInferenceSealGraceNonces(0),
		"zero group must clamp to floor")
	require.Equal(t, types.DevshardSealGraceFloor, types.DefaultDevshardInferenceSealGraceNonces(1),
		"tiny group must clamp to floor")
	require.Equal(t, uint32(160), types.DefaultDevshardInferenceSealGraceNonces(16),
		"default group of 16 must produce 160 (10 * groupSize)")
	require.Equal(t, uint32(1000), types.DefaultDevshardInferenceSealGraceNonces(100),
		"large groups must scale linearly")
}

func TestDevshardAutoSealEveryNNoncesForCreate(t *testing.T) {
	require.Equal(t, types.DefaultDevshardAutoSealEveryNNonces,
		types.DevshardAutoSealEveryNNoncesForCreate(types.DefaultDevshardEscrowParams()))

	custom := types.DefaultDevshardEscrowParams()
	custom.DefaultAutoSealEveryNNonces = 16
	require.Equal(t, uint32(16), types.DevshardAutoSealEveryNNoncesForCreate(custom))

	zero := types.DefaultDevshardEscrowParams()
	zero.DefaultAutoSealEveryNNonces = 0
	require.Equal(t, types.DefaultDevshardAutoSealEveryNNonces,
		types.DevshardAutoSealEveryNNoncesForCreate(zero))
}
