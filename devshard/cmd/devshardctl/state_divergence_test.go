package main

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func divergenceError() error {
	return errors.New("apply diff nonce 7: post_state_root does not match computed state root")
}

func divergentInflight(env *testProxyEnv, hostIdx int) *inflight {
	return &inflight{
		hostIdx:  hostIdx,
		hostID:   env.session.HostLabel(hostIdx),
		nonce:    7,
		escrowID: env.proxy.redundancy.devshardID,
		err:      divergenceError(),
	}
}

func TestStateDivergence_FirstDisagreementRewindsInsteadOfBlocking(t *testing.T) {
	env := setupTestProxy(t, 3, nil, true)
	env.proxy.redundancy.picker.stop()
	hostIdx := 1
	participantKey := env.proxy.redundancy.participantKeyForHost(hostIdx)

	env.proxy.redundancy.maybeRecordEscrowStateDivergence(context.Background(), divergentInflight(env, hostIdx), divergenceError())

	_, blocked := env.proxy.redundancy.escrowStateBlockReason(participantKey)
	require.False(t, blocked, "a host must get its replay before it is written off")
}

func TestStateDivergence_SecondDisagreementBlocksTheHost(t *testing.T) {
	env := setupTestProxy(t, 3, nil, true)
	env.proxy.redundancy.picker.stop()
	hostIdx := 1
	participantKey := env.proxy.redundancy.participantKeyForHost(hostIdx)

	env.proxy.redundancy.maybeRecordEscrowStateDivergence(context.Background(), divergentInflight(env, hostIdx), divergenceError())
	env.proxy.redundancy.maybeRecordEscrowStateDivergence(context.Background(), divergentInflight(env, hostIdx), divergenceError())

	reason, blocked := env.proxy.redundancy.escrowStateBlockReason(participantKey)
	require.True(t, blocked, "a replay that disagreed again is the evidence the block wanted")
	require.Equal(t, "escrow_state_root_diverged", reason)
}

// The retry is per participant: one host spending it must not write off another.
func TestStateDivergence_TheReplayIsSpentPerParticipant(t *testing.T) {
	env := setupTestProxy(t, 3, nil, true)
	env.proxy.redundancy.picker.stop()

	env.proxy.redundancy.maybeRecordEscrowStateDivergence(context.Background(), divergentInflight(env, 1), divergenceError())
	env.proxy.redundancy.maybeRecordEscrowStateDivergence(context.Background(), divergentInflight(env, 2), divergenceError())

	for _, hostIdx := range []int{1, 2} {
		_, blocked := env.proxy.redundancy.escrowStateBlockReason(env.proxy.redundancy.participantKeyForHost(hostIdx))
		require.False(t, blocked, "host %d spent only its own replay", hostIdx)
	}
}
