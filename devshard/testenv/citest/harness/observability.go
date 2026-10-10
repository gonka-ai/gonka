package harness

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"devshard/testenv/config"

	"github.com/stretchr/testify/require"
)

// ObservabilityEndpoints are host-published URLs for the testenv observability overlay.
type ObservabilityEndpoints struct {
	Profile        ObsProfile
	Jaeger         string
	Tempo          string
	Alloy          string
	Loki           string
	Prometheus     string
	Grafana        string
	ComposeProject string
	StartedAt      time.Time
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
	host := hostPublishedAddr("")
	return ObservabilityEndpoints{
		Profile:    profile,
		Jaeger:     "http://" + net.JoinHostPort(host, "11686"),
		Tempo:      "http://" + net.JoinHostPort(host, "13200"),
		Alloy:      "http://" + net.JoinHostPort(host, "12345"),
		Loki:       "http://" + net.JoinHostPort(host, "13101"),
		Prometheus: "http://" + net.JoinHostPort(host, "19099"),
		Grafana:    "http://" + net.JoinHostPort(host, "13000"),
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

	writeIsolatedObservabilityCompose(t, s)
	pinPromtailToComposeProject(t, filepath.Join(dst, "promtail-config.yaml"), s.ComposeProject)

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
	for k, v := range s.payloadEnv {
		set[k] = v
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
func writeIsolatedObservabilityCompose(t *testing.T, s *Stack) {
	t.Helper()
	src, err := os.ReadFile(filepath.Join(s.TestenvDir, "docker-compose.observability.yml"))
	require.NoError(t, err)
	dashboards, err := filepath.Abs(grafanaDashboardsDir(s.TestenvDir))
	require.NoError(t, err)
	require.DirExists(t, dashboards)
	for _, dir := range []string{"jaeger", "loki", "prometheus", "grafana", "promtail", "tempo", "alloy"} {
		require.NoError(t, os.MkdirAll(filepath.Join(s.WorkDir, "data", dir), 0o755))
	}
	rewritten := rewriteObservabilityCompose(string(src), filepath.ToSlash(dashboards))
	require.NoError(t, os.WriteFile(filepath.Join(s.WorkDir, "docker-compose.observability.yml"), []byte(rewritten), 0o644))
	profile := s.ObsProfile
	if profile == "" {
		profile = ResolveObsProfile()
	}
	for _, frag := range profile.ComposeFragmentNames() {
		body, err := os.ReadFile(filepath.Join(s.TestenvDir, frag))
		require.NoError(t, err, "read observability fragment %s", frag)
		isolated := rewriteObservabilityCompose(string(body), filepath.ToSlash(dashboards))
		require.NoError(t, os.WriteFile(filepath.Join(s.WorkDir, frag), []byte(isolated), 0o644))
	}
}

func rewriteObservabilityCompose(src, dashboardsAbs string) string {
	repls := [][2]string{
		{"testenv_jaeger_data:/var/lib/jaeger", "./data/jaeger:/var/lib/jaeger"},
		{"testenv_prometheus_data:/prometheus", "./data/prometheus:/prometheus"},
		{"testenv_loki_data:/loki", "./data/loki:/loki"},
		{"testenv_promtail_data:/tmp", "./data/promtail:/tmp"},
		{"testenv_grafana_data:/var/lib/grafana", "./data/grafana:/var/lib/grafana"},
		{"testenv_tempo_data:/var/tempo", "./data/tempo:/var/tempo"},
		{"testenv_alloy_data:/var/lib/alloy/data", "./data/alloy:/var/lib/alloy/data"},
		{"../../../deploy/join/observability/grafana/dashboards", dashboardsAbs},
	}
	out := src
	for _, r := range repls {
		out = strings.ReplaceAll(out, r[0], r[1])
	}
	portRe := regexp.MustCompile(`(?m)^(\s*-\s*")127\.0\.0\.1:[0-9]+:([0-9]+)(".*)$`)
	out = portRe.ReplaceAllString(out, `${1}127.0.0.1::${2}${3}`)
	volRe := regexp.MustCompile(`(?s)\nvolumes:\n(?:  testenv_\w+:\n)+`)
	return volRe.ReplaceAllString(out, "\n")
}

func pinPromtailToComposeProject(t *testing.T, promtailPath, project string) {
	t.Helper()
	require.NotEmpty(t, project)
	body, err := os.ReadFile(promtailPath)
	require.NoError(t, err)
	updated, ok := insertPromtailProjectKeep(string(body), project)
	require.True(t, ok, "promtail-config.yaml: compose_service keep rule not found")
	require.NoError(t, os.WriteFile(promtailPath, []byte(updated), 0o644))
}

func grafanaDashboardsDir(testenvDir string) string {
	return filepath.Join(testenvDir, "../../deploy/join/observability/grafana/dashboards")
}

func insertPromtailProjectKeep(cfg, project string) (string, bool) {
	re := regexp.MustCompile(`(?m)^(\s+- source_labels: \[__meta_docker_container_label_com_docker_compose_service\]\n(?:.*\n)*?\s+action: keep\n)`)
	loc := re.FindStringIndex(cfg)
	if loc == nil {
		return cfg, false
	}
	insert := `      - source_labels: [__meta_docker_container_label_com_docker_compose_project]
        regex: "` + regexp.QuoteMeta(project) + `"
        action: keep
`
	return cfg[:loc[1]] + insert + cfg[loc[1]:], true
}

// ObservabilityHostEndpoints reads Docker-assigned ports for the services this
// profile actually started. A tempo profile has no jaeger container, and a
// promtail profile has no alloy container.
func (s *Stack) ObservabilityHostEndpoints(t *testing.T) ObservabilityEndpoints {
	t.Helper()
	profile := s.ObsProfile
	if profile == "" {
		profile = ResolveObsProfile()
	}
	obs := ObservabilityEndpoints{
		Profile:        profile,
		ComposeProject: s.ComposeProject,
		StartedAt:      time.Now().Add(-30 * time.Second),
	}
	for _, pub := range profile.hostPublishedPorts() {
		addr := "http://" + s.composePublishedAddr(t, pub.service, pub.port)
		switch pub.service {
		case "loki":
			obs.Loki = addr
		case "prometheus":
			obs.Prometheus = addr
		case "grafana":
			obs.Grafana = addr
		case "tempo":
			obs.Tempo = addr
		case "jaeger":
			obs.Jaeger = addr
		case "alloy":
			obs.Alloy = addr
		}
	}
	return obs
}

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
			// Tempo 2.7.1 can panic in statusHandler for /status/buildinfo in this
			// local-blocks test config, while /ready correctly gates trace reads.
			if !httpReady(client, obs.Tempo+"/ready") {
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
	if obs.ComposeProject != "" {
		query = `{compose_project="` + obs.ComposeProject + `",compose_service=~"versiond.*"} |~ "` +
			strings.ReplaceAll(substring, `"`, `\"`) + `"`
	}
	t.Logf("citest: waiting for Loki log match %q query=%s", substring, query)
	ok := assertEventually(t, timeout, 3*time.Second, func() bool {
		return lokiQueryContains(client, obs.Loki, query, substring, obs.StartedAt)
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

// RequireDevsharddMetricSample scrapes each versiond host's /{version}/metrics
// (not the sticky router path, which can land on the idle replica). HistogramVec
// names only appear after Observe, so _count means this process handled work.
func RequireDevsharddMetricSample(t *testing.T, stack *Stack, cfg *config.File, metric string) {
	t.Helper()
	require.NotNil(t, cfg)
	version := cfg.Versiond.VersionName
	if version == "" {
		version = "v2"
	}
	var last string
	ok := AssertEventually(t, 30*time.Second, time.Second, func() bool {
		for _, h := range cfg.Hosts {
			out, err := stack.ComposeExecOutput(h.ID, "wget", "-qO-", "http://127.0.0.1:8080/"+version+"/metrics")
			if err != nil {
				last = h.ID + ": " + err.Error()
				continue
			}
			if strings.Contains(out, metric+"_count") || strings.Contains(out, metric+"_bucket") {
				t.Logf("citest: observed %s on %s", metric, h.ID)
				return true
			}
			last = h.ID + ": metric name missing"
		}
		return false
	})
	require.True(t, ok, "no versiond host exported %s after chat (last=%s); sticky /%s/metrics can miss the serving replica",
		metric, last, version)
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

func lokiQueryContains(client *http.Client, baseURL, query, substring string, startedAt time.Time) bool {
	end := time.Now()
	start := end.Add(-15 * time.Minute)
	if !startedAt.IsZero() && startedAt.Before(end) {
		start = startedAt
	}
	u, err := url.Parse(baseURL + "/loki/api/v1/query_range")
	if err != nil {
		return false
	}
	q := u.Query()
	q.Set("query", query)
	q.Set("limit", "50")
	q.Set("start", strconv.FormatInt(start.UnixNano(), 10))
	q.Set("end", strconv.FormatInt(end.UnixNano(), 10))
	u.RawQuery = q.Encode()

	resp, err := client.Get(u.String())
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false
	}
	return strings.Contains(string(body), substring)
}
