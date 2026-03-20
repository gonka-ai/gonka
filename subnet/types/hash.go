package types

import (
	"crypto/sha256"

	"google.golang.org/protobuf/proto"
)

// TxHash computes a uint64 hash from the proto-serialized tx.
// Uses the first 8 bytes of SHA-256 as a compact key for dedup maps.
func TxHash(tx *SubnetTx) uint64 {
	data, err := proto.Marshal(tx)
	if err != nil {
		return 0
	}
	h := sha256.Sum256(data)
	var v uint64
	for i := 0; i < 8; i++ {
		v = (v << 8) | uint64(h[i])
	}
	return v
}
