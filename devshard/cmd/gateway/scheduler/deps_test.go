package scheduler

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Test flow:
//  1. Build a scheduler from an empty `Deps` value.
//  2. Assert the constructor returns an error naming "Config" rather than panicking.
func TestNewSchedulerNamesTheDependencyItWasNotGiven(t *testing.T) {
	_, err := NewScheduler(Deps{})

	require.ErrorContains(t, err, "Config")
}
