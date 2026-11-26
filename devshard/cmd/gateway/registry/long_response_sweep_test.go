package registry

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/cmd/gateway/engine"
	"devshard/host"
	"devshard/internal/statetest"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/stub"
	"devshard/types"
	"devshard/user"
)

const longResponseEscrowID = "escrow-1"

// acceptingVoter serves requests in process and signs an accepting timeout vote for every execution timeout it is asked about.
type acceptingVoter struct {
	user.HostClient
	signer *signing.Secp256k1Signer
	slot   uint32
}

func (v acceptingVoter) VerifyTimeout(_ context.Context, inferenceID uint64, reason types.TimeoutReason, _ *host.InferencePayload, _ []types.Diff, _ host.TimeoutArtifacts) (bool, []byte, uint32, []*types.DevshardTx, string, error) {
	content, err := types.CanonicalSignedBytes(&types.TimeoutVoteContent{
		EscrowId:    longResponseEscrowID,
		InferenceId: inferenceID,
		Reason:      reason,
		Accept:      true,
	})
	if err != nil {
		return false, nil, 0, nil, "", err
	}
	signature, err := v.signer.Sign(content)
	if err != nil {
		return false, nil, 0, nil, "", err
	}
	return true, signature, v.slot, nil, "", nil
}

func (v acceptingVoter) VerifyErrorMiss(context.Context, uint64, []types.Diff, host.TimeoutArtifacts) (bool, []byte, uint32, []*types.DevshardTx, string, error) {
	return false, nil, 0, nil, "not asked", nil
}

type longResponseEscrow struct {
	registry *Registry
	handle   sessionHandle
	session  *user.Session
	machine  *state.StateMachine
	signers  []*signing.Secp256k1Signer
	rig      *floorRig
}

func publishLongResponseEscrow(t *testing.T, servedBindings ...servedBindingObserver) longResponseEscrow {
	t.Helper()
	return publishVotingEscrow(t, func(voter acceptingVoter) user.HostClient { return voter }, servedBindings...)
}

func publishVotingEscrow(t *testing.T, client func(acceptingVoter) user.HostClient, servedBindings ...servedBindingObserver) longResponseEscrow {
	t.Helper()
	escrow := newVotingEscrow(t, client)
	deps := Deps{
		ServingSessions: func(context.Context, string) (EscrowSession, error) { return escrow.handle, nil },
		Now:             fixedClock(),
	}
	if len(servedBindings) > 0 {
		deps.ServedBindings = servedBindings[0]
	}
	escrow.registry = New(deps)
	require.NoError(t, escrow.registry.Add(context.Background(), longResponseEscrowID, "llama"))
	return escrow
}

func newVotingEscrow(t *testing.T, client func(acceptingVoter) user.HostClient, hostOptions ...host.HostOption) longResponseEscrow {
	t.Helper()
	signers := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	group := testutil.MakeGroup(signers)
	config := testutil.DefaultConfig(len(group))
	creator := testutil.MustGenerateKey(t)
	verifier := signing.NewSecp256k1Verifier()
	rig := newFloorRig(len(signers), floorStartHeight)
	clients := make([]user.HostClient, len(signers))
	for index, signer := range signers {
		hostMachine := statetest.MustStateMachine(t, longResponseEscrowID, config, group, 1_000_000, creator.Address(), verifier)
		hostNode, err := host.NewHost(hostMachine, signer, stub.NewInferenceEngine(), longResponseEscrowID, group, nil, append([]host.HostOption{host.WithGrace(100), host.WithChainOracle(rig.oracles[index])}, hostOptions...)...)
		require.NoError(t, err)
		clients[index] = client(acceptingVoter{HostClient: &user.InProcessClient{Host: hostNode}, signer: signer, slot: group[index].SlotID})
	}
	machine := statetest.MustStateMachine(t, longResponseEscrowID, config, group, 1_000_000, creator.Address(), verifier)
	session, err := user.NewSession(machine, creator, longResponseEscrowID, group, clients, verifier, rig.sessionOptions(len(signers))...)
	require.NoError(t, err)
	rig.seedFloor(t, session)
	t.Cleanup(func() { _ = session.Close() })
	handle := NewSessionHandle(session, machine).(sessionHandle)
	return longResponseEscrow{handle: handle, session: session, machine: machine, signers: signers, rig: rig}
}

const floorStartHeight = 100

func raiseFloor(t *testing.T, escrow longResponseEscrow, height uint64) {
	t.Helper()
	escrow.rig.raise(t, escrow.session, height)
}

func startedTwoHoursAgo(t *testing.T, escrow longResponseEscrow) uint64 {
	t.Helper()
	return startedConfirmedAt(t, escrow, time.Now().Add(-2*time.Hour).Unix(), false)
}

