package main

import "testing"

func TestValidateBinaryLogVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		env             string
		link            string
		protocolVersion string
		want            string
		wantErr         bool
	}{
		{name: "standalone uses link stamp", env: "", link: "0.2.13-v2-r2", protocolVersion: "v2", want: "0.2.13-v2-r2"},
		{name: "versiond match", env: "0.2.13-v2-r2", link: "0.2.13-v2-r2", protocolVersion: "v2", want: "0.2.13-v2-r2"},
		{name: "legacy slot name", env: "v2", link: "dev-log", protocolVersion: "v2", want: "v2"},
		{name: "mismatch", env: "0.2.12-v2-r1", link: "0.2.13-v2-r2", protocolVersion: "v2", wantErr: true},
		{name: "env without link", env: "0.2.13-v2-r2", link: "", protocolVersion: "v2", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := validateBinaryLogVersion(tt.env, tt.link, tt.protocolVersion)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLoadRuntimeConfig_MLNodeAffinityEnabledDefaultsOff(t *testing.T) {
	cfg, err := loadRuntimeConfig(nil, "v2", "v2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.MLNodeAffinityEnabled {
		t.Fatal("MLNodeAffinityEnabled must default to off")
	}
}

func TestLoadRuntimeConfig_MLNodeAffinityEnabledFromEnv(t *testing.T) {
	t.Setenv("DAPI_MLNODE_AFFINITY_ENABLED", "true")
	cfg, err := loadRuntimeConfig(nil, "v2", "v2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.MLNodeAffinityEnabled {
		t.Fatal("DAPI_MLNODE_AFFINITY_ENABLED=true must enable it")
	}
}
