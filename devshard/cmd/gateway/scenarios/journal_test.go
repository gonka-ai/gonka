package scenarios

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/internal/logcapture"
	"devshard/user"
)

const (
	tickFailedLine            = "escrow tick failed"
	settledPastUnresolvedLine = "escrow settled past unresolved nonces"
)

func journaledNonces(harness *gatewayHarness) ([]uint64, error) {
	entries, err := harness.currentGateway().Store().ListInferenceCompletions(harness.seededIDs[0])
	if err != nil {
		return nil, err
	}
	nonces := make([]uint64, 0, len(entries))
	for _, entry := range entries {
		nonces = append(nonces, entry.Nonce)
	}
	return nonces, nil
}

func theJournalHoldsAStartedNonce(label string) expectThat {
	return expectThat{label: label, verify: func(harness *gatewayHarness) error {
		nonces, err := journaledNonces(harness)
		if err != nil {
			return err
		}
		if len(nonces) == 0 {
			return errors.New("journal entries = 0, want a started nonce")
		}
		return nil
	}}
}

func finalizeIsRefusedAsPending(label string) expectThat {
	return expectThat{label: label, verify: func(harness *gatewayHarness) error {
		err := harness.currentGateway().Escrows().Finalize(context.Background(), harness.seededIDs[0])
		if err == nil {
			return fmt.Errorf("Finalize(%s) = nil, want pending and busy", harness.seededIDs[0])
		}
		if !errors.Is(err, user.ErrInferenceCompletionPending) || !errors.Is(err, chain.ErrDevshardBusy) {
			return fmt.Errorf("Finalize(%s) = %w, want pending and busy", harness.seededIDs[0], err)
		}
		return nil
	}}
}

func theJournalIsEmpty(label string) expectThat {
	return expectThat{label: label, verify: func(harness *gatewayHarness) error {
		nonces, err := journaledNonces(harness)
		if err != nil {
			return err
		}
		if len(nonces) != 0 {
			return fmt.Errorf("journal entries = %v, want none", nonces)
		}
		return nil
	}}
}

func theSeededEscrowIsStillOpenOnTheChain(label string) expectThat {
	return expectThat{label: label, verify: func(harness *gatewayHarness) error {
		if record, _ := harness.chain.escrowRecord(parseEscrowID(harness.seededIDs[0])); record.settled {
			return fmt.Errorf("escrow %s settled with a started nonce unresolved, want it deferred", harness.seededIDs[0])
		}
		return nil
	}}
}

func theForcedSettleIsLogged(logged *logcapture.Recorder, label string) expectThat {
	return expectThat{label: label, verify: func(harness *gatewayHarness) error {
		harness.currentGateway().Journal().Flush()
		if _, found := logged.Find(settledPastUnresolvedLine); !found {
			return fmt.Errorf("no %q line, want the forced settle narrated with its unresolved nonces", settledPastUnresolvedLine)
		}
		return nil
	}}
}

func noTickFailureIsLogged(logged *logcapture.Recorder, label string) expectThat {
	return expectThat{label: label, verify: func(harness *gatewayHarness) error {
		harness.currentGateway().Journal().Flush()
		if entry, found := logged.Find(tickFailedLine); found {
			return fmt.Errorf("logged %q with %v, want the deferral to stay silent", tickFailedLine, entry)
		}
		return nil
	}}
}

