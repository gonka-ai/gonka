package store

import (
	"context"
	"testing"
)

const broadcastSettleTxHash = "settle-tx-1"

// Test flow:
//  1. Register an escrow and record the settle transaction it broadcast, which stamps the hash with its broadcast time.
//  2. Register the same escrow again, as an operator or a seed does, with a record that carries no hash.
//  3. Assert the hash and its stamp are both still there: an upsert never writes the hash, so it cannot leave a stamp naming a hash it erased, or a hash with no stamp.
func TestReRegisteringAnEscrowKeepsItsSettleTransaction(t *testing.T) {
	ctx := context.Background()
	registry := openTestStore(t)
	record := DevshardRecord{EscrowID: "1", PrivateKeyEnv: "KEY_1", Model: "model-a", Active: true}
	if err := registry.UpsertDevshard(ctx, record); err != nil {
		t.Fatalf("UpsertDevshard() = %v, want nil", err)
	}
	if err := registry.SetDevshardSettleTxHash(ctx, record.EscrowID, broadcastSettleTxHash); err != nil {
		t.Fatalf("SetDevshardSettleTxHash() = %v, want nil", err)
	}

	if err := registry.UpsertDevshard(ctx, record); err != nil {
		t.Fatalf("UpsertDevshard() again = %v, want nil", err)
	}

	hash, broadcastAt, err := registry.DevshardSettleTxHash(ctx, record.EscrowID)
	if err != nil {
		t.Fatalf("DevshardSettleTxHash() = %v, want nil", err)
	}
	if hash != broadcastSettleTxHash || broadcastAt.IsZero() {
		t.Fatalf("settle tx = %q stamped %v, want %q with its broadcast stamp", hash, broadcastAt, broadcastSettleTxHash)
	}
}
