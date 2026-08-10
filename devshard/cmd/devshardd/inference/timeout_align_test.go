package inference

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

func TestExecutionDrainTimeoutAlignsWithProtocol(t *testing.T) {
	require.Equal(t, types.DefaultExecutionTimeout(), executionDrainTimeout)
	require.Equal(t, types.DefaultExecutionTimeout(), mlNodeHTTPTimeout)
}
