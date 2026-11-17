package harness

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRewriteObservabilityComposeIsolatesCitest(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "docker-compose.observability.yml"))
	require.NoError(t, err)

	got := rewriteObservabilityCompose(string(src), "/abs/dashboards")

	require.Contains(t, got, "./data/loki:/loki")
	require.Contains(t, got, "./data/prometheus:/prometheus")
	require.Contains(t, got, "./data/grafana:/var/lib/grafana")
	require.Contains(t, got, `"127.0.0.1::3100"`)
	require.Contains(t, got, `"127.0.0.1::9090"`)
	require.Contains(t, got, `"127.0.0.1::3000"`)
	require.Contains(t, got, `user: "0:0"`)
	require.Regexp(t, `(?m)^  loki:\n(?:    .*\n)*?    user: "0:0"`, got)
	require.Regexp(t, `(?m)^  prometheus:\n(?:    .*\n)*?    user: "0:0"`, got)
	require.Regexp(t, `(?m)^  grafana:\n(?:    .*\n)*?    user: "0:0"`, got)
	require.Contains(t, got, "/abs/dashboards")
	require.NotContains(t, got, "testenv_loki_data")
	require.NotContains(t, got, "testenv_prometheus_data")
	require.NotContains(t, got, "testenv_grafana_data")
	require.NotContains(t, got, "13101")
	require.NotContains(t, got, "19099")
	require.NotContains(t, got, "127.0.0.1:13000:")
	require.NotRegexp(t, `(?m)^volumes:`, got)

	jaegerSrc, err := os.ReadFile(filepath.Join("..", "..", "docker-compose.observability.jaeger.yml"))
	require.NoError(t, err)
	jaeger := rewriteObservabilityCompose(string(jaegerSrc), "/abs/dashboards")
	require.Contains(t, jaeger, "./data/jaeger:/var/lib/jaeger")
	require.Contains(t, jaeger, `"127.0.0.1::16686"`)
	require.NotContains(t, jaeger, "testenv_jaeger_data")
	require.NotContains(t, jaeger, "11686")
	require.NotRegexp(t, `(?m)^volumes:`, jaeger)

	tempoSrc, err := os.ReadFile(filepath.Join("..", "..", "docker-compose.observability.tempo.yml"))
	require.NoError(t, err)
	tempo := rewriteObservabilityCompose(string(tempoSrc), "/abs/dashboards")
	require.Contains(t, tempo, "./data/tempo:/var/tempo")
	require.Contains(t, tempo, `"127.0.0.1::3200"`)
	require.NotContains(t, tempo, "testenv_tempo_data")
	require.NotContains(t, tempo, "13200")

	alloySrc, err := os.ReadFile(filepath.Join("..", "..", "docker-compose.observability.alloy.yml"))
	require.NoError(t, err)
	alloy := rewriteObservabilityCompose(string(alloySrc), "/abs/dashboards")
	require.Contains(t, alloy, "./data/alloy:/var/lib/alloy/data")
	require.NotContains(t, alloy, "testenv_alloy_data")
	require.NotRegexp(t, `(?m)^volumes:`, alloy)
}

func TestComposeFileArgsObservabilityKeepsProfileFragments(t *testing.T) {
	dir := t.TempDir()
	overlay := filepath.Join(dir, "docker-compose.observability.yml")
	names := []string{
		"docker-compose.observability.jaeger.yml",
		"docker-compose.observability.tempo.yml",
		"docker-compose.observability.alloy.yml",
		"docker-compose.observability.promtail.yml",
	}
	require.NoError(t, os.WriteFile(overlay, []byte("services: {}\n"), 0o644))
	for _, name := range names {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("services: {}\n"), 0o644))
	}
	cases := []struct {
		profile ObsProfile
		frags   []string
	}{
		{ObsProfileTempoAlloy, []string{"docker-compose.observability.tempo.yml", "docker-compose.observability.alloy.yml"}},
		{ObsProfileTempoPromtail, []string{"docker-compose.observability.tempo.yml", "docker-compose.observability.promtail.yml"}},
		{ObsProfileJaegerAlloy, []string{"docker-compose.observability.jaeger.yml", "docker-compose.observability.alloy.yml"}},
		{ObsProfileJaegerPromtail, []string{"docker-compose.observability.jaeger.yml", "docker-compose.observability.promtail.yml"}},
	}
	for _, tc := range cases {
		t.Run(string(tc.profile), func(t *testing.T) {
			s := &Stack{
				WorkDir:       dir,
				ComposePath:   filepath.Join(dir, "docker-compose.yml"),
				Observability: true,
				ObsProfile:    tc.profile,
			}
			want := []string{"-f", s.ComposePath, "-f", overlay}
			for _, frag := range tc.frags {
				want = append(want, "-f", filepath.Join(dir, frag))
			}
			got := s.composeFileArgs()
			require.Equal(t, want, got)
			for _, name := range names {
				path := filepath.Join(dir, name)
				needed := false
				for _, frag := range tc.frags {
					if frag == name {
						needed = true
					}
				}
				if needed {
					require.Contains(t, got, path)
				} else {
					require.NotContains(t, got, path)
				}
			}
		})
	}
}

func TestInsertPromtailProjectKeep(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "observability", "promtail-config.yaml"))
	require.NoError(t, err)

	got, ok := insertPromtailProjectKeep(string(src), "citest-o1-xyz")
	require.True(t, ok)
	require.Contains(t, got, "source_labels: [__meta_docker_container_label_com_docker_compose_project]")
	require.Contains(t, got, `regex: "citest-o1-xyz"`)
	require.Contains(t, got, "action: keep")

	_, ok = insertPromtailProjectKeep("server:\n  http_listen_port: 9080\n", "p")
	require.False(t, ok)
}

func TestGrafanaDashboardsDirResolvesRepoCopy(t *testing.T) {
	testenv, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	dir, err := filepath.Abs(grafanaDashboardsDir(testenv))
	require.NoError(t, err)
	require.DirExists(t, dir)
}
