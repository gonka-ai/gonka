package devshard

import "testing"

func TestVersionForRoutePrefix(t *testing.T) {
	tests := []struct {
		name        string
		routePrefix string
		want        string
		wantErr     bool
	}{
		{
			name:        "empty route rejected",
			routePrefix: "",
			wantErr:     true,
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
