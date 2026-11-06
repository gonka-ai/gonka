package chain

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"devshard/signing"
)

// newFakeTxClient wires a client to a transport that answers in process. Nothing here reaches a
// connection, so a test that expects no call can prove it by the transport's own records.
func newFakeTxClient(t *testing.T, transport *fakeTransport) *TxClient {
	t.Helper()
	client, err := NewTxClient(Config{
		Transport:    transport,
		PollInterval: time.Millisecond,
		PollTimeout:  time.Second,
		Now:          func() time.Time { return time.Unix(1_800_000_000, 0).UTC() },
	})
	if err != nil {
		t.Fatalf("NewTxClient: %v", err)
	}
	return client
}

// Test flow:
//  1. Call NewTxClient with an empty Config (no transport).
//  2. Assert it returns an error.
func TestNewTxClientRequiresATransport(t *testing.T) {
	_, err := NewTxClient(Config{})
	if err == nil {
		t.Fatal("want an error when no transport is given, got nil")
	}
}

// Test flow:
//  1. Call NewTxClient with only a transport set, leaving every other field zero.
//  2. Assert fee denom, fee amount, gas limit, poll interval, poll timeout and the clock all fall back to their documented defaults.
func TestNewTxClientAppliesDefaultsForZeroFields(t *testing.T) {
	client, err := NewTxClient(Config{Transport: newFakeTransport()})
	if err != nil {
		t.Fatalf("NewTxClient: %v", err)
	}
	if client.feeDenom != DefaultFeeDenom {
		t.Errorf("feeDenom = %q, want %q", client.feeDenom, DefaultFeeDenom)
	}
	if client.feeAmount != DefaultFeeAmount {
		t.Errorf("feeAmount = %d, want %d", client.feeAmount, DefaultFeeAmount)
	}
	if client.gasLimit != DefaultGasLimit {
		t.Errorf("gasLimit = %d, want %d", client.gasLimit, DefaultGasLimit)
	}
	if client.pollInterval != DefaultPollInterval {
		t.Errorf("pollInterval = %v, want %v", client.pollInterval, DefaultPollInterval)
	}
	if client.pollTimeout != DefaultPollTimeout {
		t.Errorf("pollTimeout = %v, want %v", client.pollTimeout, DefaultPollTimeout)
	}
	if client.now == nil {
		t.Error("now default not applied")
	}
}

// Test flow:
//  1. Build a table of invalid CreateEscrow inputs: table varies between a nil signer, a zero amount, and a blank model id.
//  2. Call CreateEscrow with each invalid input.
//  3. Assert it returns an error and the transport recorded no broadcast.
func TestCreateEscrowValidatesInput(t *testing.T) {
	validSigner := fixedSigner(t)

	testCases := []struct {
		name    string
		signer  *signing.Secp256k1Signer
		amount  uint64
		modelID string
	}{
		{name: "nil_signer", amount: 1_000_000, modelID: fixedModelID},
		{name: "zero_amount", signer: validSigner, modelID: fixedModelID},
		{name: "blank_model_id", signer: validSigner, amount: 1_000_000, modelID: "   "},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			transport := newFakeTransport()
			client := newFakeTxClient(t, transport)

			_, err := client.CreateEscrow(t.Context(), testCase.signer, testCase.amount, testCase.modelID, nil)

			if err == nil {
				t.Fatal("want a validation error, got nil")
			}
			if len(transport.broadcasts()) != 0 {
				t.Fatal("a rejected request still reached the chain")
			}
		})
	}
}

// Test flow:
//  1. Configure a fake transport and client, and an onPrepared callback that records the tx hash, whether it ran before any broadcast, and pre-registers the create-escrow commit result.
//  2. Call CreateEscrow with that callback.
//  3. Assert the intent was recorded before the broadcast happened.
//  4. Assert the returned result's tx hash, escrow id and creator match what onPrepared and the commit event produced.
func TestCreateEscrowRecordsTheIntentBeforeItBroadcasts(t *testing.T) {
	signer := fixedSigner(t)
	transport := newFakeTransport()
	transport.account = Account{Number: 7}
	client := newFakeTxClient(t, transport)

	var recordedHash string
	var recordedBeforeBroadcast bool
	onPrepared := func(txHash string) error {
		recordedHash = txHash
		recordedBeforeBroadcast = len(transport.broadcasts()) == 0
		transport.setTx(txHash, escrowCreatedResult("42"))
		return nil
	}

	result, err := client.CreateEscrow(t.Context(), signer, 1_000_000, fixedModelID, onPrepared)
	if err != nil {
		t.Fatalf("CreateEscrow: %v", err)
	}
	if !recordedBeforeBroadcast {
		t.Fatal("the intent was recorded after the broadcast, so a crash between them loses the escrow")
	}
	if result.TxHash != recordedHash {
		t.Fatalf("TxHash = %q, want the hash the intent was recorded under %q", result.TxHash, recordedHash)
	}
	if result.EscrowID != 42 {
		t.Fatalf("EscrowID = %d, want the id from the commit event", result.EscrowID)
	}
	if result.Creator != signer.Address() {
		t.Fatalf("Creator = %q, want %q", result.Creator, signer.Address())
	}
}

