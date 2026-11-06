package transport

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTokenAdmitThreshold(t *testing.T) {
	require.Equal(t, 0.0, TokenAdmitThreshold(0, 10))
	require.Equal(t, 1.0, TokenAdmitThreshold(1, 10))
	require.Equal(t, 60.0, TokenAdmitThreshold(60, 600))
	require.Equal(t, 10.0, TokenAdmitThreshold(60, 10), "GetDiffs overdrafts from a 10-token pulse")
	require.Equal(t, 1.0, TokenAdmitThreshold(60, 0), "burst below 1 still admits from a full 1-token bucket")
}
