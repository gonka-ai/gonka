package devshard

import (
	"testing"

	"devshard/types"
)

func TestNormalizeRoutePrefixDefaultsToLegacy(t *testing.T) {
	if got := NormalizeRoutePrefix(""); got != LegacyRoutePrefix {
		t.Fatalf("NormalizeRoutePrefix(\"\") = %q, want %q", got, LegacyRoutePrefix)
	}
}

func TestResolveVersionedRoutePrefix(t *testing.T) {
	if got := ResolveVersionedRoutePrefix("v1", ""); got != VersionedRoutePrefix("v1") {
		t.Fatalf("ResolveVersionedRoutePrefix(\"v1\", \"\") = %q, want %q", got, VersionedRoutePrefix("v1"))
	}
	if got := ResolveVersionedRoutePrefix("v1", LegacyRoutePrefix); got != LegacyRoutePrefix {
		t.Fatalf("ResolveVersionedRoutePrefix override = %q, want %q", got, LegacyRoutePrefix)
	}
}

func TestResolveHostRoutePrefix(t *testing.T) {
	if got := ResolveHostRoutePrefix(types.ProtocolV1, ""); got != LegacyRoutePrefix {
		t.Fatalf("ResolveHostRoutePrefix(v1) = %q, want %q", got, LegacyRoutePrefix)
	}
	if got := ResolveHostRoutePrefix(types.ProtocolV1, LegacyRoutePrefix); got != LegacyRoutePrefix {
		t.Fatalf("ResolveHostRoutePrefix override = %q, want %q", got, LegacyRoutePrefix)
	}
}

func TestProtocolSessionVersion(t *testing.T) {
	if got := ProtocolSessionVersion(types.ProtocolV1); got != "v1" {
		t.Fatalf("ProtocolSessionVersion(v1) = %q, want %q", got, "v1")
	}
	if got := ProtocolSessionVersion("v1"); got != "v1" {
		t.Fatalf("ProtocolSessionVersion(route-style v1) = %q, want %q", got, "v1")
	}
	if got := ProtocolSessionVersion(""); got != "v1" {
		t.Fatalf("ProtocolSessionVersion(\"\") = %q, want %q", got, "v1")
	}
}

func TestVersionForRoutePrefix(t *testing.T) {
	tests := []struct {
		name        string
		routePrefix string
		want        string
		wantErr     bool
	}{
		{
			name:        "default legacy",
			routePrefix: "",
			want:        "v1",
		},
		{
			name:        "explicit legacy",
			routePrefix: LegacyRoutePrefix,
			want:        "v1",
		},
		{
			name:        "old subnet host route rejected",
			routePrefix: "/v1/subnet",
			wantErr:     true,
		},
		{
			name:        "versioned",
			routePrefix: VersionedRoutePrefix("v2.1.0"),
			want:        "v2.1.0",
		},
		{
			name:        "invalid",
			routePrefix: "/devshard",
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := VersionForRoutePrefix(tt.routePrefix)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("VersionForRoutePrefix(%q) error = nil, want non-nil", tt.routePrefix)
				}
				return
			}
			if err != nil {
				t.Fatalf("VersionForRoutePrefix(%q) error = %v", tt.routePrefix, err)
			}
			if got != tt.want {
				t.Fatalf("VersionForRoutePrefix(%q) = %q, want %q", tt.routePrefix, got, tt.want)
			}
		})
	}
}

func TestSessionPayloadPath(t *testing.T) {
	tests := []struct {
		name        string
		routePrefix string
		escrowID    string
		want        string
	}{
		{
			name:        "legacy",
			routePrefix: "",
			escrowID:    "1",
			want:        "v1/devshard/sessions/1/payloads",
		},
		{
			name:        "versioned",
			routePrefix: VersionedRoutePrefix("v1"),
			escrowID:    "1",
			want:        "devshard/v1/sessions/1/payloads",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SessionPayloadPath(tt.routePrefix, tt.escrowID); got != tt.want {
				t.Fatalf("SessionPayloadPath(%q, %q) = %q, want %q", tt.routePrefix, tt.escrowID, got, tt.want)
			}
		})
	}
}
