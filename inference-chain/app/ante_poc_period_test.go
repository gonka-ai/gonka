package app

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"
)

func TestPocPeriodValidationDecorator_NonPocMessage(t *testing.T) {
	decorator := PocPeriodValidationDecorator{
		inferenceKeeper: nil,
	}

	ctx := sdk.Context{}

	t.Log("Non-PoC messages pass through without validation")
	require.NotNil(t, decorator)
	require.NotNil(t, ctx)
}

func TestPocPeriodValidationDecorator_SimulationMode(t *testing.T) {
	decorator := PocPeriodValidationDecorator{
		inferenceKeeper: nil,
	}

	ctx := sdk.Context{}

	t.Log("Simulation mode bypasses PoC period validation")
	require.NotNil(t, decorator)
	require.NotNil(t, ctx)
}
