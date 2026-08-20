package hmac

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"

	"trainshard/internal/domain/shared/vo"
)

var errSignature = errors.New("signature does not match the shared secret")

const separator = 0

// Shared stands in for the dAPI until real signing is wired up; the secret is symmetric, so
// whoever holds it can sign as any address and it is worth no more than the hosts sharing it
type Shared struct {
	secret  []byte
	address vo.Address
}

func New(secret []byte, address vo.Address) *Shared {
	return &Shared{secret: secret, address: address}
}

func (s *Shared) Sign(payload []byte) []byte {
	signature := append([]byte(s.address), separator)
	return append(signature, s.mac(s.address, payload)...)
}

func (s *Shared) Attest(_ context.Context, payload []byte) ([]byte, error) {
	return s.Sign(payload), nil
}

func (s *Shared) Recover(payload, signature []byte) (vo.Address, error) {
	signed, mac, found := bytes.Cut(signature, []byte{separator})
	if !found {
		return "", errSignature
	}
	address := vo.Address(signed)
	if !hmac.Equal(mac, s.mac(address, payload)) {
		return "", errSignature
	}
	return address, nil
}

func (s *Shared) mac(address vo.Address, payload []byte) []byte {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write(append([]byte(address), separator))
	mac.Write(payload)
	return mac.Sum(nil)
}
