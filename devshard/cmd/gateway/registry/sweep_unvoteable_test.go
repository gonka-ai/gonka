package registry

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/cmd/gateway/quorum"
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
	rig := newFloorRig(len(signers), floorStartHeight)
	slotSigners := make([]*signing.Secp256k1Signer, len(group))
	clients := make([]user.HostClient, len(group))
	hosts := make(map[string]*host.Host, len(signers))
	for slot, assignment := range group {
		for signerIndex, signer := range signers {
			if signer.Address() != assignment.ValidatorAddress {
				continue
			}
			slotSigners[slot] = signer
			if hosts[signer.Address()] == nil {
				hostMachine := statetest.MustStateMachine(t, longResponseEscrowID, config, group, 1_000_000, creator.Address(), verifier)
				node, err := host.NewHost(hostMachine, signer, stub.NewInferenceEngine(), longResponseEscrowID, group, nil, host.WithGrace(100), host.WithChainOracle(rig.oracles[signerIndex]))
				require.NoError(t, err)
				hosts[signer.Address()] = node
			}
			clients[slot] = acceptingVoter{HostClient: &user.InProcessClient{Host: hosts[signer.Address()]}, signer: signer, slot: assignment.SlotID}
		}
	}
	machine := statetest.MustStateMachine(t, longResponseEscrowID, config, group, 1_000_000, creator.Address(), verifier)
	session, err := user.NewSession(machine, creator, longResponseEscrowID, group, clients, verifier, rig.sessionOptions(len(signers))...)
	require.NoError(t, err)
	rig.seedFloor(t, session)
	t.Cleanup(func() { _ = session.Close() })
	handle := NewSessionHandle(session, machine).(sessionHandle)
	escrow := longResponseEscrow{handle: handle, session: session, machine: machine, signers: slotSigners, rig: rig}
	escrow.registry = New(Deps{
		ServingSessions: func(context.Context, string) (EscrowSession, error) { return handle, nil },
		Now:             fixedClock(),
	})
	require.NoError(t, escrow.registry.Add(context.Background(), longResponseEscrowID, "llama"))
	escrow.registry.ShareChainMaxNonce(1_700)
	return escrow
}

// Test flow:
//  1. Publish a real escrow of four slots where one validator holds two, so its nonces cannot reach the vote threshold, and start inferences past their deadline until at least two nonces of each kind exist.
//  2. Sweep with a budget of 8 and assert only the nonces other validators execute were voted, applied and timed out.
//  3. Sweep again and assert the unvoteable nonces were not voted, so they no longer take the budget, and are still started.
func TestTheSweepNeverVotesANonceNoRoundCanApply(t *testing.T) {
	escrow := newUnvoteableEscrow(t)
	voteable, unvoteable := startMixedNonces(t, escrow)

	due, applied, failed := escrow.registry.SweepExecutionTimeouts(context.Background(), 0, 8)

	require.Equal(t, [3]int{len(voteable), len(voteable), 0}, [3]int{due, applied, failed})
	for _, nonce := range voteable {
		record, _ := escrow.machine.GetInference(nonce)
		require.Equal(t, types.StatusTimedOut, record.Status, "voteable nonce %d", nonce)
	}
	for _, nonce := range unvoteable {
		record, _ := escrow.machine.GetInference(nonce)
		require.Equal(t, types.StatusStarted, record.Status, "unvoteable nonce %d", nonce)
	}
	due, applied, failed = escrow.registry.SweepExecutionTimeouts(context.Background(), 0, 8)
	require.Equal(t, [3]int{0, 0, 0}, [3]int{due, applied, failed})
}

// Test flow:
//  1. Publish the four-slot escrow where one validator holds two slots, and start inferences past their deadline until at least two nonces of each kind exist.
//  2. Sweep before finalizing with a budget of 8 and no race grace.
//  3. Assert only the voteable nonces were voted and applied, and the unvoteable ones counted neither as due nor as failed, so finalize does not wait on votes that cannot pass.
func TestTheSweepBeforeFinalizeSkipsANonceNoRoundCanApply(t *testing.T) {
	escrow := newUnvoteableEscrow(t)
	voteable, _ := startMixedNonces(t, escrow)

	due, applied, failed, err := escrow.registry.SweepBeforeFinalize(context.Background(), longResponseEscrowID, 8)

	require.NoError(t, err)
	require.Equal(t, [3]int{len(voteable), len(voteable), 0}, [3]int{due, applied, failed})
}

const (
	nonceSearchLimit = 32
	nonceKindsWanted = 2
)

func startMixedNonces(t *testing.T, escrow longResponseEscrow) (voteable, unvoteable []uint64) {
	t.Helper()
	for range nonceSearchLimit {
		if len(voteable) >= nonceKindsWanted && len(unvoteable) >= nonceKindsWanted {
			return voteable, unvoteable
		}
		for _, confirmedAt := range []int64{time.Now().Unix(), time.Now().Add(-2 * time.Hour).Unix()} {
			next := escrow.machine.LatestNonce() + 1
			canPass := quorum.Voteable(escrow.machine.SnapshotStateNoInferences().Group, escrow.machine.VoteThreshold(), next)
			nonce := startedConfirmedAt(t, escrow, confirmedAt, false)
			if canPass {
				voteable = append(voteable, nonce)
			} else {
				unvoteable = append(unvoteable, nonce)
			}
		}
	}
	t.Fatalf("no mix of %d voteable and %d unvoteable nonces within %d rounds, got %d and %d", nonceKindsWanted, nonceKindsWanted, nonceSearchLimit, len(voteable), len(unvoteable))
	return nil, nil
}
