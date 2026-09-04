package heightsync

import (
	"crypto/sha256"
	"testing"
)

func TestMarkLog_CapacityDropsOldest(t *testing.T) {
	log := NewMarkLogCapacity(2)
	log.Append(AttributableMark{Kind: MarkDisputeCarrier, Nonce: 1})
	log.Append(AttributableMark{Kind: MarkDisputeCarrier, Nonce: 2})
	log.Append(AttributableMark{Kind: MarkVectorContradiction, Nonce: 3})
	if log.Len() != 2 {
		t.Fatalf("len=%d want 2", log.Len())
	}
	all := log.All()
	if all[0].Nonce != 2 || all[1].Nonce != 3 {
		t.Fatalf("retained %#v; want nonces 2, 3", all)
	}
	if !log.HasKind(MarkVectorContradiction) {
		t.Fatal("newest kind must be retained")
	}
	if log.HasKind(MarkHeightUnbacked) {
		t.Fatal("dropped kind must be gone")
	}
}

func TestMarkLog_AppendCapsBlob(t *testing.T) {
	log := NewMarkLog()
	big := make([]byte, MaxMarkBlobBytes+64)
	for i := range big {
		big[i] = byte(i)
	}
	log.Append(AttributableMark{Kind: MarkDisputeCarrier, Blob: big})
	got := log.All()[0].Blob
	if len(got) != sha256.Size {
		t.Fatalf("capped blob len=%d want %d", len(got), sha256.Size)
	}
	sum := sha256.Sum256(big)
	for i := range got {
		if got[i] != sum[i] {
			t.Fatal("capped blob must be sha256 of the original")
		}
	}
}
