package main

import (
	"testing"

	"devshard/cmd/devshardd/session"
)

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

func TestEnvBoolOrUsesDevshardBooleanGrammar(t *testing.T) {
	const key = "TEST_DEVSHARD_BOOL"

	t.Setenv(key, "on")
	if !envBoolOr(key, false) {
		t.Fatal("on must enable the setting")
	}

	t.Setenv(key, "f")
	if envBoolOr(key, true) {
		t.Fatal("f must disable the setting")
	}

	t.Setenv(key, "")
	if !envBoolOr(key, true) {
		t.Fatal("empty value must preserve the caller fallback")
	}

	t.Setenv(key, "invalid")
	if !envBoolOr(key, true) {
		t.Fatal("invalid value must preserve the caller fallback")
	}
}

func TestLoadRuntimeConfig_VoteFalseOnFetchFailureDefaultAndOverride(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want bool
	}{
		{name: "unset defaults on", want: true},
		{name: "false disables", env: "false", want: false},
		{name: "true enables", env: "true", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("DEVSHARD_BINARY_LOG_VERSION", "")
			t.Setenv("DEVSHARD_VALIDATION_RETRY_INTERVAL", "")
			t.Setenv("DEVSHARD_VALIDATION_LEASE_TTL", "")
			t.Setenv("DEVSHARD_SHUTDOWN_GRACE", "")
			t.Setenv("DEVSHARD_VALIDATION_VOTE_FALSE_ON_FETCH_FAILURE", tt.env)

			cfg, err := loadRuntimeConfig(nil, "v2", "dev-log")
			if err != nil {
				t.Fatalf("loadRuntimeConfig: %v", err)
			}
			if cfg.VoteFalseOnFetchFailure != tt.want {
				t.Fatalf("VoteFalseOnFetchFailure got %v, want %v", cfg.VoteFalseOnFetchFailure, tt.want)
			}
			if cfg.ValidationRetryInterval != session.DefaultValidationRetryInterval {
				t.Fatalf("ValidationRetryInterval got %s, want %s", cfg.ValidationRetryInterval, session.DefaultValidationRetryInterval)
			}
			if cfg.ValidationLeaseTTL != session.DefaultValidationLeaseTTL {
				t.Fatalf("ValidationLeaseTTL got %s, want %s", cfg.ValidationLeaseTTL, session.DefaultValidationLeaseTTL)
			}
		})
	}
}
