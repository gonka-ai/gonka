package hosts

import "testing"

func TestHostAddressKeepsTheSchemeSoAShellIsNotSentInTheClear(t *testing.T) {
	cases := []struct {
		name    string
		base    string
		address string
		secure  bool
	}{
		{name: "https without a port is tls on 443", base: "https://host.example", address: "host.example:443", secure: true},
		{name: "https with a port is still tls", base: "https://host.example:8443", address: "host.example:8443", secure: true},
		{name: "http without a port is plain on 80", base: "http://host.example", address: "host.example:80"},
		{name: "http with a port is plain", base: "http://127.0.0.1:9700", address: "127.0.0.1:9700"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			address, secure, err := hostAddress(tc.base)
			if err != nil {
				t.Fatalf("host address: %v", err)
			}
			if address != tc.address || secure != tc.secure {
				t.Fatalf("got %q secure=%t, want %q secure=%t", address, secure, tc.address, tc.secure)
			}
		})
	}
}
