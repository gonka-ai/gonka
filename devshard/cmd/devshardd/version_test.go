package main

import (
	"bytes"
	"strings"
	"testing"

	"common/storage/mode"
)

func TestMaybePrintStorageMode(t *testing.T) {
	tests := []struct {
		name     string
		envMode  string
		pgHost   string
		envHA    string
		want     string
		wantCode int
		wantErr  string
	}{
		{name: "postgres", envMode: "postgres", pgHost: "postgres", want: "postgres"},
		{name: "HA postgres", envMode: "postgres", pgHost: "postgres", envHA: "on", want: "postgres"},
		{name: "auto with PGHOST resolves hybrid", pgHost: "postgres", want: "hybrid"},
		{name: "empty resolves sqlite", want: "sqlite"},
		{name: "invalid fails", envMode: "bad", wantCode: 1, wantErr: mode.EnvStorageMode},
		{name: "HA sqlite fails", envMode: "sqlite", envHA: "on", wantCode: 1, wantErr: mode.EnvStorageMode},
		{name: "HA hybrid fails", envMode: "hybrid", pgHost: "postgres", envHA: "on", wantCode: 1, wantErr: mode.EnvStorageMode},
		{name: "invalid HA flag fails", envMode: "postgres", pgHost: "postgres", envHA: "enabled", wantCode: 1, wantErr: envHADeployment},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(mode.EnvStorageMode, tt.envMode)
			t.Setenv("PGHOST", tt.pgHost)
			t.Setenv(envHADeployment, tt.envHA)

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			code, handled := maybePrintVersion([]string{printStorageModeFlag}, &stdout, &stderr)
			if !handled {
				t.Fatal("expected storage mode flag to be handled")
			}
			if code != tt.wantCode {
				t.Fatalf("exit code = %d, want %d", code, tt.wantCode)
			}
			if tt.want != "" && strings.TrimSpace(stdout.String()) != tt.want {
				t.Fatalf("stdout = %q, want %q", strings.TrimSpace(stdout.String()), tt.want)
			}
			if tt.wantErr != "" && !strings.Contains(stderr.String(), tt.wantErr) {
				t.Fatalf("stderr = %q, want it to contain %q", stderr.String(), tt.wantErr)
			}
		})
	}
}

func TestMaybePrintVersionUnknownFlag(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code, handled := maybePrintVersion([]string{"--unknown"}, &stdout, &stderr)
	if handled {
		t.Fatal("unknown flag should not be handled")
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
}
