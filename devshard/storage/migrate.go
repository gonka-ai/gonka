package storage

import (
	"bytes"
	"fmt"

	"devshard/types"

	"google.golang.org/protobuf/proto"
)

func verifyMigratedDiff(escrowID string, expected, actual types.DiffRecord) error {
	if expected.Nonce != actual.Nonce ||
		!bytes.Equal(expected.UserSig, actual.UserSig) ||
		!bytes.Equal(expected.PostStateRoot, actual.PostStateRoot) ||
		!bytes.Equal(expected.StateHash, actual.StateHash) ||
		expected.CreatedAt != actual.CreatedAt {
		return fmt.Errorf("migrated diff conflict for %s nonce %d", escrowID, expected.Nonce)
	}
	if !equalWarmKeyDelta(expected.WarmKeyDelta, actual.WarmKeyDelta) {
		return fmt.Errorf("migrated warm-key conflict for %s nonce %d", escrowID, expected.Nonce)
	}
	if !equalTxs(expected.Txs, actual.Txs) {
		return fmt.Errorf("migrated tx conflict for %s nonce %d", escrowID, expected.Nonce)
	}
	for slotID, sig := range expected.Signatures {
		if actualSig, ok := actual.Signatures[slotID]; ok && !bytes.Equal(sig, actualSig) {
			return fmt.Errorf("migrated signature conflict for %s nonce %d slot %d", escrowID, expected.Nonce, slotID)
		}
	}
	return nil
}

func equalWarmKeyDelta(a, b map[uint32]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		if b[k] != av {
			return false
		}
	}
	return true
}

func equalTxs(a, b []*types.DevshardTx) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !proto.Equal(a[i], b[i]) {
			return false
		}
	}
	return true
}
