package broker

import "testing"

// Node.Endpoint() must reproduce the legacy *UrlWithVersion output for Host-Port
// nodes, so routing callers through the mlnode.Endpoint seam preserves behavior.
func TestNode_Endpoint_HostPortMatchesLegacy(t *testing.T) {
	node := Node{Host: "1.2.3.4", PoCPort: 8080, InferencePort: 5000}

	ep := node.Endpoint()

	if got, want := ep.PoCURL("v1"), node.PoCUrlWithVersion("v1"); got != want {
		t.Errorf("PoCURL = %q, want legacy %q", got, want)
	}
	if got, want := ep.InferenceURL("v1"), node.InferenceUrlWithVersion("v1"); got != want {
		t.Errorf("InferenceURL = %q, want legacy %q", got, want)
	}
}

// A Node carrying a BaseURL produces a BaseURL-mode endpoint with the auth token.
func TestNode_Endpoint_BaseURLMode(t *testing.T) {
	node := Node{BaseURL: "http://svc.provider.com/path/", AuthToken: "tok"}

	ep := node.Endpoint()

	if got, want := ep.PoCURL(""), "http://svc.provider.com/path"; got != want {
		t.Errorf("PoCURL(\"\") = %q, want %q", got, want)
	}
	if got := ep.AuthToken(); got != "tok" {
		t.Errorf("AuthToken() = %q, want %q", got, "tok")
	}
}
