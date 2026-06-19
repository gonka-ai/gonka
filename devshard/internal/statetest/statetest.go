// Package statetest provides test helpers for state.StateMachine without
// creating an import cycle through devshard/internal/testutil.
package statetest

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/types"
)

// MustStateMachine creates a StateMachine backed by an in-memory store for tests.
func MustStateMachine(
	t *testing.T,
	escrowID string,
	config types.SessionConfig,
	group []types.SlotAssignment,
	balance uint64,
	userAddr string,
	verifier signing.Verifier,
	opts ...state.SMOption,
) *state.StateMachine {
	t.Helper()
	sm, err := state.NewStateMachine(
		escrowID, config, group, balance, userAddr, verifier,
		testutil.MustMemoryStore(t, escrowID, userAddr, config, group, balance),
		opts...,
	)
	require.NoError(t, err)
	return sm
}
