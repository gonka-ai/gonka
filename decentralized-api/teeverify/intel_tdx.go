package teeverify

import (
	"context"
	"errors"
)

const IntelTDXProvider = "intel-tdx"

type IntelTDXVerifier struct{}

func (IntelTDXVerifier) Provider() string { return IntelTDXProvider }

func (IntelTDXVerifier) Verify(_ context.Context, _ VerifyRequest) (*Report, error) {
	return nil, ErrIntelTDXNotImplemented
}

var ErrIntelTDXNotImplemented = errors.New("teeverify(intel-tdx): strict verifier not implemented yet; use intel-tdx-lite or wait for the PCS-backed verifier")
