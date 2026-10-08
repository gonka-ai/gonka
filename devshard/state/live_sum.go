package state

import (
	"crypto/sha512"
	"hash"

	"github.com/gtank/ristretto255"
	"google.golang.org/protobuf/encoding/protowire"
)

// liveSetDST is the RFC 9380 domain-separation tag for the live-set
// hash-to-curve. The suite is ristretto255_XMD:SHA-512_R255MAP_RO_.
const liveSetDST = "devshard-live-set-v1-ristretto255_XMD:SHA-512_R255MAP_RO_"

// frameEntry is the bytes the live-set commitment hashes: protowire tag 1,
// the varint length, then the canonical protobuf.
func frameEntry(entry []byte) []byte {
	framed := make([]byte, 0, len(entry)+4)
	framed = protowire.AppendTag(framed, 1, protowire.BytesType)
	framed = protowire.AppendVarint(framed, uint64(len(entry)))
	return append(framed, entry...)
}

// liveEntryPoint hashes one framed entry onto Ristretto255.
// expand_message_xmd(SHA-512) produces 64 uniform bytes, and
// ristretto255_from_uniform_bytes maps those bytes to a point.
func liveEntryPoint(entry []byte) *ristretto255.Element {
	uniform := expandMessageXMD(sha512.New, sha512.BlockSize, frameEntry(entry), []byte(liveSetDST), 64)
	return ristretto255.NewElement().FromUniformBytes(uniform)
}

// sumLivePointsFromEntries is the live-set commitment: one point per id,
// added in the Ristretto255 group. An empty map encodes as 32 zero bytes,
// the group identity. Order does not matter.
func sumLivePointsFromEntries(entries map[uint64][]byte) [32]byte {
	acc := ristretto255.NewElement()
	for _, entry := range entries {
		acc.Add(acc, liveEntryPoint(entry))
	}
	return encodeLivePoint(acc)
}

func addLivePoint(sum [32]byte, entry []byte) [32]byte {
	acc := decodeLivePoint(sum)
	acc.Add(acc, liveEntryPoint(entry))
	return encodeLivePoint(acc)
}

func subLivePoint(sum [32]byte, entry []byte) [32]byte {
	acc := decodeLivePoint(sum)
	acc.Subtract(acc, liveEntryPoint(entry))
	return encodeLivePoint(acc)
}

func encodeLivePoint(e *ristretto255.Element) [32]byte {
	var buf [32]byte
	encoded := e.Encode(buf[:0])
	if len(encoded) != 32 {
		panic("devshard live-set commitment: point encoding is not 32 bytes")
	}
	var out [32]byte
	copy(out[:], encoded)
	return out
}

func decodeLivePoint(sum [32]byte) *ristretto255.Element {
	e := ristretto255.NewElement()
	if err := e.Decode(sum[:]); err != nil {
		panic("devshard live-set commitment: " + err.Error())
	}
	return e
}

// expandMessageXMD is RFC 9380 expand_message_xmd. newHash is H, and
// blockSize is H's input block size in bytes (64 for SHA-256, 128 for SHA-512).
func expandMessageXMD(newHash func() hash.Hash, blockSize int, msg, dst []byte, outLen int) []byte {
	probe := newHash()
	b := probe.Size()
	if outLen < 0 || outLen > 65535 || len(dst) > 255 || blockSize < b {
		panic("expand_message_xmd: invalid parameters")
	}
	ell := 0
	if outLen > 0 {
		ell = (outLen + b - 1) / b
	}
	if ell > 255 {
		panic("expand_message_xmd: output too long")
	}

	dstPrime := make([]byte, len(dst)+1)
	copy(dstPrime, dst)
	dstPrime[len(dst)] = byte(len(dst))

	msgPrime := make([]byte, blockSize+len(msg)+3+len(dstPrime))
	off := blockSize
	copy(msgPrime[off:], msg)
	off += len(msg)
	msgPrime[off] = byte(outLen >> 8)
	msgPrime[off+1] = byte(outLen)
	copy(msgPrime[off+3:], dstPrime)

	h := newHash()
	h.Write(msgPrime)
	b0 := h.Sum(nil)

	out := make([]byte, ell*b)
	var prev []byte
	for i := 1; i <= ell; i++ {
		h.Reset()
		if i == 1 {
			h.Write(b0)
		} else {
			xored := make([]byte, b)
			for j := 0; j < b; j++ {
				xored[j] = b0[j] ^ prev[j]
			}
			h.Write(xored)
		}
		h.Write([]byte{byte(i)})
		h.Write(dstPrime)
		prev = h.Sum(nil)
		copy(out[(i-1)*b:], prev)
	}
	return out[:outLen]
}
