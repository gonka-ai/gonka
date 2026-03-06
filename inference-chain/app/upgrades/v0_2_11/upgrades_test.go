package v0_2_11

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpgradeName(t *testing.T) {
	require.Equal(t, "v0.2.11", UpgradeName)
}
