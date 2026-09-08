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

	require.Contains(t, got, "./data/jaeger:/var/lib/jaeger")
	require.Contains(t, got, "./data/loki:/loki")
	require.Contains(t, got, `"127.0.0.1::16686"`)
	require.Contains(t, got, `"127.0.0.1::3100"`)
	require.Contains(t, got, `"127.0.0.1::9090"`)
	require.Contains(t, got, `"127.0.0.1::3000"`)
	require.Contains(t, got, "/abs/dashboards")
	require.NotContains(t, got, "testenv_jaeger_data")
	require.NotContains(t, got, "testenv_loki_data")
	require.NotContains(t, got, "11686")
	require.NotContains(t, got, "13101")
	require.NotContains(t, got, "19099")
	require.NotRegexp(t, `(?m)^volumes:`, got)
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
