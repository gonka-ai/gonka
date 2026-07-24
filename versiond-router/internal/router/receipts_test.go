package router

import (
	"errors"
	"os"
	"testing"
	"time"
)

func TestReceiptIndexImportsValidAuditRecordsAndSkipsCorruption(t *testing.T) {
	controller, _, paths := newTestController(t)
	audit := "{not json}\n" +
		`{"time":"2026-07-24T00:00:00Z",` +
		`"operation_id":"evacuate-2","action":"transfer",` +
		`"host":"versiond-2","membership_id":"membership-versiond-2",` +
		`"target":"offline","result":"completed"}` + "\n"
	if err := os.WriteFile(paths.AuditPath, []byte(audit), 0o600); err != nil {
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
