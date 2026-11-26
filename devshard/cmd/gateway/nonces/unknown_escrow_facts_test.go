package nonces

import (
	"errors"
	"testing"
	"time"

	"devshard/cmd/gateway/accounting"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/scheduler"
)

const unknownEscrowID = "escrow-not-yet-swept"

// Test flow:
//  1. Open a ledger and report a burned nonce for an escrow its sweep has not opened yet, which the ledger cannot apply.
//  2. Assert the drop is counted: a fact that arrives before the sweep opens its escrow is lost, and a loss nothing counts cannot be seen.
func TestAFactForAnEscrowNotYetOpenedIsCounted(t *testing.T) {
	ledger := Open(config.NonceAccounting{Enabled: true, SnapshotSeconds: 300}, t.TempDir(), nil, func() time.Time { return time.Unix(0, 0).UTC() })
	t.Cleanup(func() { _ = ledger.Close() })

	ledger.RecordGhost(unknownEscrowID, 1, scheduler.GhostReasonWindowFull)

	if dropped := ledger.UnknownEscrowFacts(); dropped != 1 {
		t.Fatalf("UnknownEscrowFacts() = %d, want the one fact the ledger could not apply", dropped)
	}
}

// Test flow:
//  1. Open a ledger and record a warmup probe's nonce for an escrow its sweep has not opened yet.
//  2. Assert the probe is refused to its caller and counted too: the probe is the fact most likely to arrive before its escrow is opened.
func TestAProbeForAnEscrowNotYetOpenedIsCounted(t *testing.T) {
	ledger := Open(config.NonceAccounting{Enabled: true, SnapshotSeconds: 300}, t.TempDir(), nil, func() time.Time { return time.Unix(0, 0).UTC() })
	t.Cleanup(func() { _ = ledger.Close() })

	if err := ledger.RecordProbe(unknownEscrowID, accounting.Attempt{Nonce: 1, Sent: true}); !errors.Is(err, accounting.ErrUnknownEscrow) {
		t.Fatalf("RecordProbe() = %v, want the unknown-escrow refusal its caller names", err)
	}
	if dropped := ledger.UnknownEscrowFacts(); dropped != 1 {
		t.Fatalf("UnknownEscrowFacts() = %d, want the probe counted", dropped)
	}
}
