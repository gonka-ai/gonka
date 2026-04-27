package types

import (
	"encoding/binary"

	"golang.org/x/crypto/sha3"
)

const DealerValidityProofDomain = "gonka:share-proof-v1"

func BuildDealerValidityProofHash(epochID uint64, dealerIndex uint32) []byte {
	domain := []byte(DealerValidityProofDomain)
	payload := make([]byte, len(domain)+8+4)
	copy(payload, domain)
	binary.BigEndian.PutUint64(payload[len(domain):len(domain)+8], epochID)
	binary.BigEndian.PutUint32(payload[len(domain)+8:], dealerIndex)

	hash := sha3.NewLegacyKeccak256()
	hash.Write(payload)
	return hash.Sum(nil)
}
