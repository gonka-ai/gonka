package devshard

import (
	"context"
	"errors"
)

// Sentinel errors from EncryptedEngine.ForwardEncrypted
var (
	EncryptedBadEnvelope = errors.New("encrypted: bad envelope")
	EncryptedKeyUnknown  = errors.New("encrypted: recipient key id unknown on this host")
	EncryptedNoCapacity  = errors.New("encrypted: no capacity for target recipient key")
)

// InferenceEngine executes inference on an ML node.
// Implemented by dapi using existing broker + completionapi.
type InferenceEngine interface {
	Execute(ctx context.Context, req ExecuteRequest) (*ExecuteResult, error)
}

// ValidationEngine re-executes inference and compares logits.
// Implemented by dapi using existing broker + completionapi.
type ValidationEngine interface {
	Validate(ctx context.Context, req ValidateRequest) (*ValidateResult, error)
}

// EncryptedEngine forwards opaque HPKE envelopes to an ML node
// Host must not decrypt the payload
type EncryptedEngine interface {
	ForwardEncrypted(ctx context.Context, body []byte) ([]byte, error)
}
