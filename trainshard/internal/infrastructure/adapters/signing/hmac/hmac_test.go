package hmac_test

import (
	"testing"

	"trainshard/internal/domain/shared/vo"
	"trainshard/internal/infrastructure/adapters/signing/hmac"
)

var secret = []byte("shared-secret")

func TestRecoverNamesWhoeverSignedRatherThanWhoeverVerifies(t *testing.T) {
	// arrange
	host := hmac.New(secret, "gonka1host")
	coordinator := hmac.New(secret, "gonka1creator")
	payload := []byte("mesh identity")

	// act
	signed, err := coordinator.Recover(payload, host.Sign(payload))

	// assert
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if signed != vo.Address("gonka1host") {
		t.Fatalf("got %q, want the host that signed, not the party that verified", signed)
	}
}

func TestRecoverRefusesASignatureMadeUnderAnotherSecret(t *testing.T) {
	// arrange
	outsider := hmac.New([]byte("another-secret"), "gonka1host")
	verifier := hmac.New(secret, "")
	payload := []byte("mesh identity")

	// act
	_, err := verifier.Recover(payload, outsider.Sign(payload))

	// assert
	if err == nil {
		t.Fatal("got no error, want a signature made under another secret refused")
	}
}

func TestRecoverRefusesASignatureWhoseAddressWasSwapped(t *testing.T) {
	// arrange
	host := hmac.New(secret, "gonka1host")
	verifier := hmac.New(secret, "")
	payload := []byte("mesh identity")

	// act
	signature := host.Sign(payload)
	_, mac, _ := cut(signature)
	_, err := verifier.Recover(payload, append([]byte("gonka1thief\x00"), mac...))

	// assert
	if err == nil {
		t.Fatal("got no error, want the address refused unless the mac covers it")
	}
}

func cut(signature []byte) (address, mac []byte, found bool) {
	for i, b := range signature {
		if b == 0 {
			return signature[:i], signature[i+1:], true
		}
	}
	return signature, nil, false
}
