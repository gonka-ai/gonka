package types_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/inference/types"
)

// Tests for the devshard runtime seal-grace knob, which lives on
// DevshardEscrowParams.DefaultSealGraceNonces. Settlement does not depend
// on this value (it is a runtime-only RAM-eviction knob, not part of the
// state root), but governance can tune it via MsgUpdateParams.

func TestDefault_DevshardEscrowParams_HasSealGraceNonces(t *testing.T) {
	p := types.DefaultDevshardEscrowParams()
	require.NotNil(t, p)
	require.Equal(t,
		types.DefaultDevshardSealGraceNonces(types.DefaultDevshardGroupSize),
		p.DefaultSealGraceNonces,
		"default seal grace must derive from default group size",
	)
}

func TestDefaultParams_IncludesSealGraceNonces(t *testing.T) {
	p := types.DefaultParams()
	require.NotNil(t, p.DevshardEscrowParams,
		"DefaultParams must include DevshardEscrowParams")
	require.Equal(t,
		types.DefaultDevshardSealGraceNonces(types.DefaultDevshardGroupSize),
		p.DevshardEscrowParams.DefaultSealGraceNonces,
	)
	require.NoError(t, p.Validate())
}

func TestDefaultDevshardSealGraceNonces_FloorAndScaling(t *testing.T) {
	require.Equal(t, types.DevshardSealGraceFloor, types.DefaultDevshardSealGraceNonces(0),
		"zero group must clamp to floor")
	require.Equal(t, types.DevshardSealGraceFloor, types.DefaultDevshardSealGraceNonces(1),
		"tiny group must clamp to floor")
	require.Equal(t, uint32(160), types.DefaultDevshardSealGraceNonces(16),
		"default group of 16 must produce 160 (10 * groupSize)")
	require.Equal(t, uint32(1000), types.DefaultDevshardSealGraceNonces(100),
		"large groups must scale linearly")
}
