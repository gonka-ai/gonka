package hosts

import (
	"errors"
	"testing"

	"trainshard/internal/domain/shared/vo"
)

func TestBaseURLIsTheEndpointTheChainNames(t *testing.T) {
	alice := vo.Participant("gonka1alice")

	cases := []struct {
		name string
		host vo.Host
		want string
		err  error
	}{
		{name: "the endpoint from the chain", host: vo.Host{Participant: alice, Endpoint: "https://gpu2.alice.example/trainshard-node2"}, want: "https://gpu2.alice.example/trainshard-node2"},
		{name: "no endpoint is an unknown host", host: vo.Host{Participant: alice}, err: errUnknownHost},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := baseURL(tc.host)
			if !errors.Is(err, tc.err) {
				t.Fatalf("got err %v, want %v", err, tc.err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

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
