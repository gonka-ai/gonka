package types

import "encoding/binary"

// MaxGroupSize is the maximum number of slots in a session group.
const MaxGroupSize = 128

// Bitmap128 is a 128-bit bitmap stored as two uint64 words.
// Value type: struct copies work without deep clone.
type Bitmap128 [2]uint64

// Set sets the bit at position bit. No-op if bit >= MaxGroupSize.
func (b *Bitmap128) Set(bit uint32) {
	if bit >= MaxGroupSize {
		return
	}
	b[bit/64] |= 1 << (bit % 64)
}

// IsSet returns true if the bit at position bit is set.
// Returns false if bit >= MaxGroupSize.
func (b Bitmap128) IsSet(bit uint32) bool {
	if bit >= MaxGroupSize {
		return false
	}
	return (b[bit/64]>>(bit%64))&1 == 1
}

// Bytes returns the bitmap as a 16-byte little-endian slice.
func (b Bitmap128) Bytes() []byte {
	buf := make([]byte, 16)
	binary.LittleEndian.PutUint64(buf[:8], b[0])
	binary.LittleEndian.PutUint64(buf[8:], b[1])
	return buf
}

// Bitmap128FromBytes reconstructs a Bitmap128 from a 16-byte little-endian slice.
func Bitmap128FromBytes(data []byte) Bitmap128 {
	var b Bitmap128
	if len(data) >= 16 {
		b[0] = binary.LittleEndian.Uint64(data[:8])
		b[1] = binary.LittleEndian.Uint64(data[8:])
	}
	return b
}
