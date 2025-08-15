package utils

import (
	"encoding/base64"
	"net/url"

	errorsmod "cosmossdk.io/errors"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

// ValidateBase64RSig64 validates that the provided string is base64-encoded
// and decodes to exactly 64 bytes, representing r||s concatenated signature bytes.
// This is curve-agnostic and matches the spec that signatures are bytes of r + s, padded as needed.
func ValidateBase64RSig64(fieldName, sigB64 string) error {
	if len(sigB64) == 0 {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "%s is required", fieldName)
	}
	b, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "%s must be base64: %v", fieldName, err)
	}
	if len(b) != 64 {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "%s must decode to 64 bytes (r||s), got %d bytes", fieldName, len(b))
	}
	return nil
}

// ValidateURL enforces a basic HTTP/HTTPS URL format with a non-empty host.
// It is purely syntactic validation (no network calls) and deterministic.
func ValidateURL(fieldName, raw string) error {
	if len(raw) == 0 {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "%s is required", fieldName)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "invalid %s: %v", fieldName, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "%s must have scheme http or https", fieldName)
	}
	if u.Host == "" {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "%s must include a host for %s", fieldName, raw)
	}
	return nil
}
