package escrow

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Test flow:
//  1. Call NewManager with an empty Deps.
//  2. Assert the returned error names the missing "Config" field.
func TestNewManagerNamesTheDependencyItWasNotGiven(t *testing.T) {
	_, err := NewManager(Deps{})

	require.ErrorContains(t, err, "Config")
}
