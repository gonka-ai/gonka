package blocks

import (
	"crypto/sha256"
	"encoding/binary"
)

// CanonicalHeaderBytes is the deterministic byte encoding of a Header that
// all producers sign and all consumers verify.
//
// The Commit field is intentionally excluded: signatures inside the Commit
// are computed over these bytes, so the Commit cannot be part of the signed
// payload. Every other field is bound.
//
// Stability: once shipped, changes to this encoding require a version bump
// of the blockoracle wire protocol. See §3.2 in devshard/docs/testenv.md.
func CanonicalHeaderBytes(h *Header) []byte {
	if h == nil {
		return nil
	}
	var buf []byte
	buf = appendString(buf, h.ChainID)
	buf = appendInt64(buf, h.Height)
	buf = appendInt64(buf, h.Time.UnixNano())
	buf = appendBytes(buf, h.BlockHash)
	buf = appendBytes(buf, h.AppHash)
	buf = appendBytes(buf, h.ValidatorsHash)
	buf = appendBytes(buf, h.NextValidatorsHash)
	return buf
}

// CanonicalHeaderDigest returns SHA-256 over CanonicalHeaderBytes.
func CanonicalHeaderDigest(h *Header) [32]byte {
	return sha256.Sum256(CanonicalHeaderBytes(h))
}

func appendString(dst []byte, s string) []byte {
	dst = appendUint32(dst, uint32(len(s)))
	return append(dst, s...)
}

func appendBytes(dst []byte, b []byte) []byte {
	dst = appendUint32(dst, uint32(len(b)))
	return append(dst, b...)
}

func appendInt64(dst []byte, v int64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(v))
	return append(dst, b[:]...)
}

func appendUint32(dst []byte, v uint32) []byte {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	return append(dst, b[:]...)
}
