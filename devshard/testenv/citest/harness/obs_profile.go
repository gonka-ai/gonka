package harness

import (
	"os"
	"strings"
)

// ObsProfile selects the observability compose fragments and OTLP endpoint.
type ObsProfile string

const (
	ObsProfileTempoAlloy      ObsProfile = "tempo-alloy"
	ObsProfileTempoPromtail   ObsProfile = "tempo-promtail"
	ObsProfileJaegerAlloy     ObsProfile = "jaeger-alloy"
	ObsProfileJaegerPromtail  ObsProfile = "jaeger-promtail"
	DefaultObsProfile                    = ObsProfileTempoAlloy
	envObsProfile                        = "TESTENV_OBS_PROFILE"
)

// ResolveObsProfile reads TESTENV_OBS_PROFILE (default tempo-alloy).
func ResolveObsProfile() ObsProfile {
	raw := strings.TrimSpace(os.Getenv(envObsProfile))
	if raw == "" {
		return DefaultObsProfile
	}
	p := ObsProfile(raw)
	switch p {
	case ObsProfileTempoAlloy, ObsProfileTempoPromtail, ObsProfileJaegerAlloy, ObsProfileJaegerPromtail:
		return p
	default:
		return DefaultObsProfile
	}
}

// OTELEndpoint is the in-compose OTLP URL written into TESTENV_OTEL_ENDPOINT.
func (p ObsProfile) OTELEndpoint() string {
	switch p {
	case ObsProfileTempoAlloy, ObsProfileJaegerAlloy:
		return "http://alloy:4317"
	case ObsProfileTempoPromtail:
		return "http://tempo:4317"
	case ObsProfileJaegerPromtail:
		return "http://jaeger:4317"
	default:
		return ObsProfileTempoAlloy.OTELEndpoint()
	}
}

// TraceBackend is "tempo" or "jaeger" (query API).
func (p ObsProfile) TraceBackend() string {
	switch p {
	case ObsProfileTempoAlloy, ObsProfileTempoPromtail:
		return "tempo"
	default:
		return "jaeger"
	}
}

// UsesAlloy reports whether the Alloy fragment is started.
func (p ObsProfile) UsesAlloy() bool {
	return p == ObsProfileTempoAlloy || p == ObsProfileJaegerAlloy
}

// UsesPromtail reports whether the Promtail fragment is started.
func (p ObsProfile) UsesPromtail() bool {
	return p == ObsProfileTempoPromtail || p == ObsProfileJaegerPromtail
}

// LokiTraceDatasourceUID is the Grafana derivedFields target for TraceID links.
func (p ObsProfile) LokiTraceDatasourceUID() string {
	return p.TraceBackend()
}

// AlloyTraceConfigFile is the River exporter file to activate as config.trace.alloy.
func (p ObsProfile) AlloyTraceConfigFile() string {
	switch p.TraceBackend() {
	case "jaeger":
		return "config.jaeger.trace.alloy"
	default:
		return "config.tempo.trace.alloy"
	}
}

// ComposeFragmentNames lists overlay compose files (relative to testenv dir),
// after the shared docker-compose.observability.yml.
func (p ObsProfile) ComposeFragmentNames() []string {
	var out []string
	switch p.TraceBackend() {
	case "tempo":
		out = append(out, "docker-compose.observability.tempo.yml")
	default:
		out = append(out, "docker-compose.observability.jaeger.yml")
	}
	if p.UsesAlloy() {
		out = append(out, "docker-compose.observability.alloy.yml")
	}
	if p.UsesPromtail() {
		out = append(out, "docker-compose.observability.promtail.yml")
	}
	return out
}

// IPServices lists compose service names that need static IPs for this profile.
func (p ObsProfile) IPServices() []string {
	services := []string{"prometheus", "loki", "grafana"}
	switch p.TraceBackend() {
	case "tempo":
		services = append(services, "tempo")
	default:
		services = append(services, "jaeger")
	}
	if p.UsesAlloy() {
		services = append(services, "alloy")
	}
	if p.UsesPromtail() {
		services = append(services, "promtail")
	}
	return services
}
