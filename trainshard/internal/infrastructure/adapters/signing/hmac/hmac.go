package hmac

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"

	"trainshard/internal/domain/shared/vo"
)

var errSignature = errors.New("signature does not match the shared secret")

type Shared struct {
	secret []byte
	actor  vo.Address
}

func New(secret []byte, actor vo.Address) *Shared {
	return &Shared{secret: secret, actor: actor}
}

func (s *Shared) Sign(payload []byte) []byte {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write(payload)
	return mac.Sum(nil)
}

func (s *Shared) Attest(_ context.Context, payload []byte) ([]byte, error) {
	return s.Sign(payload), nil
}

func (s *Shared) Recover(payload, signature []byte) (vo.Address, error) {
	if !hmac.Equal(signature, s.Sign(payload)) {
		return "", errSignature
	}
	return s.actor, nil
}
