package types

import (
	"encoding/binary"
	"fmt"

	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
)

const DealerConstantTermPoKDomain = "GONKA-BLS-DKG-DEALER-POK-V1"

const DealerConstantTermPoKLen = 2 * fr.Bytes

func DealerConstantTermPoKChallenge(epochID uint64, dealer string, commitments [][]byte, nonceCommitment []byte) (fr.Element, error) {
	size := 8 + 4 + len(dealer) + 4 + 4 + len(nonceCommitment)
	for _, c := range commitments {
		size += 4 + len(c)
	}
	msg := make([]byte, 0, size)
	msg = binary.BigEndian.AppendUint64(msg, epochID)
	msg = appendLengthPrefixed(msg, []byte(dealer))
	msg = binary.BigEndian.AppendUint32(msg, uint32(len(commitments)))
	for _, c := range commitments {
		msg = appendLengthPrefixed(msg, c)
	}
	msg = appendLengthPrefixed(msg, nonceCommitment)

	elems, err := fr.Hash(msg, []byte(DealerConstantTermPoKDomain), 1)
	if err != nil {
		return fr.Element{}, fmt.Errorf("failed to hash dealer proof-of-knowledge challenge: %w", err)
	}
	return elems[0], nil
}

func appendLengthPrefixed(dst, data []byte) []byte {
	dst = binary.BigEndian.AppendUint32(dst, uint32(len(data)))
	return append(dst, data...)
}
