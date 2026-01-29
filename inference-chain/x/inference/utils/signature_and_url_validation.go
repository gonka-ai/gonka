package utils

import (
	"encoding/base64"
	"net"
	"net/url"
	"strings"

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

// ValidateURLWithSSRFProtection validates URL format and rejects private/internal addresses
// to prevent SSRF attacks. This should be used for participant-controlled URLs.
func ValidateURLWithSSRFProtection(fieldName, raw string) error {
	if err := ValidateURL(fieldName, raw); err != nil {
		return err
	}

	u, _ := url.Parse(raw) // Already validated above
	host := u.Hostname()

	// Check for localhost variants
	if isLocalhost(host) {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "%s cannot point to localhost", fieldName)
	}

	// Parse as IP and check for private ranges
	ip := net.ParseIP(host)
	if ip != nil {
		if isPrivateIP(ip) {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "%s cannot point to private IP address", fieldName)
		}
	}

	return nil
}

// isLocalhost checks if the host is a localhost variant
func isLocalhost(host string) bool {
	host = strings.ToLower(host)
	return host == "localhost" ||
		host == "127.0.0.1" ||
		host == "::1" ||
		host == "[::1]" ||
		strings.HasPrefix(host, "127.") ||
		host == "0.0.0.0"
}

// isPrivateIP checks if an IP address is in a private/internal range
func isPrivateIP(ip net.IP) bool {
	// Loopback (127.0.0.0/8, ::1)
	if ip.IsLoopback() {
		return true
	}

	// Link-local unicast (169.254.0.0/16, fe80::/10)
	if ip.IsLinkLocalUnicast() {
		return true
	}

	// Link-local multicast
	if ip.IsLinkLocalMulticast() {
		return true
	}

	// Private IPv4 ranges
	if ip4 := ip.To4(); ip4 != nil {
		// 10.0.0.0/8
		if ip4[0] == 10 {
			return true
		}
		// 172.16.0.0/12
		if ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31 {
			return true
		}
		// 192.168.0.0/16
		if ip4[0] == 192 && ip4[1] == 168 {
			return true
		}
		// 0.0.0.0
		if ip4[0] == 0 && ip4[1] == 0 && ip4[2] == 0 && ip4[3] == 0 {
			return true
		}
		// AWS metadata endpoint 169.254.169.254
		if ip4[0] == 169 && ip4[1] == 254 {
			return true
		}
	}

	// Private IPv6: fc00::/7 (Unique Local Addresses)
	if len(ip) == net.IPv6len && (ip[0]&0xfe) == 0xfc {
		return true
	}

	return false
}
