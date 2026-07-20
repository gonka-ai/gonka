package devshard

import (
	"testing"

	"devshard/types"
)

func TestNormalizeRoutePrefixDefaultsToVersioned(t *testing.T) {
	want := VersionedRoutePrefix(types.DevshardStateRootAndProtocolVersion)
	if got := NormalizeRoutePrefix(""); got != want {
		t.Fatalf("NormalizeRoutePrefix(\"\") = %q, want %q", got, want)
	}
}

func TestResolveVersionedRoutePrefix(t *testing.T) {
	if got := ResolveVersionedRoutePrefix("v2", ""); got != VersionedRoutePrefix("v2") {
		t.Fatalf("ResolveVersionedRoutePrefix(\"v2\", \"\") = %q, want %q", got, VersionedRoutePrefix("v2"))
	}
	override := VersionedRoutePrefix("custom")
	if got := ResolveVersionedRoutePrefix("v2", override); got != override {
		t.Fatalf("ResolveVersionedRoutePrefix override = %q, want %q", got, override)
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
			name:        "default versioned",
			routePrefix: "",
			want:        types.DevshardStateRootAndProtocolVersion,
		},
		{
			name:        "explicit versioned",
			routePrefix: VersionedRoutePrefix("v2.1.0"),
			want:        "v2.1.0",
		},
		{
			name:        "legacy path rejected",
			routePrefix: "/v1/devshard",
			wantErr:     true,
		},
		{
			name:        "old subnet host route rejected",
			routePrefix: "/v1/subnet",
			wantErr:     true,
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

func TestResolveRoutePrefix(t *testing.T) {
	tests := []struct {
		name        string
		routePrefix string
		wantPrefix  string
		wantVersion string
		wantErr     bool
	}{
		{
			name:        "versioned",
			routePrefix: "/devshard/v2",
			wantPrefix:  "/devshard/v2",
			wantVersion: "v2",
		},
		{
			name:        "trims whitespace and trailing slash",
			routePrefix: " /devshard/dev/ ",
			wantPrefix:  "/devshard/dev",
			wantVersion: "dev",
		},
		{
			name:        "empty route rejected",
			routePrefix: "",
			wantErr:     true,
		},
		{
			name:        "legacy route rejected",
			routePrefix: "/v1/devshard",
			wantErr:     true,
		},
		{
			name:        "missing version rejected",
			routePrefix: "/devshard",
			wantErr:     true,
		},
		{
			name:        "nested version rejected",
			routePrefix: "/devshard/v2/extra",
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotPrefix, gotVersion, err := ResolveRoutePrefix(tt.routePrefix)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ResolveRoutePrefix(%q) error = nil, want non-nil", tt.routePrefix)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveRoutePrefix(%q) error = %v", tt.routePrefix, err)
			}
			if gotPrefix != tt.wantPrefix || gotVersion != tt.wantVersion {
				t.Fatalf("ResolveRoutePrefix(%q) = (%q, %q), want (%q, %q)",
					tt.routePrefix, gotPrefix, gotVersion, tt.wantPrefix, tt.wantVersion)
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
			name:        "default versioned",
			routePrefix: "",
			escrowID:    "1",
			want:        "devshard/" + types.DevshardStateRootAndProtocolVersion + "/sessions/1/payloads",
		},
		{
			name:        "explicit versioned",
			routePrefix: VersionedRoutePrefix("v2"),
			escrowID:    "1",
			want:        "devshard/v2/sessions/1/payloads",
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
