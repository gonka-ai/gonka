package harness

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devshard/testenv/config"

	"github.com/stretchr/testify/require"
)

// ObservabilityEndpoints are host-published URLs for the testenv observability overlay.
type ObservabilityEndpoints struct {
	Profile    ObsProfile
	Jaeger     string
	Tempo      string
	Alloy      string
	Loki       string
	Prometheus string
	Grafana    string
}

// TraceQueryBase is the host base URL used by WaitTrace* helpers.
func (o ObservabilityEndpoints) TraceQueryBase() string {
	if o.Profile.TraceBackend() == "tempo" {
		return o.Tempo
	}
	return o.Jaeger
}

// DefaultObservabilityEndpoints matches host bindings for the given profile.
func DefaultObservabilityEndpoints() ObservabilityEndpoints {
	return ObservabilityEndpointsFor(ResolveObsProfile())
}

// ObservabilityEndpointsFor returns host URLs for a profile (unused backends stay set for convenience).
func ObservabilityEndpointsFor(profile ObsProfile) ObservabilityEndpoints {
	return ObservabilityEndpoints{
		Profile:    profile,
		Jaeger:     "http://127.0.0.1:11686",
		Tempo:      "http://127.0.0.1:13200",
		Alloy:      "http://127.0.0.1:12345",
		Loki:       "http://127.0.0.1:13101",
		Prometheus: "http://127.0.0.1:19099",
		Grafana:    "http://127.0.0.1:13000",
	}
}

// PrepareObservabilityOverlay copies observability configs into the stack workdir and
// patches Prometheus scrape targets / OTEL env / profile-specific Alloy exporter.
func (s *Stack) PrepareObservabilityOverlay(t *testing.T, cfg *config.File) {
	t.Helper()
	s.Observability = true
	s.ObsProfile = ResolveObsProfile()

	src := filepath.Join(s.TestenvDir, "observability")
	dst := filepath.Join(s.WorkDir, "observability")
	cmd := exec.Command("cp", "-R", src, dst)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "copy observability configs: %s", out)

	promPath := filepath.Join(dst, "prometheus.yml")
	body, err := os.ReadFile(promPath)
	require.NoError(t, err)
	version := cfg.Versiond.VersionName
	if version == "" {
		version = "v2"
	}
	replaced := strings.ReplaceAll(string(body), "devshardctl:8081", fmt.Sprintf("devshardctl:%d", cfg.Devshardctl.Port))
	replaced = strings.ReplaceAll(replaced, "metrics_path: /v2/metrics", fmt.Sprintf("metrics_path: /%s/metrics", version))
	require.NoError(t, os.WriteFile(promPath, []byte(replaced), 0o644))

	if s.ObsProfile.UsesAlloy() {
		// Alloy `run` accepts only one config file — merge base + profile exporter.
		mergeAlloyConfig(t, filepath.Join(dst, "alloy"), s.ObsProfile)
	}

	patchLokiDerivedFields(t, dst, s.ObsProfile)

	envPath := filepath.Join(s.WorkDir, ".env")
	envBody, err := os.ReadFile(envPath)
	require.NoError(t, err)
	lines := strings.Split(string(envBody), "\n")
	set := map[string]string{
		"TESTENV_OTEL_ENABLED":  "true",
		"TESTENV_OTEL_ENDPOINT": s.ObsProfile.OTELEndpoint(),
		"LOG_FORMAT":            "json",
		"TESTENV_OBS_PROFILE":   string(s.ObsProfile),
	}
	outLines := make([]string, 0, len(lines)+len(set))
	seen := make(map[string]struct{})
	for _, line := range lines {
		if line == "" {
			continue
		}
		key, _, ok := strings.Cut(line, "=")
		if ok {
			if val, ok := set[key]; ok {
				outLines = append(outLines, key+"="+val)
				seen[key] = struct{}{}
				continue
			}
		}
		outLines = append(outLines, line)
	}
	for key, val := range set {
		if _, ok := seen[key]; ok {
			continue
		}
		outLines = append(outLines, key+"="+val)
	}
	require.NoError(t, os.WriteFile(envPath, []byte(strings.Join(outLines, "\n")+"\n"), 0o600))

	writeObservabilityIPOverride(t, s.WorkDir, cfg, s.ObsProfile)
}

func mergeAlloyConfig(t *testing.T, alloyDir string, profile ObsProfile) {
	t.Helper()
	basePath := filepath.Join(alloyDir, "config.base.alloy")
	baseBody, err := os.ReadFile(basePath)
	require.NoError(t, err, "read alloy base config")
	tracePath := filepath.Join(alloyDir, profile.AlloyTraceConfigFile())
	traceBody, err := os.ReadFile(tracePath)
	require.NoError(t, err, "read alloy trace exporter %s", tracePath)
	merged := append(append([]byte{}, baseBody...), '\n')
	merged = append(merged, traceBody...)
	if len(merged) == 0 || merged[len(merged)-1] != '\n' {
		merged = append(merged, '\n')
	}
	require.NoError(t, os.WriteFile(filepath.Join(alloyDir, "config.alloy"), merged, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(alloyDir, "config.trace.alloy"), traceBody, 0o644))
}

