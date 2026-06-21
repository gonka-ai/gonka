// Partial SSRF mitigation: validates hostname resolves to a public IP at check time.
// TODO(security): does not defend against DNS-rebinding attacks (short-TTL records returning
// private IPs at dial time after public IP at check time). Closing this requires a pinned-IP
// custom Dialer in the http.Client — tracked as a follow-up.
package public

import (
	"fmt"
	"net"
	"net/url"
)

// privateIPBlocks contains CIDR ranges that should not be reachable
// from the public API to prevent SSRF attacks.
var privateIPBlocks []*net.IPNet

func init() {
	for _, cidr := range []string{
		"127.0.0.0/8",    // IPv4 loopback
		"10.0.0.0/8",     // RFC1918
		"172.16.0.0/12",  // RFC1918
		"192.168.0.0/16", // RFC1918
		"169.254.0.0/16", // Link-local
		"0.0.0.0/8",      // Current network
		"100.64.0.0/10",  // Shared address space (CGN)
		"::1/128",        // IPv6 loopback
		"fc00::/7",       // IPv6 unique local
		"fe80::/10",      // IPv6 link-local
		"::ffff:0:0/96",  // IPv4-mapped IPv6 (catches e.g. ::ffff:7f00:1 = 127.0.0.1)
	} {
		_, block, _ := net.ParseCIDR(cidr)
		privateIPBlocks = append(privateIPBlocks, block)
	}
}

func isPrivateIP(ip net.IP) bool {
	for _, block := range privateIPBlocks {
		if block.Contains(ip) {
			return true
		}
	}
	return false
}

// ValidateExecutorURL checks that a URL from chain state does not point to
// private/internal networks. This prevents SSRF via malicious executor
// registrations (e.g., http://169.254.169.254 for cloud metadata).
func ValidateExecutorURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid executor URL: %w", err)
	}

	host := parsed.Hostname()
	if host == "" {
		return fmt.Errorf("executor URL has no host: %s", rawURL)
	}

	ips, err := net.LookupHost(host)
	if err != nil {
		return fmt.Errorf("cannot resolve executor host %q: %w", host, err)
	}

	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			continue
		}
		if isPrivateIP(ip) {
			return fmt.Errorf("executor URL %q resolves to private IP %s", rawURL, ipStr)
		}
	}

	return nil
}
