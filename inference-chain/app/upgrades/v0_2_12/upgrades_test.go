package v0_2_12

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpgradeName(t *testing.T) {
	require.Equal(t, "v0.2.12", UpgradeName)
}
