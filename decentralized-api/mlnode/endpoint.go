// Package mlnode models how the API node reaches one MLNode: its address plus
// optional authentication, and the rule for turning those into versioned PoC /
// inference / health URLs. Endpoint is a value object with two mutually-exclusive
// variants (Host-Port, BaseURL). It does pure string assembly and never fails;
// URL validity is checked once at the registration/config boundary, not here.
package mlnode

import (
	"fmt"
	"net/url"
	"strings"
)

// Endpoint is the addressing+auth value object for one MLNode.
type Endpoint interface {
	// PoCURL is the management API base URL, with the node version inserted
	// into the path when non-empty (nginx version routing for rolling upgrades).
	PoCURL(version string) string
	// InferenceURL is the inference base URL (equal to PoCURL in BaseURL mode).
	InferenceURL(version string) string
	// HealthURL is the readiness/health probe URL; its path differs by variant
	// (Host-Port: /health on the inference URL; BaseURL: /readyz).
	HealthURL(version string) string
	// AuthToken is the bearer token to authenticate MLNode requests, or "".
	AuthToken() string
}

// Spec is the flat set of addressing fields an InferenceNodeConfig or broker.Node
// carries. New picks the variant from it.
type Spec struct {
	Host             string
	InferencePort    int
	InferenceSegment string
	PoCPort          int
	PoCSegment       string
	BaseURL          string
	AuthToken        string
}

// Validate checks that s describes exactly one valid addressing mode. It returns
// a list of human-readable problems (empty when valid). This is the boundary
// check that keeps New/Endpoint total: once a Spec validates, URL assembly never
// fails. Auth token is always optional.
func Validate(s Spec) []string {
	var errs []string
	if s.BaseURL != "" {
		// BaseURL mode: host/ports must not also be set (exactly one mode).
		if s.Host != "" || s.InferencePort != 0 || s.PoCPort != 0 ||
			s.InferenceSegment != "" || s.PoCSegment != "" {
			errs = append(errs, "specify either host+ports or base_url, not both")
		}
		u, err := url.Parse(strings.TrimSpace(s.BaseURL))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs = append(errs, fmt.Sprintf("base_url must be a valid http(s) URL, got %q", s.BaseURL))
		}
		return errs
	}
	// Host-Port mode.
	if strings.TrimSpace(s.Host) == "" {
		errs = append(errs, "host is required and cannot be empty")
	}
	if s.InferencePort <= 0 || s.InferencePort > 65535 {
		errs = append(errs, fmt.Sprintf("inference_port must be between 1 and 65535, got %d", s.InferencePort))
	}
	if s.PoCPort <= 0 || s.PoCPort > 65535 {
		errs = append(errs, fmt.Sprintf("poc_port must be between 1 and 65535, got %d", s.PoCPort))
	}
	return errs
}

// New builds the Endpoint variant described by s: BaseURL mode when BaseURL is
// set, otherwise Host-Port mode. Total: it assembles strings and never errors.
func New(s Spec) Endpoint {
	if s.BaseURL != "" {
		return baseURLEndpoint{base: strings.TrimRight(s.BaseURL, "/"), authToken: s.AuthToken}
	}
	return hostPortEndpoint{spec: s}
}

// baseURLEndpoint addresses an MLNode by a single base URL serving both the
// management and inference APIs (single-port). The node version is inserted
// into the path identically to Host-Port mode; health is /readyz.
type baseURLEndpoint struct {
	base      string // trailing slash trimmed
	authToken string
}

func (e baseURLEndpoint) PoCURL(version string) string       { return e.versioned(version) }
func (e baseURLEndpoint) InferenceURL(version string) string { return e.versioned(version) }
func (e baseURLEndpoint) HealthURL(version string) string    { return e.versioned(version) + "/readyz" }
func (e baseURLEndpoint) AuthToken() string                  { return e.authToken }

func (e baseURLEndpoint) versioned(version string) string {
	if version == "" {
		return e.base
	}
	return e.base + "/" + version
}

type hostPortEndpoint struct {
	spec Spec
}

func (e hostPortEndpoint) PoCURL(version string) string {
	return hostPortURL(e.spec.Host, e.spec.PoCPort, e.spec.PoCSegment, version)
}

func (e hostPortEndpoint) InferenceURL(version string) string {
	return hostPortURL(e.spec.Host, e.spec.InferencePort, e.spec.InferenceSegment, version)
}

func (e hostPortEndpoint) HealthURL(version string) string {
	return e.InferenceURL(version) + "/health"
}

func (e hostPortEndpoint) AuthToken() string {
	return e.spec.AuthToken
}

// hostPortURL reproduces the legacy apiconfig.MLNodeURL shaping exactly.
func hostPortURL(host string, port int, segment, version string) string {
	if version == "" {
		return fmt.Sprintf("http://%s:%d%s", host, port, segment)
	}
	return fmt.Sprintf("http://%s:%d/%s%s", host, port, version, segment)
}