// Test flow:
//  1. Call CreateEscrow with an onPrepared callback that returns an error.
//  2. Assert CreateEscrow returns an error and the transport recorded no broadcast.
func TestCreateEscrowOnPreparedErrorAbortsBeforeBroadcast(t *testing.T) {
	transport := newFakeTransport()
	client := newFakeTxClient(t, transport)

	_, err := client.CreateEscrow(t.Context(), fixedSigner(t), 1_000_000, fixedModelID, func(string) error {
		return errors.New("store unavailable")
	})

	if err == nil {
		t.Fatal("want the intent failure to abort the create, got nil")
	}
	if len(transport.broadcasts()) != 0 {
		t.Fatal("an escrow was created on chain after the intent write failed")
	}
}

// Test flow:
//  1. Configure a fake transport whose Broadcast reports a hash different from the one the client computed locally.
//  2. Call CreateEscrow.
//  3. Assert it returns a "hash mismatch" error.
func TestCreateEscrowHashMismatchErrors(t *testing.T) {
	transport := newFakeTransport()
	transport.broadcastHash = "0000000000000000000000000000000000000000000000000000000000000000"
	client := newFakeTxClient(t, transport)

	_, err := client.CreateEscrow(t.Context(), fixedSigner(t), 1_000_000, fixedModelID, nil)

	if err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("CreateEscrow = %v, want a hash mismatch", err)
	}
}

// Test flow:
//  1. Build a table of invalid SettleEscrow inputs: table varies between a nil signer and a zero-value settlement input.
//  2. Call SettleEscrow with each invalid input.
//  3. Assert it returns an error and the transport recorded no broadcast.
func TestSettleEscrowValidatesInput(t *testing.T) {
	validSigner := fixedSigner(t)

	testCases := []struct {
		name   string
		signer *signing.Secp256k1Signer
		input  SettlementInput
	}{
		{name: "nil_signer", input: fixedSettlementFull()},
		{name: "zero_escrow_id", signer: validSigner, input: SettlementInput{}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			transport := newFakeTransport()
			client := newFakeTxClient(t, transport)

			_, err := client.SettleEscrow(t.Context(), testCase.signer, testCase.input, nil)

			if err == nil {
				t.Fatal("want a validation error, got nil")
			}
			if len(transport.broadcasts()) != 0 {
				t.Fatal("a rejected settlement still reached the chain")
			}
		})
	}
}

// Test flow:
//  1. Configure a fake transport whose onTx hook makes the broadcast transaction appear only from the second poll onward.
//  2. Call SettleEscrow with a full settlement input.
//  3. Assert it succeeded only after polling at least twice.
//  4. Assert the result's escrow id and settler match the input and signer.
func TestSettleEscrowWaitsForTheTransactionToCommit(t *testing.T) {
	signer := fixedSigner(t)
	transport := newFakeTransport()
	transport.account = Account{Number: 9}
	var polls atomic.Int64
	transport.onTx = func(call int) {
		polls.Store(int64(call))
		if call >= 2 {
			for _, sent := range transport.broadcasts() {
				transport.setTx(txHashFromBytes(sent), TxResult{})
			}
		}
	}
	client := newFakeTxClient(t, transport)
	input := fixedSettlementFull()

	result, err := client.SettleEscrow(t.Context(), signer, input, nil)
	if err != nil {
		t.Fatalf("SettleEscrow: %v", err)
	}
	if polls.Load() < 2 {
		t.Fatalf("polls = %d, want the settle to wait past the first answer", polls.Load())
	}
	if result.EscrowID != input.EscrowID {
		t.Fatalf("EscrowID = %d, want %d", result.EscrowID, input.EscrowID)
	}
	if result.Settler != signer.Address() {
		t.Fatalf("Settler = %q, want %q", result.Settler, signer.Address())
	}
}

