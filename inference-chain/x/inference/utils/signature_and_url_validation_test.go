package utils

import (
	"testing"

	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/stretchr/testify/require"
)

func TestValidateURLWithSSRFProtection(t *testing.T) {
	t.Run("valid_public_url", func(t *testing.T) {
		err := ValidateURLWithSSRFProtection("inference_url", "https://example.com")
		require.NoError(t, err)
	})

	t.Run("reject_localhost", func(t *testing.T) {
		err := ValidateURLWithSSRFProtection("inference_url", "http://localhost:8080")
		require.ErrorIs(t, err, sdkerrors.ErrInvalidRequest)
	})

	t.Run("reject_private_ipv4", func(t *testing.T) {
		err := ValidateURLWithSSRFProtection("inference_url", "http://192.168.0.1")
		require.ErrorIs(t, err, sdkerrors.ErrInvalidRequest)
	})
}
