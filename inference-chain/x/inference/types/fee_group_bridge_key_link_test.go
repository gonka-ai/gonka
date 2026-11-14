package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A key link is a user action with an unmetered ECDSA recovery; it must not be free.
func TestRegisterBridgeKeyLinkIsPaid(t *testing.T) {
	require.Equal(t, FeeGroupCosmos, FeeGroupOf(&MsgRegisterBridgeKeyLink{}))
}