// Test flow:
//  1. Configure a fake transport whose onTx hook resolves the broadcast transaction with a nonzero result code and a rejection log.
//  2. Call SettleEscrow.
//  3. Assert it returns an error containing the chain's rejection message.
func TestSettleEscrowFailsWhenTheChainRejectsTheCommittedTransaction(t *testing.T) {
	transport := newFakeTransport()
	transport.onTx = func(int) {
		for _, sent := range transport.broadcasts() {
			transport.setTx(txHashFromBytes(sent), TxResult{Code: 11, Codespace: "inference", RawLog: "settlement window closed"})
		}
	}
	client := newFakeTxClient(t, transport)

	_, err := client.SettleEscrow(t.Context(), fixedSigner(t), fixedSettlementFull(), nil)

	if err == nil || !strings.Contains(err.Error(), "settlement window closed") {
		t.Fatalf("SettleEscrow = %v, want the chain's rejection", err)
	}
}

// Test flow:
//  1. Build a table of transaction outcomes: table varies between a committed create-escrow result, a committed-but-failed result, and no transaction on chain.
//  2. Call GetTxEscrowID for each case.
//  3. For the not-on-chain case, assert it returns ErrTxNotFound.
//  4. For the other cases, assert the escrow id and found flag match the case's expectation.
func TestGetTxEscrowIDThreeWaySemantics(t *testing.T) {
	testCases := []struct {
		name       string
		result     TxResult
		present    bool
		wantID     uint64
		wantFound  bool
		wantErr    error
		wantNoErr  bool
		transports func(*fakeTransport)
	}{
		{name: "created", result: escrowCreatedResult("42"), present: true, wantID: 42, wantFound: true, wantNoErr: true},
		{name: "committed_but_failed", result: TxResult{Code: 5}, present: true, wantNoErr: true},
		{name: "not_on_chain", wantErr: ErrTxNotFound},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			transport := newFakeTransport()
			if testCase.present {
				transport.setTx("HASH", testCase.result)
			}
			client := newFakeTxClient(t, transport)

			escrowID, found, err := client.GetTxEscrowID(t.Context(), "HASH")

			if testCase.wantErr != nil {
				if !errors.Is(err, testCase.wantErr) {
					t.Fatalf("err = %v, want %v", err, testCase.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("GetTxEscrowID: %v", err)
			}
			if escrowID != testCase.wantID || found != testCase.wantFound {
				t.Fatalf("got (%d, %v), want (%d, %v)", escrowID, found, testCase.wantID, testCase.wantFound)
			}
		})
	}
}

// Test flow:
//  1. Configure a fake transport whose transaction read always fails.
//  2. Call GetTxEscrowID and assert it returns the transport failure.
//  3. Call TxCommitted and assert it also returns the transport failure.
func TestATransportFailureIsNeverReadAsAbsence(t *testing.T) {
	transport := newFakeTransport()
	transport.txErr = errTransportRefused
	client := newFakeTxClient(t, transport)

	if _, _, err := client.GetTxEscrowID(t.Context(), "HASH"); !errors.Is(err, errTransportRefused) {
		t.Fatalf("GetTxEscrowID = %v, want the transport failure", err)
	}
	if _, err := client.TxCommitted(t.Context(), "HASH"); !errors.Is(err, errTransportRefused) {
		t.Fatalf("TxCommitted = %v, want the transport failure", err)
	}
}

// Test flow:
//  1. Build a table of transaction outcomes: table varies between an executed transaction, a committed-but-failed one, and no transaction on chain.
//  2. Call TxCommitted for each case.
//  3. For the not-on-chain case, assert it returns ErrTxNotFound.
//  4. For the other cases, assert the succeeded flag matches the case's expectation.
func TestTxCommittedReportsExecution(t *testing.T) {
	testCases := []struct {
		name          string
		result        TxResult
		present       bool
		wantSucceeded bool
		wantErr       error
	}{
		{name: "executed", present: true, wantSucceeded: true},
		{name: "committed_but_failed", result: TxResult{Code: 3}, present: true},
		{name: "not_on_chain", wantErr: ErrTxNotFound},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			transport := newFakeTransport()
			if testCase.present {
				transport.setTx("HASH", testCase.result)
			}
			client := newFakeTxClient(t, transport)

			succeeded, err := client.TxCommitted(t.Context(), "HASH")

			if testCase.wantErr != nil {
				if !errors.Is(err, testCase.wantErr) {
					t.Fatalf("err = %v, want %v", err, testCase.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("TxCommitted: %v", err)
			}
			if succeeded != testCase.wantSucceeded {
				t.Fatalf("succeeded = %v, want %v", succeeded, testCase.wantSucceeded)
			}
		})
	}
}

