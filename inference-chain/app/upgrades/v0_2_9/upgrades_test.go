package v0_2_9

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpgradeName(t *testing.T) {
	require.Equal(t, "v0.2.9", UpgradeName)
}
