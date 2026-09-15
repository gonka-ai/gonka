package public

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewChainOracle_Disabled(t *testing.T) {
	t.Setenv("DAPI_CHAINORACLE_DISABLED", "1")
	o, err := NewChainOracle("http://127.0.0.1:26657")
	require.NoError(t, err)
	require.Nil(t, o)
}

func TestNewChainOracle_EmptyURL(t *testing.T) {
	t.Setenv("DAPI_CHAINORACLE_DISABLED", "")
	o, err := NewChainOracle("")
	require.NoError(t, err)
	require.Nil(t, o)
}