// Test flow:
//  1. Create a TxClient with a short poll timeout against a transport that never has the transaction.
//  2. Call waitForCreatedEscrowID.
//  3. Assert it returns an error reporting the transaction is not confirmed within the timeout.
//  4. Assert the same error tells the caller not to create another escrow, since misreading a timeout as a failure would risk a duplicate.
func TestWaitForCreatedEscrowIDTimesOutWhenNeverFound(t *testing.T) {
	transport := newFakeTransport()
	client, err := NewTxClient(Config{Transport: transport, PollInterval: time.Millisecond, PollTimeout: 10 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewTxClient: %v", err)
	}

	_, err = client.waitForCreatedEscrowID(t.Context(), "HASH")

	if err == nil || !strings.Contains(err.Error(), "not confirmed within") {
		t.Fatalf("waitForCreatedEscrowID = %v, want a bounded wait that reports the broadcast", err)
	}
	if !strings.Contains(err.Error(), "do not create another") {
		t.Fatalf("waitForCreatedEscrowID = %v, want the caller told not to retry the creation", err)
	}
}

// Test flow:
//  1. Create a TxClient with a long poll interval and timeout.
//  2. Cancel the context immediately.
//  3. Call waitForCreatedEscrowID and assert it returns context.Canceled.
func TestWaitForCreatedEscrowIDReturnsOnContextCancel(t *testing.T) {
	transport := newFakeTransport()
	client, err := NewTxClient(Config{Transport: transport, PollInterval: time.Hour, PollTimeout: time.Hour})
	if err != nil {
		t.Fatalf("NewTxClient: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := client.waitForCreatedEscrowID(ctx, "HASH"); !errors.Is(err, context.Canceled) {
		t.Fatalf("waitForCreatedEscrowID = %v, want context.Canceled", err)
	}
}

type recordingSettlementNarrator struct {
	broadcasts []string
}

func (n *recordingSettlementNarrator) SettleBroadcast(escrowID, txHash, settler string) {
	n.broadcasts = append(n.broadcasts, escrowID+" "+txHash+" "+settler)
}

// Test flow:
//  1. Configure a fake transport and a recording settlement narrator, with onTx recording how many broadcasts were narrated by the first poll and then resolving the transaction.
//  2. Call SettleEscrow with a full settlement input.
//  3. Assert exactly one broadcast was already narrated by the time the first poll ran.
//  4. Assert the narrator recorded the escrow id, tx hash and settler address of that broadcast.
func TestASettleBroadcastIsNarratedBeforeItsCommitIsAwaited(t *testing.T) {
	signer := fixedSigner(t)
	transport := newFakeTransport()
	narrator := &recordingSettlementNarrator{}
	narratedAtFirstPoll := -1
	transport.onTx = func(call int) {
		if call == 1 {
			narratedAtFirstPoll = len(narrator.broadcasts)
		}
		for _, sent := range transport.broadcasts() {
			transport.setTx(txHashFromBytes(sent), TxResult{})
		}
	}
	client, err := NewTxClient(Config{
		Transport:    transport,
		PollInterval: time.Millisecond,
		PollTimeout:  time.Second,
		Now:          func() time.Time { return time.Unix(1_800_000_000, 0).UTC() },
		Narrator:     narrator,
	})
	if err != nil {
		t.Fatalf("NewTxClient: %v", err)
	}

	result, err := client.SettleEscrow(t.Context(), signer, fixedSettlementFull(), nil)

	if err != nil {
		t.Fatalf("SettleEscrow: %v", err)
	}
	if narratedAtFirstPoll != 1 {
		t.Fatalf("broadcasts narrated by the first commit poll = %d, want 1", narratedAtFirstPoll)
	}
	if want := "123 " + result.TxHash + " " + signer.Address(); len(narrator.broadcasts) != 1 || narrator.broadcasts[0] != want {
		t.Fatalf("narrated broadcasts = %v, want [%s]", narrator.broadcasts, want)
	}
}

// Test flow:
//  1. Give the fake transport a spendable balance one below amount + fee, the fee paid in the escrow denom.
//  2. Call CreateEscrow with an onPrepared hook that records its call.
//  3. Assert the error is a WalletUnderfundedError carrying have and need, onPrepared never ran, and nothing was broadcast.
func TestCreateEscrowRefusesAWalletThatCannotPayWithoutBroadcasting(t *testing.T) {
	const amount = 1_000_000
	transport := newFakeTransport()
	transport.balance = amount + DefaultFeeAmount - 1
	client := newFakeTxClient(t, transport)
	prepared := false

	_, err := client.CreateEscrow(t.Context(), fixedSigner(t), amount, fixedModelID, func(string) error {
		prepared = true
		return nil
	})

	var underfunded *WalletUnderfundedError
	if !errors.As(err, &underfunded) || !errors.Is(err, ErrWalletUnderfunded) {
		t.Fatalf("CreateEscrow = %v, want a WalletUnderfundedError", err)
	}
	if underfunded.Have != amount+DefaultFeeAmount-1 || underfunded.Need != amount+DefaultFeeAmount {
		t.Fatalf("have/need = %d/%d, want %d/%d", underfunded.Have, underfunded.Need, amount+DefaultFeeAmount-1, amount+DefaultFeeAmount)
	}
	if prepared || len(transport.broadcasts()) != 0 {
		t.Fatalf("prepared = %v, broadcasts = %d: an underfunded create must not reach the chain", prepared, len(transport.broadcasts()))
	}
}

// Test flow:
//  1. Give the fake transport exactly amount + fee, and resolve the create's commit in onPrepared.
//  2. Call CreateEscrow.
//  3. Assert the create is broadcast, and the balance was read once for the signer's address in the escrow denom.
func TestCreateEscrowBroadcastsWhenTheWalletCoversAmountAndFee(t *testing.T) {
	const amount = 1_000_000
	signer := fixedSigner(t)
	transport := newFakeTransport()
	transport.balance = amount + DefaultFeeAmount
	client := newFakeTxClient(t, transport)

	_, err := client.CreateEscrow(t.Context(), signer, amount, fixedModelID, func(txHash string) error {
		transport.setTx(txHash, escrowCreatedResult("42"))
		return nil
	})

	if err != nil {
		t.Fatalf("CreateEscrow: %v", err)
	}
	if len(transport.broadcasts()) != 1 {
		t.Fatalf("broadcasts = %d, want 1", len(transport.broadcasts()))
	}
	if want := []balanceCall{{address: signer.Address(), denom: EscrowDenom}}; !slices.Equal(transport.balanceCalls, want) {
		t.Fatalf("balance reads = %+v, want %+v", transport.balanceCalls, want)
	}
}

// Test flow:
//  1. Configure the TxClient with a fee denom other than the escrow denom.
//  2. Give the wallet exactly the amount in the escrow denom.
//  3. Assert the create is broadcast: a fee paid in another denom is not added to the escrow denom's need.
func TestCreateEscrowAddsTheFeeOnlyWhenItIsPaidInTheEscrowDenom(t *testing.T) {
	const amount = 1_000_000
	transport := newFakeTransport()
	transport.balance = amount
	client, err := NewTxClient(Config{
		Transport:    transport,
		FeeDenom:     "uother",
		PollInterval: time.Millisecond,
		PollTimeout:  time.Second,
		Now:          func() time.Time { return time.Unix(1_800_000_000, 0).UTC() },
	})
	if err != nil {
		t.Fatalf("NewTxClient: %v", err)
	}

	_, err = client.CreateEscrow(t.Context(), fixedSigner(t), amount, fixedModelID, func(txHash string) error {
		transport.setTx(txHash, escrowCreatedResult("42"))
		return nil
	})

	if err != nil {
		t.Fatalf("CreateEscrow: %v", err)
	}
	if len(transport.broadcasts()) != 1 {
		t.Fatalf("broadcasts = %d, want 1", len(transport.broadcasts()))
	}
}

// Test flow:
//  1. Make the fake transport's balance query fail.
//  2. Call CreateEscrow.
//  3. Assert the query error is returned, it is not ErrWalletUnderfunded, and nothing was broadcast.
func TestCreateEscrowStopsWhenTheBalanceCannotBeRead(t *testing.T) {
	transport := newFakeTransport()
	transport.balanceErr = errTransportRefused
	client := newFakeTxClient(t, transport)

	_, err := client.CreateEscrow(t.Context(), fixedSigner(t), 1_000_000, fixedModelID, nil)

	if !errors.Is(err, errTransportRefused) || errors.Is(err, ErrWalletUnderfunded) {
		t.Fatalf("CreateEscrow = %v, want the balance query's own error", err)
	}
	if len(transport.broadcasts()) != 0 {
		t.Fatal("a create was broadcast although the wallet balance could not be read")
	}
}
