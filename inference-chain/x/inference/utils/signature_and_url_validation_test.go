package utils

import (
	"net"
	"testing"

	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/stretchr/testify/require"
)

func TestValidateURLWithSSRFProtection(t *testing.T) {
	t.Run("valid_public_url", func(t *testing.T) {
		err := ValidateURLWithSSRFProtection("inference_url", "https://example.com")
		require.NoError(t, err)
	})

	t.Run("reject_localhost", func(t *testing.T) {
		err := ValidateURLWithSSRFProtection("inference_url", "http://localhost:8080")
		require.ErrorIs(t, err, sdkerrors.ErrInvalidRequest)
	})

	t.Run("reject_private_ipv4", func(t *testing.T) {
		err := ValidateURLWithSSRFProtection("inference_url", "http://192.168.0.1")
		require.ErrorIs(t, err, sdkerrors.ErrInvalidRequest)
	})

	// Registration gate is intentionally DNS-free (stateless ValidateBasic must
	// be deterministic), so a hostname always passes here regardless of what it
	// resolves to. The real defense is the dial-time guard; see IsPrivateIP tests.
	t.Run("accept_hostname_no_dns_resolution", func(t *testing.T) {
		require.NoError(t, ValidateURLWithSSRFProtection("inference_url", "http://ssrf.attacker.tld"))
		require.NoError(t, ValidateURLWithSSRFProtection("inference_url", "http://localtest.me"))
	})
}

func TestIsPrivateIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1",              // loopback
		"127.5.6.7",              // loopback /8
		"::1",                    // IPv6 loopback
		"10.0.0.1",               // RFC1918 10/8
		"172.16.0.1",             // RFC1918 172.16/12
		"172.31.255.255",         // RFC1918 172.16/12 upper
		"192.168.1.1",            // RFC1918 192.168/16
		"169.254.169.254",        // cloud metadata / link-local
		"169.254.0.1",            // link-local
		"0.0.0.0",                // unspecified
		"fe80::1",                // IPv6 link-local
		"fc00::1",                // IPv6 ULA
		"fd12:3456::1",           // IPv6 ULA
		"::ffff:127.0.0.1",       // IPv4-mapped IPv6 loopback
		"::ffff:169.254.169.254", // IPv4-mapped IPv6 metadata
		"::ffff:10.0.0.1",        // IPv4-mapped IPv6 RFC1918
	}
	for _, s := range blocked {
		ip := net.ParseIP(s)
		require.NotNil(t, ip, "parse %s", s)
		require.True(t, IsPrivateIP(ip), "expected %s to be private/blocked", s)
	}

	allowed := []string{
		"8.8.8.8",
		"1.1.1.1",
		"93.184.216.34",     // example.com
		"2606:2800:220:1::", // public IPv6
	}
	for _, s := range allowed {
		ip := net.ParseIP(s)
		require.NotNil(t, ip, "parse %s", s)
		require.False(t, IsPrivateIP(ip), "expected %s to be public/allowed", s)
	}
}
