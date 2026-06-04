package types

import "testing"

func TestBitmap128_SetIsSet(t *testing.T) {
	var b Bitmap128
	for _, bit := range []uint32{0, 1, 63, 64, 65, 127} {
		if b.IsSet(bit) {
			t.Fatalf("bit %d should not be set on zero bitmap", bit)
		}
		b.Set(bit)
		if !b.IsSet(bit) {
			t.Fatalf("bit %d should be set after Set", bit)
		}
	}
	// Bits not set should remain unset.
	for _, bit := range []uint32{2, 62, 66, 126} {
		if b.IsSet(bit) {
			t.Fatalf("bit %d should not be set", bit)
		}
	}
}

func TestBitmap128_ZeroValue(t *testing.T) {
	var b Bitmap128
	for i := uint32(0); i < MaxGroupSize; i++ {
		if b.IsSet(i) {
			t.Fatalf("bit %d set on zero value", i)
		}
	}
}

func TestBitmap128_Bytes_Roundtrip(t *testing.T) {
	var b Bitmap128
	b.Set(0)
	b.Set(63)
	b.Set(64)
	b.Set(127)

	data := b.Bytes()
	if len(data) != 16 {
		t.Fatalf("expected 16 bytes, got %d", len(data))
	}

	b2 := Bitmap128FromBytes(data)
	if b != b2 {
		t.Fatalf("roundtrip mismatch: %v != %v", b, b2)
	}
}

func TestBitmap128_SetBits(t *testing.T) {
	var b Bitmap128
	if got := b.SetBits(); got != nil {
		t.Fatalf("zero bitmap SetBits: want nil, got %v", got)
	}

	b.Set(0)
	b.Set(5)
	b.Set(63)
	b.Set(64)
	b.Set(127)
	want := []uint32{0, 5, 63, 64, 127}
	got := b.SetBits()
	if len(got) != len(want) {
		t.Fatalf("SetBits length: want %d, got %d", len(want), len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("SetBits[%d]: want %d, got %d", i, want[i], got[i])
		}
	}
}

func TestBitmap128_OutOfBounds(t *testing.T) {
	var b Bitmap128
	// Set and IsSet should be safe for out-of-range bits.
	b.Set(128)
	b.Set(200)
	if b.IsSet(128) {
		t.Fatal("bit 128 should not be settable")
	}
	if b.IsSet(200) {
		t.Fatal("bit 200 should not be settable")
	}
	// Bitmap should still be zero.
	if b != (Bitmap128{}) {
		t.Fatal("bitmap should be zero after out-of-bounds Set")
	}
}
