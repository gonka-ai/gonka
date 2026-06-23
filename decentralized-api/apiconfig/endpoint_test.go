package apiconfig

import "testing"

// Normalize must strip surrounding whitespace and any trailing slash from BaseURL
// so the stored/validated/endpoint value is canonical. Regression for the seam
// where Validate trimmed but the URL builder did not: a stray-whitespace base_url
// used to validate yet build a space-laced URL (and dodge the duplicate check).
func TestInferenceNodeConfig_Normalize_BaseURL(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"  http://svc.provider.com/path/  ", "http://svc.provider.com/path"},
		{"http://svc.provider.com/path/", "http://svc.provider.com/path"},
		{"\thttp://host:8080\n", "http://host:8080"},
		{"", ""}, // Host-Port node: empty BaseURL stays empty
	}
	for _, c := range cases {
		cfg := InferenceNodeConfig{BaseURL: c.in}
		cfg.Normalize()
		if cfg.BaseURL != c.want {
			t.Errorf("Normalize(%q): BaseURL = %q, want %q", c.in, cfg.BaseURL, c.want)
		}
	}
}

// After Normalize, a whitespace-padded base_url both passes validation and builds
// a clean URL — the two must agree on the same canonical value.
func TestInferenceNodeConfig_Normalize_ValidateAndEndpointAgree(t *testing.T) {
	cfg := InferenceNodeConfig{
		Id:            "n1",
		BaseURL:       "  http://svc.provider.com/path/  ",
		MaxConcurrent: 1,
		Models:        map[string]ModelConfig{"m": {}},
	}
	cfg.Normalize()

	if errs := ValidateInferenceNodeBasic(cfg); len(errs) > 0 {
		t.Fatalf("ValidateInferenceNodeBasic after Normalize = %v, want no errors", errs)
	}
	if got, want := cfg.Endpoint().PoCURL("v1"), "http://svc.provider.com/path/v1"; got != want {
		t.Errorf("PoCURL(\"v1\") = %q, want %q (no stray whitespace)", got, want)
	}
}

// Endpoint() must reproduce the legacy MLNodeURL output for Host-Port nodes, so
// routing callers through the mlnode.Endpoint seam preserves behavior.
func TestInferenceNodeConfig_Endpoint_HostPortMatchesLegacy(t *testing.T) {
	cfg := InferenceNodeConfig{Host: "1.2.3.4", PoCPort: 8080, InferencePort: 5000}

	ep := cfg.Endpoint()

	if got, want := ep.PoCURL("v1"), MLNodeURL(cfg.Host, cfg.PoCPort, cfg.PoCSegment, "v1"); got != want {
		t.Errorf("PoCURL = %q, want legacy %q", got, want)
	}
	if got, want := ep.InferenceURL("v1"), MLNodeURL(cfg.Host, cfg.InferencePort, cfg.InferenceSegment, "v1"); got != want {
		t.Errorf("InferenceURL = %q, want legacy %q", got, want)
	}
}

// A node configured with a BaseURL produces a BaseURL-mode endpoint carrying the
// auth token, not a Host-Port endpoint.
func TestInferenceNodeConfig_Endpoint_BaseURLMode(t *testing.T) {
	cfg := InferenceNodeConfig{BaseURL: "http://svc.provider.com/path/", AuthToken: "tok"}

	ep := cfg.Endpoint()

	if got, want := ep.PoCURL(""), "http://svc.provider.com/path"; got != want {
		t.Errorf("PoCURL(\"\") = %q, want %q", got, want)
	}
	if got := ep.AuthToken(); got != "tok" {
		t.Errorf("AuthToken() = %q, want %q", got, "tok")
	}
}
