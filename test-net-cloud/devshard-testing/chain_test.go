package main

import "testing"

const testPrivKeyHex = "0101010101010101010101010101010101010101010101010101010101010101"
const testExpectedAddr = "gonka10xcqpzrky6eff2g52qdye53xkk9jxkvr34az8p"

func TestDeriveAddress(t *testing.T) {
	addr, err := deriveAddress(testPrivKeyHex)
	if err != nil {
		t.Fatalf("deriveAddress: %v", err)
	}
	if addr != testExpectedAddr {
		t.Errorf("got %q, want %q", addr, testExpectedAddr)
	}
	// deterministic
	addr2, _ := deriveAddress(testPrivKeyHex)
	if addr != addr2 {
		t.Errorf("non-deterministic: %q != %q", addr, addr2)
	}
}

func TestDeriveAddressInvalidHex(t *testing.T) {
	_, err := deriveAddress("not-hex")
	if err == nil {
		t.Fatal("expected error for invalid hex")
	}
}
