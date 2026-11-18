package heightsync

import (
	"errors"
	"fmt"
)

const (
	MaxSectionChainIDChars          = 50
	MaxSectionLabelChars            = 32
	MaxSectionBlockHashHexWireChars = MaxMainnetBlockHashHexChars + len("0x")
	MaxOriginatorSenderIDChars      = 90
)

var ErrSectionOversized = errors.New("heightsync: height_sync section field exceeds wire bound")

func CheckSectionWireBounds(sec *HeightSyncSection) error {
	if sec == nil {
		return nil
	}
	fields := []struct {
		name string
		n    int
		max  int
	}{
		{"chain_id", len(sec.ChainID), MaxSectionChainIDChars},
		{"proof_type", len(sec.ProofType), MaxSectionLabelChars},
		{"direction", len(sec.Direction), MaxSectionLabelChars},
		{"mainnet_block_hash_hex", len(sec.MainnetBlockHashHex), MaxSectionBlockHashHexWireChars},
		{"originator_sender_id", len(sec.OriginatorSenderID), MaxOriginatorSenderIDChars},
		{"sender_signature", len(sec.SenderSignature), MaxOriginSignatureBytes},
	}
	for _, f := range fields {
		if f.n > f.max {
			return fmt.Errorf("%w: %s length %d exceeds %d", ErrSectionOversized, f.name, f.n, f.max)
		}
	}
	return nil
}
