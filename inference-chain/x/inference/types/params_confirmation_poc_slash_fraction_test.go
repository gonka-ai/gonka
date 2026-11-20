package types_test

import (
	"testing"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestParamsConfirmationPoCSlashFractionValidation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		fraction *types.Decimal
		wantErr  bool
	}{
		{"unset", nil, false},
		{"zero", types.DecimalFromFloat(0), false},
		{"ten percent", types.DecimalFromFloat(0.1), false},
		{"one", types.DecimalFromFloat(1), false},
		{"negative", types.DecimalFromFloat(-0.1), true},
		{"above one", types.DecimalFromFloat(1.5), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params := types.DefaultParams()
			params.ConfirmationPocParams.SlashFraction = tc.fraction
			err := params.Validate()
			if tc.wantErr {
				require.ErrorContains(t, err, "invalid confirmation_poc_params.slash_fraction")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestParamsWithoutConfirmationPoCParamsStayValid(t *testing.T) {
	params := types.DefaultParams()
	params.ConfirmationPocParams = nil
	require.NoError(t, params.Validate())
}
