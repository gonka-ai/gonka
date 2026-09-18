package vo

import (
	"net/url"
	"strconv"
	"strings"

	"trainshard/internal/domain/shared"
)

const maxEndpointLen = 256

var ErrEndpointInvalid = shared.New("ENDPOINT_INVALID", shared.ErrValidation, "endpoint must be an absolute http(s) base: scheme, host, optional port in 1..65535 and path, no trailing slash")

// Endpoint is where a daemon answers a coordinator, as the host published it on chain. A request
// path is appended to it as is, so it carries nothing a signed path could not: no query, no
// fragment, no credentials
type Endpoint string

func ParseEndpoint(raw string) (Endpoint, error) {
	if raw == "" {
		return "", nil
	}
	if len(raw) > maxEndpointLen {
		return "", ErrEndpointInvalid
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", ErrEndpointInvalid
	}
	switch {
	case parsed.Scheme != "http" && parsed.Scheme != "https",
		parsed.Hostname() == "",
		parsed.User != nil, parsed.Opaque != "", strings.ContainsAny(raw, "?#"),
		strings.HasSuffix(parsed.Path, "/"),
		strings.HasSuffix(parsed.Host, ":"), !validPort(parsed.Port()):
		return "", ErrEndpointInvalid
	}
	return Endpoint(raw), nil
}

func validPort(port string) bool {
	if port == "" {
		return true
	}
	n, err := strconv.Atoi(port)
	return err == nil && n >= 1 && n <= 65535
}

func (e Endpoint) IsZero() bool { return e == "" }