func patchLokiDerivedFields(t *testing.T, obsDir string, profile ObsProfile) {
	t.Helper()
	path := filepath.Join(obsDir, "grafana", "provisioning", "datasources", "loki.yaml")
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	uid := profile.LokiTraceDatasourceUID()
	// Default committed file points at tempo; rewrite for jaeger profiles.
	patched := strings.ReplaceAll(string(body), "datasourceUid: tempo", "datasourceUid: "+uid)
	patched = strings.ReplaceAll(patched, "datasourceUid: jaeger", "datasourceUid: "+uid)
	require.NoError(t, os.WriteFile(path, []byte(patched), 0o644))
}

func writeObservabilityIPOverride(t *testing.T, workDir string, cfg *config.File, profile ObsProfile) {
	t.Helper()
	base := cfg.Network.BaseIP
	if base == "" {
		base = "172.30.0"
	}
	ipByService := map[string]string{
		"jaeger":     base + ".60",
		"prometheus": base + ".61",
		"loki":       base + ".62",
		"promtail":   base + ".63",
		"grafana":    base + ".64",
		"tempo":      base + ".65",
		"alloy":      base + ".66",
	}

	var b strings.Builder
	b.WriteString("# Auto-generated by citest observability harness — fixed IPs on testenv network.\n")
	b.WriteString(fmt.Sprintf("# profile=%s\n", profile))
	b.WriteString("services:\n")
	for _, svc := range profile.IPServices() {
		ip, ok := ipByService[svc]
		require.True(t, ok, "no static IP mapping for service %q", svc)
		b.WriteString(fmt.Sprintf("  %s:\n", svc))
		b.WriteString("    networks:\n")
		b.WriteString("      testenv:\n")
		b.WriteString(fmt.Sprintf("        ipv4_address: %s\n", ip))
	}
	path := filepath.Join(workDir, "docker-compose.observability.ip.yml")
	require.NoError(t, os.WriteFile(path, []byte(b.String()), 0o644))
}

// WaitObservabilityReady polls readiness endpoints for the active profile.
func WaitObservabilityReady(t *testing.T, obs ObservabilityEndpoints, timeout time.Duration) {
	t.Helper()
	if timeout == 0 {
		timeout = 3 * time.Minute
	}
	client := &http.Client{Timeout: 5 * time.Second}
	t.Logf("citest: waiting for observability profile=%s (trace=%s loki=%s)",
		obs.Profile, obs.TraceQueryBase(), obs.Loki)

	ok := assertEventually(t, timeout, 2*time.Second, func() bool {
		if !httpReady(client, obs.Loki+"/ready") {
			return false
		}
		switch obs.Profile.TraceBackend() {
		case "tempo":
			// Tempo /ready returns 503 for ~15s after start ("Ingester not ready");
			// buildinfo is up earlier and is enough to accept traffic shortly after.
			if !httpReady(client, obs.Tempo+"/ready") && !httpReady(client, obs.Tempo+"/status/buildinfo") {
				return false
			}
		default:
			if !httpReady(client, obs.Jaeger+"/jaeger/") && !httpReady(client, obs.Jaeger+"/") {
				return false
			}
		}
		if obs.Profile.UsesAlloy() {
			// Alloy UI is enough to prove the process is up; OTLP is not HTTP-probed here.
			if !httpReady(client, obs.Alloy+"/-/ready") && !httpReady(client, obs.Alloy+"/") {
				return false
			}
		}
		return true
	})
	require.True(t, ok, "observability stack (profile=%s) not ready within %s", obs.Profile, timeout)
}

// WaitLokiSubstring polls Loki until a log line from versiond services contains text.
func WaitLokiSubstring(t *testing.T, obs ObservabilityEndpoints, substring string, timeout time.Duration) {
	t.Helper()
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	client := &http.Client{Timeout: 10 * time.Second}
	query := `{compose_service=~"versiond.*"} |~ "` + strings.ReplaceAll(substring, `"`, `\"`) + `"`
	t.Logf("citest: waiting for Loki log match %q", substring)
	ok := assertEventually(t, timeout, 3*time.Second, func() bool {
		return lokiQueryContains(client, obs.Loki, query, substring)
	})
	require.True(t, ok, "Loki logs missing %q within %s (Explore: %s/explore, datasource Loki)",
		substring, timeout, obs.Grafana)
}

// RequireMetricsBody GETs a Prometheus exposition endpoint and requires a metric name substring.
func RequireMetricsBody(t *testing.T, client *http.Client, metricsURL, contains string) {
	t.Helper()
	if client == nil {
		client = HTTPClient()
	}
	resp, err := client.Get(metricsURL)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "GET %s: %s", metricsURL, string(body))
	require.Contains(t, string(body), contains, "GET %s metrics body", metricsURL)
}

func httpReady(client *http.Client, url string) bool {
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode >= 200 && resp.StatusCode < 400
}
