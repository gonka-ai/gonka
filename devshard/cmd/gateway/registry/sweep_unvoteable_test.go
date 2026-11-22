package registry

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/internal/statetest"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/stub"
	"devshard/types"
	"devshard/user"
)

func newUnvoteableEscrow(t *testing.T) longResponseEscrow {
	t.Helper()
	signers := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	group := testutil.MakeMultiSlotGroup(signers, []int{2, 1, 1})
	config := testutil.DefaultConfig(len(group))
	creator := testutil.MustGenerateKey(t)
	verifier := signing.NewSecp256k1Verifier()
	slotSigners := make([]*signing.Secp256k1Signer, len(group))
	clients := make([]user.HostClient, len(group))
	hosts := make(map[string]*host.Host, len(signers))
	for slot, assignment := range group {
		for _, signer := range signers {
			if signer.Address() != assignment.ValidatorAddress {
				continue
			}
			slotSigners[slot] = signer
			if hosts[signer.Address()] == nil {
				hostMachine := statetest.MustStateMachine(t, longResponseEscrowID, config, group, 1_000_000, creator.Address(), verifier)
				node, err := host.NewHost(hostMachine, signer, stub.NewInferenceEngine(), longResponseEscrowID, group, nil, host.WithGrace(100))
				require.NoError(t, err)
				hosts[signer.Address()] = node
			}
			clients[slot] = acceptingVoter{HostClient: &user.InProcessClient{Host: hosts[signer.Address()]}, signer: signer, slot: assignment.SlotID}
		}
	}
	machine := statetest.MustStateMachine(t, longResponseEscrowID, config, group, 1_000_000, creator.Address(), verifier)
	session, err := user.NewSession(machine, creator, longResponseEscrowID, group, clients, verifier)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	handle := NewSessionHandle(session, machine).(sessionHandle)
	escrow := longResponseEscrow{handle: handle, session: session, machine: machine, signers: slotSigners}
	escrow.registry = New(Deps{
		ServingSessions: func(context.Context, string) (EscrowSession, error) { return handle, nil },
		Now:             fixedClock(),
	})
	require.NoError(t, escrow.registry.Add(context.Background(), longResponseEscrowID, "llama"))
	escrow.registry.ShareChainMaxNonce(1_700)
	return escrow
}

// Test flow:
//  1. Publish a real escrow of four slots where one validator holds two, so its nonces cannot reach the vote threshold, and start four inferences confirmed two hours ago and never finished.
//  2. Sweep with a budget of 8 and assert only the two nonces other validators execute were voted, applied and timed out.
//  3. Sweep again and assert the two unvoteable nonces were not voted, so they no longer take the budget, and are still started.
func TestTheSweepNeverVotesANonceNoRoundCanApply(t *testing.T) {
	escrow := newUnvoteableEscrow(t)
	nonces := make([]uint64, 4)
	for index := range nonces {
		nonces[index] = startedConfirmedAt(t, escrow, time.Now().Add(-2*time.Hour).Unix(), false)
	}

	due, applied, failed := escrow.registry.SweepExecutionTimeouts(context.Background(), 0, 8)

	require.Equal(t, [3]int{2, 2, 0}, [3]int{due, applied, failed})
	var timedOut, started int
	for _, nonce := range nonces {
		record, _ := escrow.machine.GetInference(nonce)
		switch record.Status {
		case types.StatusTimedOut:
			timedOut++
		case types.StatusStarted:
			started++
		}
	}
	require.Equal(t, [2]int{2, 2}, [2]int{timedOut, started})
	due, applied, failed = escrow.registry.SweepExecutionTimeouts(context.Background(), 0, 8)
	require.Equal(t, [3]int{0, 0, 0}, [3]int{due, applied, failed})
}

// Test flow:
//  1. Publish the four-slot escrow where one validator holds two slots, and start four inferences confirmed two hours ago and never finished.
//  2. Sweep before finalizing with a budget of 8 and no race grace.
//  3. Assert only the two voteable nonces were voted and applied, and the two unvoteable ones counted neither as due nor as failed, so finalize does not wait on votes that cannot pass.
func TestTheSweepBeforeFinalizeSkipsANonceNoRoundCanApply(t *testing.T) {
	escrow := newUnvoteableEscrow(t)
	for range 4 {
		startedConfirmedAt(t, escrow, time.Now().Add(-2*time.Hour).Unix(), false)
	}

	due, applied, failed, err := escrow.registry.SweepBeforeFinalize(context.Background(), longResponseEscrowID, 8)

	require.NoError(t, err)
	require.Equal(t, [3]int{2, 2, 0}, [3]int{due, applied, failed})
}
