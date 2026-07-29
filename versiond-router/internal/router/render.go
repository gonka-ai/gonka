package router

import (
	"fmt"
	"strings"
	"time"
)

const (
	defaultMaxBodyBytes      = 10 * 1024 * 1024
	defaultConnectTimeout    = 2 * time.Second
	defaultStreamIdle        = 20 * time.Minute
	defaultUpstreamKeepalive = 64
)

// ProxyPolicy is the transport contract between the outer API proxy and the
// versiond router. Timeouts are idle timeouts, not limits on total inference
// duration.
type ProxyPolicy struct {
	MaxBodyBytes      int64
	ConnectTimeout    time.Duration
	StreamIdleTimeout time.Duration
	UpstreamKeepalive int
}

func DefaultProxyPolicy() ProxyPolicy {
	return ProxyPolicy{
		MaxBodyBytes:      defaultMaxBodyBytes,
		ConnectTimeout:    defaultConnectTimeout,
		StreamIdleTimeout: defaultStreamIdle,
		UpstreamKeepalive: defaultUpstreamKeepalive,
	}
}

func (p ProxyPolicy) withDefaults() ProxyPolicy {
	defaults := DefaultProxyPolicy()
	if p.MaxBodyBytes <= 0 {
		p.MaxBodyBytes = defaults.MaxBodyBytes
	}
	if p.ConnectTimeout <= 0 {
		p.ConnectTimeout = defaults.ConnectTimeout
	}
	if p.StreamIdleTimeout <= 0 {
		p.StreamIdleTimeout = defaults.StreamIdleTimeout
	}
	if p.UpstreamKeepalive <= 0 {
		p.UpstreamKeepalive = defaults.UpstreamKeepalive
	}
	return p
}

func (p ProxyPolicy) validate() error {
	if p.MaxBodyBytes <= 0 {
		return fmt.Errorf("proxy max body bytes must be positive")
	}
	if p.ConnectTimeout <= 0 || p.StreamIdleTimeout <= 0 {
		return fmt.Errorf("proxy timeouts must be positive")
	}
	if p.ConnectTimeout < time.Second || p.StreamIdleTimeout < time.Second {
		return fmt.Errorf("proxy timeouts must be at least one second")
	}
	if p.UpstreamKeepalive <= 0 {
		return fmt.Errorf("proxy upstream keepalive must be positive")
	}
	return nil
}

func Render(template []byte, state State, policy ProxyPolicy) ([]byte, error) {
	if err := state.Validate(); err != nil {
		return nil, err
	}
	policy = policy.withDefaults()
	if err := policy.validate(); err != nil {
		return nil, err
	}

	var ha strings.Builder
	for _, host := range state.Hosts {
		fmt.Fprintf(
			&ha,
			"    server %s:%d resolve max_fails=1 fail_timeout=10s",
			host.Address,
			state.Port,
		)
		if host.State != HostActive {
			ha.WriteString(" down")
		}
		ha.WriteString(";\n")
	}

	legacy := state.Hosts[state.hostIndex(state.LegacyHost)]
	var legacyLine strings.Builder
	fmt.Fprintf(&legacyLine, "    server %s:%d resolve", legacy.Address, state.Port)
	if legacy.State != HostActive {
		legacyLine.WriteString(" down")
	}
	legacyLine.WriteString(";\n")

	var backendMap strings.Builder
	backendMap.WriteString("    default versiond_ha_pool;\n")
	for _, version := range state.NonHAVersions {
		fmt.Fprintf(&backendMap, "    %s versiond_legacy;\n", version)
	}

	var haHeaderMap strings.Builder
	haHeaderMap.WriteString("    default \"\";\n")
	if len(state.Hosts) > 1 {
		haHeaderMap.WriteString("    versiond_ha_pool \"true\";\n")
	}

	replacements := map[string]string{
		"${HA_UPSTREAM_SERVERS}":     strings.TrimSuffix(ha.String(), "\n"),
		"${LEGACY_UPSTREAM_SERVERS}": strings.TrimSuffix(legacyLine.String(), "\n"),
		"${VERSIOND_BACKEND_MAP}":    strings.TrimSuffix(backendMap.String(), "\n"),
		"${DEVSHARD_HA_HEADER_MAP}":  strings.TrimSuffix(haHeaderMap.String(), "\n"),
		"${MAX_BODY_BYTES}":          fmt.Sprintf("%d", policy.MaxBodyBytes),
		"${CONNECT_TIMEOUT_SECONDS}": fmt.Sprintf("%d", durationSeconds(policy.ConnectTimeout)),
		"${STREAM_IDLE_SECONDS}":     fmt.Sprintf("%d", durationSeconds(policy.StreamIdleTimeout)),
		"${UPSTREAM_KEEPALIVE}":      fmt.Sprintf("%d", policy.UpstreamKeepalive),
	}
	out := string(template)
	for placeholder, value := range replacements {
		if !strings.Contains(out, placeholder) {
			return nil, fmt.Errorf("router template missing placeholder %s", placeholder)
		}
		out = strings.ReplaceAll(out, placeholder, value)
	}
	return []byte(fmt.Sprintf("# router-state-generation: %d\n%s", state.Generation, out)), nil
}

func durationSeconds(value time.Duration) int64 {
	return int64(value / time.Second)
}
