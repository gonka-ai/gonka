package types

import (
	"crypto/sha256"
	"encoding/binary"
	"math/big"
)

const DealerConstantTermPoKDomain = "GONKA-BLS-DKG-DEALER-POK-V1"

const dealerPoKScalarLen = 32

const DealerConstantTermPoKLen = 2 * dealerPoKScalarLen

const dealerPoKUniformLen = 48

var blsScalarFieldModulus, _ = new(big.Int).SetString("73eda753299d7d483339d80809a1d80553bda402fffe5bfeffffffff00000001", 16)

func DealerConstantTermPoKChallenge(epochID uint64, dealer string, commitments [][]byte, nonceCommitment []byte) [dealerPoKScalarLen]byte {
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

	uniform := expandMessageXMD(msg, []byte(DealerConstantTermPoKDomain), dealerPoKUniformLen)
	var out [dealerPoKScalarLen]byte
	new(big.Int).Mod(new(big.Int).SetBytes(uniform), blsScalarFieldModulus).FillBytes(out[:])
	return out
}

func expandMessageXMD(msg, dst []byte, lenInBytes int) []byte {
	dstPrime := append(append([]byte(nil), dst...), byte(len(dst)))

	h := sha256.New()
	h.Write(make([]byte, h.BlockSize()))
	h.Write(msg)
	h.Write([]byte{byte(lenInBytes >> 8), byte(lenInBytes), 0})
	h.Write(dstPrime)
	b0 := h.Sum(nil)

	out := make([]byte, 0, lenInBytes)
	prev := make([]byte, len(b0))
	for i := 1; len(out) < lenInBytes; i++ {
		input := make([]byte, len(b0))
		for j := range b0 {
			input[j] = b0[j] ^ prev[j]
		}
		h.Reset()
		h.Write(input)
		h.Write([]byte{byte(i)})
		h.Write(dstPrime)
		prev = h.Sum(nil)
		out = append(out, prev...)
	}
	return out[:lenInBytes]
}

func appendLengthPrefixed(dst, data []byte) []byte {
	dst = binary.BigEndian.AppendUint32(dst, uint32(len(data)))
	return append(dst, data...)
}