func startedConfirmedAt(t *testing.T, escrow longResponseEscrow, confirmedAt int64, delivered bool) uint64 {
	t.Helper()
	baseHeight, _, _ := escrow.machine.HeightSyncFloorAsOf(escrow.machine.LatestNonce() + 1)
	prepared, err := escrow.session.PrepareInferenceFn(func(user.HostBinding) (user.InferenceParams, bool, context.Context, error) {
		return user.InferenceParams{Model: "llama", Prompt: testutil.TestPrompt, InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000}, false, nil, nil
	})
	require.NoError(t, err)
	if delivered {
		_, err = escrow.session.SendOnly(context.Background(), prepared, io.Discard, nil)
		require.NoError(t, err)
	}
	nonce := prepared.Nonce()
	record, tracked := escrow.machine.GetInference(nonce)
	require.True(t, tracked)
	stamp := testutil.ReceiptStamp{Height: record.StartedAtHeight, Hash: []byte{0xaa}}
	signature := testutil.SignExecutorReceipt(t, escrow.signers[record.ExecutorSlot], longResponseEscrowID, nonce, record.PromptHash, record.Model, record.InputLength, record.MaxTokens, stamp)
	require.NoError(t, escrow.session.ProcessResponse(prepared.HostIdx(), &host.HostResponse{Nonce: nonce, Receipt: signature, ConfirmedAt: confirmedAt, ObservedHeight: stamp.Height, ObservedBlockHash: stamp.Hash}, nonce))
	require.NoError(t, escrow.session.SendPendingDiff(context.Background()))
	if age := time.Now().Unix() - confirmedAt; age > 0 {
		raiseFloor(t, escrow, baseHeight+uint64(age))
	}
	started, tracked := escrow.machine.GetInference(nonce)
	require.True(t, tracked)
	require.Equal(t, types.StatusStarted, started.Status, "the fixture must leave the nonce started with its reserve held")
	return nonce
}

// Test flow:
//  1. Publish a real escrow session whose three hosts sign accepting timeout votes, and start one inference whose executor confirmed it two hours ago and never finished; share the chain's max_nonce.
//  2. Judge that attempt as a race would: it streamed content for 281 seconds, stalled and never finished; assert the race plans a vote for it.
//  3. Assert the nonce still holds its whole reservation.
//  4. Run the registry's execution-timeout sweep, as the backstop for a vote the race failed to land; assert it finds the nonce due and applies the vote.
//  5. Assert the record timed out and its reservation is back in the balance.
func TestALongResponseWithoutAFinishIsVotedByTheRaceAndBackstoppedByTheSweep(t *testing.T) {
	escrow := publishLongResponseEscrow(t)
	escrow.registry.ShareChainMaxNonce(1_700)
	nonce := startedTwoHoursAgo(t, escrow)
	sentAt := time.Now().Add(-10 * time.Minute)
	outcome := engine.RaceOutcome{
		EscrowID: longResponseEscrowID,
		Model:    "llama",
		Attempts: []engine.AttemptOutcome{{
			Nonce:         nonce,
			StartedAt:     sentAt,
			SendTime:      sentAt,
			Completed:     sentAt.Add(281 * time.Second),
			ContentSource: "delta.content",
			Terminal:      engine.TerminalStalled,
		}},
	}

	plan := outcome.TimeoutPlan()
	require.Len(t, plan, 1)
	require.True(t, plan[0].Post, "the race must vote on a long response that never finished")
	record, tracked := escrow.machine.GetInference(nonce)
	require.True(t, tracked)
	balanceWhileHeld := escrow.machine.Balance()

	due, applied, failed := escrow.registry.SweepExecutionTimeouts(context.Background(), 0, 8)

	require.Equal(t, [3]int{1, 1, 0}, [3]int{due, applied, failed})
	swept, tracked := escrow.machine.GetInference(nonce)
	require.True(t, tracked)
	require.Equal(t, types.StatusTimedOut, swept.Status)
	require.Equal(t, balanceWhileHeld+record.ReservedCost, escrow.machine.Balance(), "the timed-out reservation must return to the balance")
}

// Test flow:
//  1. Publish a real escrow session with one inference started two hours ago and never finished, and share the chain's max_nonce.
//  2. Take the escrow's finalize lock, as a running Finalize does, and run the registry's execution-timeout sweep.
//  3. Assert the sweep left the escrow alone: nothing due, and the record still started with its reserve held.
func TestTheSweepLeavesAnEscrowAloneWhileItFinalizes(t *testing.T) {
	escrow := publishLongResponseEscrow(t)
	escrow.registry.ShareChainMaxNonce(1_700)
	nonce := startedTwoHoursAgo(t, escrow)
	escrow.handle.finalizing.Lock()
	defer escrow.handle.finalizing.Unlock()

	due, applied, failed := escrow.registry.SweepExecutionTimeouts(context.Background(), 0, 8)

	require.Equal(t, [3]int{0, 0, 0}, [3]int{due, applied, failed})
	record, tracked := escrow.machine.GetInference(nonce)
	require.True(t, tracked)
	require.Equal(t, types.StatusStarted, record.Status)
}
