package main

import (
	"strings"
	"testing"
	"time"
)

func TestLoadConfigUsesProxyPolicyDefaults(t *testing.T) {
	clearProxyPolicyEnv(t)

	config, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.ProxyPolicy.MaxBodyBytes != 10*1024*1024 {
		t.Fatalf(
			"max body bytes = %d, want %d",
			config.ProxyPolicy.MaxBodyBytes,
			10*1024*1024,
		)
	}
	if config.ProxyPolicy.ConnectTimeout != 2*time.Second {
		t.Fatalf(
			"connect timeout = %s, want 2s",
			config.ProxyPolicy.ConnectTimeout,
		)
	}
	if config.ProxyPolicy.StreamIdleTimeout != 20*time.Minute {
		t.Fatalf(
			"stream idle timeout = %s, want 20m",
			config.ProxyPolicy.StreamIdleTimeout,
		)
	}
	if config.ProxyPolicy.UpstreamKeepalive != 64 {
		t.Fatalf(
			"upstream keepalive = %d, want 64",
			config.ProxyPolicy.UpstreamKeepalive,
		)
	}
}

func TestLoadConfigRejectsInvalidProxyPolicyEnv(t *testing.T) {
	tests := []struct {
		key   string
		value string
	}{
		{
			key:   "VERSIOND_ROUTER_MAX_BODY_BYTES",
			value: "ten-megabytes",
		},
		{
			key:   "VERSIOND_ROUTER_CONNECT_TIMEOUT",
			value: "fast",
		},
		{
			key:   "VERSIOND_ROUTER_STREAM_IDLE_TIMEOUT",
			value: "500ms",
		},
		{
			key:   "VERSIOND_ROUTER_UPSTREAM_KEEPALIVE",
			value: "0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			clearProxyPolicyEnv(t)
			t.Setenv(tt.key, tt.value)

			_, err := loadConfig()
			if err == nil || !strings.Contains(err.Error(), tt.key) {
				t.Fatalf(
					"loadConfig error = %v, want error naming %s",
					err,
					tt.key,
				)
			}
		})
	}
}

func clearProxyPolicyEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"VERSIOND_ROUTER_MAX_BODY_BYTES",
		"VERSIOND_ROUTER_CONNECT_TIMEOUT",
		"VERSIOND_ROUTER_STREAM_IDLE_TIMEOUT",
		"VERSIOND_ROUTER_UPSTREAM_KEEPALIVE",
	} {
		t.Setenv(key, "")
	}
}