// Test flow:
//  1. Make every participant stall after its receipt, send one request and advance one minute.
//  2. Assert the started nonce is in the journal, then move the chain into the bridge window and advance three minutes.
//  3. Assert Finalize is refused as pending and busy, the escrow is still unsettled and the tick logged no failure.
//  4. Advance past the execution timeout, then assert the escrow settled and the journal is empty.
func TestAStartedNonceDefersSettlementUntilItsTimeoutResolvesIt(t *testing.T) {
	logged := logcapture.Install(t)
	spec := defaultSpec()
	spec.armedFloor = true
	spec.steps = []harnessStep{
		changeParticipants{change: func(behaviour *participantBehaviour) { behaviour.stall = true }},
		sendRequests{promptBytes: 200, maxTokens: 64, count: 1},
		advance{by: time.Minute},
		theJournalHoldsAStartedNonce("the started nonce is journaled"),
		moveChain{move: func(blockchain *fakeChain) { blockchain.moveToHeight(1900) }},
		advance{by: 3 * time.Minute},
		finalizeIsRefusedAsPending("finalize waits on the journal"),
		theSeededEscrowIsStillOpenOnTheChain("settlement is deferred"),
		noTickFailureIsLogged(logged, "the deferral is not a tick failure"),
		advance{by: 35 * time.Minute},
		seededEscrowSettled("settled once the timeout resolved the nonce"),
		theJournalIsEmpty("the journal is empty after settlement"),
	}
	runSteps(t, spec)
}

// Test flow:
//  1. Make every participant stall after its receipt, send one request and advance six minutes.
//  2. Restart the gateway and assert the journal entry survived and Finalize is still refused as pending.
//  3. Move the chain into the bridge window and advance past the execution timeout.
//  4. Assert the escrow settled and the journal is empty.
func TestAJournalEntrySurvivesARestartAndStillGatesFinalize(t *testing.T) {
	spec := defaultSpec()
	spec.armedFloor = true
	spec.steps = []harnessStep{
		changeParticipants{change: func(behaviour *participantBehaviour) { behaviour.stall = true }},
		sendRequests{promptBytes: 200, maxTokens: 64, count: 1},
		advance{by: 6 * time.Minute},
		theJournalHoldsAStartedNonce("the started nonce is journaled before the restart"),
		restartGateway{},
		theJournalHoldsAStartedNonce("the entry survived the restart"),
		finalizeIsRefusedAsPending("finalize still waits on the journal"),
		moveChain{move: func(blockchain *fakeChain) { blockchain.moveToHeight(1900) }},
		advance{by: 40 * time.Minute},
		seededEscrowSettled("settled once the timeout resolved the nonce"),
		theJournalIsEmpty("the journal is empty after settlement"),
	}
	runSteps(t, spec)
}

// Test flow:
//  1. Boot with the warm-up probe armed, seed an escrow of chain epoch 7 serving its last epoch, make every participant stall after its receipt, send one request and advance one minute.
//  2. Assert the started nonce is in the journal, Finalize is refused as pending and busy, and the escrow is still unsettled.
//  3. Move inside the settle margin and advance three minutes: the deadline forces the settle.
//  4. Assert the escrow settled without waiting for the execution timeout, the journal is empty and the forced settle was logged with its unresolved nonce.
func TestAForcedSettleFinalizesThroughAPendingJournalEntry(t *testing.T) {
	logged := logcapture.Install(t)
	spec := lastEpochSpec()
	spec.armedFloor = true
	spec.steps = []harnessStep{
		advance{by: 30 * time.Second},
		changeParticipants{change: func(behaviour *participantBehaviour) { behaviour.stall = true }},
		sendRequests{promptBytes: 200, maxTokens: 64, count: 1},
		advance{by: time.Minute},
		theJournalHoldsAStartedNonce("the started nonce is journaled"),
		finalizeIsRefusedAsPending("an ordinary finalize waits on the journal"),
		theSeededEscrowIsStillOpenOnTheChain("the escrow is unsettled before its margin"),
		moveInsideTheMargin(),
		advance{by: 3 * time.Minute},
		seededEscrowSettled("the forced settle did not wait for the execution timeout"),
		theJournalIsEmpty("the forced settle cleared the escrow's journal"),
		theForcedSettleIsLogged(logged, "the unresolved nonce was logged"),
	}
	runStepsWith(t, spec, switchedToEpochEight)
}
