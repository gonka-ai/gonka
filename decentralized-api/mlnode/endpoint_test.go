package mlnode

import "testing"

// Host-Port mode must reproduce the legacy apiconfig.MLNodeURL output exactly,
// so introducing the Endpoint seam is a behavior-preserving change.
func TestHostPortEndpoint_PoCURL_Unversioned(t *testing.T) {
	ep := New(Spec{Host: "1.2.3.4", PoCPort: 8080, InferencePort: 5000})

	got := ep.PoCURL("")
	want := "http://1.2.3.4:8080"
	if got != want {
		t.Errorf("PoCURL(\"\") = %q, want %q", got, want)
	}
}

// Versioned URLs insert the node version into the path (nginx rolling-upgrade
// routing) — must match legacy MLNodeURL("http://host:port/version").
func TestHostPortEndpoint_InferenceURL_Versioned(t *testing.T) {
	ep := New(Spec{Host: "1.2.3.4", PoCPort: 8080, InferencePort: 5000})

	got := ep.InferenceURL("v1.2.3")
	want := "http://1.2.3.4:5000/v1.2.3"
	if got != want {
		t.Errorf("InferenceURL(\"v1.2.3\") = %q, want %q", got, want)
	}
}

// Host-Port health is the inference URL + /health (today's
// url.JoinPath(inferenceUrl, "/health")).
func TestHostPortEndpoint_HealthURL(t *testing.T) {
	ep := New(Spec{Host: "1.2.3.4", PoCPort: 8080, InferencePort: 5000})

	got := ep.HealthURL("")
	want := "http://1.2.3.4:5000/health"
	if got != want {
		t.Errorf("HealthURL(\"\") = %q, want %q", got, want)
	}
}

func TestHostPortEndpoint_AuthToken(t *testing.T) {
	ep := New(Spec{Host: "1.2.3.4", PoCPort: 8080, InferencePort: 5000, AuthToken: "secret"})

	if got := ep.AuthToken(); got != "secret" {
		t.Errorf("AuthToken() = %q, want %q", got, "secret")
	}
}

// BaseURL mode: a non-empty BaseURL selects the single-endpoint variant where
// PoCURL == InferenceURL == the base URL (version inserted into the path), and
// health is /readyz. Any trailing slash on the base URL is normalised away.
func TestBaseURLEndpoint_PoCAndInferenceShareBaseURL(t *testing.T) {
	ep := New(Spec{BaseURL: "http://svc.provider.com/path/"})

	if got, want := ep.PoCURL(""), "http://svc.provider.com/path"; got != want {
		t.Errorf("PoCURL(\"\") = %q, want %q", got, want)
	}
	if got, want := ep.InferenceURL(""), "http://svc.provider.com/path"; got != want {
		t.Errorf("InferenceURL(\"\") = %q, want %q", got, want)
	}
}

func TestBaseURLEndpoint_VersionInserted(t *testing.T) {
	ep := New(Spec{BaseURL: "http://svc.provider.com/path/"})

	if got, want := ep.PoCURL("v1.2.3"), "http://svc.provider.com/path/v1.2.3"; got != want {
		t.Errorf("PoCURL(\"v1.2.3\") = %q, want %q", got, want)
	}
}

func TestBaseURLEndpoint_HealthIsReadyz(t *testing.T) {
	ep := New(Spec{BaseURL: "http://svc.provider.com/path/"})

	if got, want := ep.HealthURL(""), "http://svc.provider.com/path/readyz"; got != want {
		t.Errorf("HealthURL(\"\") = %q, want %q", got, want)
	}
}

func TestBaseURLEndpoint_AuthToken(t *testing.T) {
	ep := New(Spec{BaseURL: "http://svc.provider.com/", AuthToken: "tok"})

	if got := ep.AuthToken(); got != "tok" {
		t.Errorf("AuthToken() = %q, want %q", got, "tok")
	}
}

func TestValidate_HostPortValid(t *testing.T) {
	if errs := Validate(Spec{Host: "1.2.3.4", InferencePort: 5000, PoCPort: 8080}); len(errs) != 0 {
		t.Errorf("expected no errors, got %v", errs)
	}
}

func TestValidate_BaseURLValid(t *testing.T) {
	if errs := Validate(Spec{BaseURL: "https://svc.provider.com/path"}); len(errs) != 0 {
		t.Errorf("expected no errors, got %v", errs)
	}
}

func TestValidate_RejectsBothModes(t *testing.T) {
	errs := Validate(Spec{Host: "1.2.3.4", InferencePort: 5000, PoCPort: 8080, BaseURL: "https://svc"})
	if len(errs) == 0 {
		t.Error("expected error when both host+ports and base_url are set")
	}
}

func TestValidate_BaseURLMustBeHTTP(t *testing.T) {
	if errs := Validate(Spec{BaseURL: "ftp://svc.provider.com"}); len(errs) == 0 {
		t.Error("expected error for non-http(s) base_url")
	}
	if errs := Validate(Spec{BaseURL: "not a url"}); len(errs) == 0 {
		t.Error("expected error for malformed base_url")
	}
}

func TestValidate_HostPortMissingHost(t *testing.T) {
	if errs := Validate(Spec{InferencePort: 5000, PoCPort: 8080}); len(errs) == 0 {
		t.Error("expected error when host missing in host-port mode")
	}
}

func TestValidate_HostPortBadPorts(t *testing.T) {
	if errs := Validate(Spec{Host: "h", InferencePort: 0, PoCPort: 70000}); len(errs) == 0 {
		t.Error("expected error for out-of-range ports")
	}
}
