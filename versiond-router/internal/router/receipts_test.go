package router

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestReceiptIndexImportFailsClosedUntilAuditIsRepaired(t *testing.T) {
	controller, _, paths := newTestController(t)
	validRecord := `{"time":"2026-07-24T00:00:00Z",` +
		`"operation_id":"evacuate-2","action":"transfer",` +
		`"host":"versiond-2","membership_id":"membership-versiond-2",` +
		`"target":"offline","result":"completed"}` + "\n"
	if err := os.WriteFile(
		paths.AuditPath,
		[]byte("{not json}\n"+validRecord),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	if _, err := controller.loadOrCreateReceiptIndex(); err == nil ||
		!strings.Contains(err.Error(), "line 1") {
		t.Fatalf("receipt import error = %v, want malformed line failure", err)
	}
	if _, err := os.Stat(paths.ReceiptsPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial receipt index was persisted: %v", err)
	}

	if err := os.WriteFile(paths.AuditPath, []byte(validRecord), 0o600); err != nil {
		t.Fatal(err)
	}
	index, err := controller.loadOrCreateReceiptIndex()
	if err != nil {
		t.Fatal(err)
	}
	receipt, ok := index.Completed["evacuate-2"]
	if !ok {
		t.Fatal("valid completion receipt was not imported")
	}
	if receipt.Host != "versiond-2" || receipt.Target != HostOffline {
		t.Fatalf("imported receipt = %#v", receipt)
	}

	if err := os.WriteFile(paths.AuditPath, []byte("rotated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reloaded, err := controller.loadOrCreateReceiptIndex()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reloaded.Completed["evacuate-2"]; !ok {
		t.Fatal("audit rotation removed a durable completion receipt")
	}
}

func TestReceiptIndexImportFailsClosedOnOversizedAuditRecord(t *testing.T) {
	controller, _, paths := newTestController(t)
	record := append(bytes.Repeat([]byte("x"), 1024*1024+1), '\n')
	if err := os.WriteFile(paths.AuditPath, record, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := controller.loadOrCreateReceiptIndex(); err == nil ||
		!strings.Contains(err.Error(), "scan router audit") {
		t.Fatalf("receipt import error = %v, want scanner failure", err)
	}
	if _, err := os.Stat(paths.ReceiptsPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial receipt index was persisted: %v", err)
	}
}

func TestReceiptIndexImportIgnoresOnlyTornFinalAuditRecord(t *testing.T) {
	controller, _, paths := newTestController(t)
	validRecord := `{"time":"2026-07-24T00:00:00Z",` +
		`"operation_id":"evacuate-2","action":"transfer",` +
		`"host":"versiond-2","membership_id":"membership-versiond-2",` +
		`"target":"offline","result":"completed"}` + "\n"
	if err := os.WriteFile(
		paths.AuditPath,
		[]byte(validRecord+`{"time":`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	index, err := controller.loadOrCreateReceiptIndex()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := index.Completed["evacuate-2"]; !ok {
		t.Fatal("valid receipt before torn final record was not imported")
	}

	if err := os.Remove(paths.ReceiptsPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		paths.AuditPath,
		[]byte(validRecord+"{not json}\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.loadOrCreateReceiptIndex(); err == nil ||
		!strings.Contains(err.Error(), "line 2") {
		t.Fatalf(
			"receipt import error = %v, want terminated final line failure",
			err,
		)
	}
}

func TestReceiptIndexTreatsRepeatedEquivalentAuditRecordsAsOne(t *testing.T) {
	controller, _, _ := newTestController(t)
	first := AuditRecord{
		Time:         time.Now().Add(-time.Minute).UTC(),
		OperationID:  "evacuate-2",
		Action:       "transfer",
		Host:         "versiond-2",
		MembershipID: "membership-versiond-2",
		Target:       HostOffline,
		Result:       "completed",
	}
	second := first
	second.Time = time.Now().UTC()
	if err := controller.appendAudit(first); err != nil {
		t.Fatal(err)
	}
	if err := controller.appendAudit(second); err != nil {
		t.Fatal(err)
	}

	index, err := controller.loadOrCreateReceiptIndex()
	if err != nil {
		t.Fatal(err)
	}
	receipt := index.Completed["evacuate-2"]
	if receipt.Conflict {
		t.Fatalf("equivalent audit records produced a conflict: %#v", receipt)
	}
	if !receipt.CompletedAt.Equal(second.Time) {
		t.Fatalf("completion time = %s, want %s", receipt.CompletedAt, second.Time)
	}
}

func TestReceiptIndexImportsTerminalPreFSMRecord(t *testing.T) {
	controller, _, _ := newTestController(t)
	if err := controller.appendAudit(AuditRecord{
		Time:        time.Now().UTC(),
		OperationID: "legacy-evacuation",
		Action:      "offline",
		Host:        "versiond-2",
		To:          HostOffline,
		Result:      "success",
	}); err != nil {
		t.Fatal(err)
	}

	index, err := controller.loadOrCreateReceiptIndex()
	if err != nil {
		t.Fatal(err)
	}
	receipt, ok := index.Completed["legacy-evacuation"]
	if !ok {
		t.Fatal("terminal pre-FSM audit record was not imported")
	}
	if receipt.Result != "completed" || receipt.Target != HostOffline {
		t.Fatalf("legacy receipt = %#v", receipt)
	}
}

func TestImportedReceiptPreservesLegacyActionAgnosticReplay(t *testing.T) {
	receipt := receiptFromAudit(AuditRecord{
		Time:         time.Now().UTC(),
		OperationID:  "add-versiond-2",
		Action:       "transfer",
		Host:         "versiond-2",
		MembershipID: "membership-versiond-2",
		Target:       HostActive,
		Result:       "completed",
	})

	if err := matchCompletedOperation(
		receipt,
		"add-versiond-2",
		"add",
		"versiond-2",
		"membership-versiond-2",
		HostActive,
	); err != nil {
		t.Fatalf("legacy add replay was rejected: %v", err)
	}
	if err := matchCompletedOperation(
		receipt,
		"add-versiond-2",
		"add",
		"versiond-2",
		"membership-replacement",
		HostActive,
	); !errors.Is(err, ErrOperationOwner) {
		t.Fatalf("membership mismatch error = %v, want ErrOperationOwner", err)
	}
}

func TestLookupOperationReturnsOneDurableCompletion(t *testing.T) {
	controller, _, _ := newTestController(t)
	completedAt := time.Now().UTC()
	if err := controller.persistCompletionReceipt(
		"decommission-versiond-2",
		operationReceipt{
			Action:       "transfer",
			Host:         "versiond-2",
			MembershipID: "membership-versiond-2",
			Target:       HostRemoved,
			Result:       "completed",
			CompletedAt:  completedAt,
		},
	); err != nil {
		t.Fatal(err)
	}

	lookup, err := controller.LookupOperation(
		context.Background(),
		"decommission-versiond-2",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !lookup.Completed || lookup.Completion == nil {
		t.Fatalf("operation lookup = %#v, want completed receipt", lookup)
	}
	if lookup.Completion.Host != "versiond-2" ||
		lookup.Completion.Target != HostRemoved ||
		!lookup.Completion.CompletedAt.Equal(completedAt) {
		t.Fatalf("operation completion = %#v", lookup.Completion)
	}

	missing, err := controller.LookupOperation(
		context.Background(),
		"decommission-versiond-3",
	)
	if err != nil {
		t.Fatal(err)
	}
	if missing.Completed || missing.Completion != nil {
		t.Fatalf("missing operation lookup = %#v", missing)
	}
}

func TestLookupOperationPreservesCanceledTransitionResult(t *testing.T) {
	controller, _, _ := newTestController(t)
	state := newTestState(t)
	if _, err := controller.Bootstrap(
		context.Background(),
		staticState(state),
	); err != nil {
		t.Fatal(err)
	}
	operationID := "cancel-versiond-2"
	membershipID := membershipOf(t, state, "versiond-2")
	if _, err := controller.Transition(context.Background(), Transition{
		OperationID:  operationID,
		MembershipID: membershipID,
		Host:         "versiond-2",
		From:         HostActive,
		To:           HostDraining,
		Target:       HostOffline,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Cancel(context.Background(), CancelTransfer{
		OperationID:  operationID,
		MembershipID: membershipID,
		Host:         "versiond-2",
	}); err != nil {
		t.Fatal(err)
	}

	lookup, err := controller.LookupOperation(
		context.Background(),
		operationID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !lookup.Completed || lookup.Completion == nil {
		t.Fatalf("operation lookup = %#v, want canceled receipt", lookup)
	}
	if lookup.Completion.Action != "cancel" ||
		lookup.Completion.Target != HostActive ||
		lookup.Completion.Result != "canceled" {
		t.Fatalf("cancellation receipt = %#v", lookup.Completion)
	}
}
