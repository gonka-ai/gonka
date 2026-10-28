package chain

import (
	"errors"
	"testing"
)

// Test flow:
//  1. Configure a fake transport for each case: table varies between an escrow present, no escrow, and a transport read failure.
//  2. Call GetEscrow through a fake tx client for each case.
//  3. For the failed-read case, assert the transport error is returned and found is false.
//  4. For the other cases, assert found and the escrow balance match the case's expectation.
func TestGetEscrowSeparatesAbsenceFromFailure(t *testing.T) {
	testCases := []struct {
		name      string
		escrow    EscrowInfo
		present   bool
		failure   error
		wantFound bool
		wantErr   bool
	}{
		{name: "present", escrow: EscrowInfo{EscrowID: "7", Balance: 500}, present: true, wantFound: true},
		{name: "absent"},
		{name: "read_failed", failure: errTransportRefused, wantErr: true},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			transport := newFakeTransport()
			transport.escrow = testCase.escrow
			transport.escrowRaw = testCase.present
			transport.escrowErr = testCase.failure
			client := newFakeTxClient(t, transport)

			info, found, err := client.GetEscrow(t.Context(), "7")

			if testCase.wantErr {
				if !errors.Is(err, errTransportRefused) {
					t.Fatalf("err = %v, want the transport failure", err)
				}
				if found {
					t.Fatal("a failed read reported the escrow as present")
				}
				return
			}
			if err != nil {
				t.Fatalf("GetEscrow: %v", err)
			}
			if found != testCase.wantFound {
				t.Fatalf("found = %v, want %v", found, testCase.wantFound)
			}
			if found && info.Balance != testCase.escrow.Balance {
				t.Fatalf("balance = %d, want %d", info.Balance, testCase.escrow.Balance)
			}
		})
	}
}

// Test flow:
//  1. Call GetEscrow with a non-numeric escrow id.
//  2. Assert it returns an error and found is false.
func TestGetEscrowRejectsANonNumericID(t *testing.T) {
	client := newFakeTxClient(t, newFakeTransport())

	_, found, err := client.GetEscrow(t.Context(), "not-a-number")

	if err == nil {
		t.Fatal("want an error for an unusable escrow id, got nil")
	}
	if found {
		t.Fatal("an unusable id reported an escrow as present")
	}
}
