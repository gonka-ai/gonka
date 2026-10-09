package mlnodeping_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"decentralized-api/broker"
	"decentralized-api/internal/mlnodeping"
	"decentralized-api/observability"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

type stubInventory struct {
	mu    sync.Mutex
	nodes []broker.NodeResponse
}

func (s *stubInventory) GetNodes() ([]broker.NodeResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]broker.NodeResponse, len(s.nodes))
	copy(out, s.nodes)
	return out, nil
}

func (s *stubInventory) set(nodes []broker.NodeResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nodes = nodes
}

func TestTargetsMatchFederationDialBase(t *testing.T) {
	node := broker.Node{
		Host:       "ml.example",
		PoCPort:    8080,
		PoCSegment: "/poc",
		Id:         "node-1",
	}
	base := node.PoCUrl()
	metricsURL, err := observability.JoinMLNodePoCPath(base, observability.MLNodeMetricsPath)
	require.NoError(t, err)
	pingURL, err := observability.JoinMLNodePoCPath(base, observability.MLNodeClockPath)
	require.NoError(t, err)
	readyzURL, err := observability.JoinMLNodePoCPath(base, observability.MLNodeReadyzPath)
	require.NoError(t, err)

	require.Equal(t, base+"/api/v1/metrics", metricsURL)
	require.Equal(t, base+"/api/v1/clock", pingURL)
	require.Equal(t, base+"/readyz", readyzURL)

	inv := &stubInventory{nodes: []broker.NodeResponse{{Node: node}}}
	job := mlnodeping.New(inv, mlnodeping.Config{Disabled: true})
	targets := job.TargetsForTest()
	require.Len(t, targets, 1)
	require.Equal(t, "node-1", targets[0].Key)
	require.Equal(t, pingURL, targets[0].ClockURL)
	require.Equal(t, readyzURL, targets[0].FallbackURL)
}

func TestJobPublishesAndForgets(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/clock", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Date", time.Now().UTC().Format(http.TimeFormat))
		_, _ = io.WriteString(w, "ok")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	host, port := splitHostPort(t, srv.URL)
	inv := &stubInventory{nodes: []broker.NodeResponse{{
		Node: broker.Node{Host: host, PoCPort: port, Id: "node-pub"},
	}}}

	job := mlnodeping.New(inv, mlnodeping.Config{
		Interval:    200 * time.Millisecond,
		Timeout:     50 * time.Millisecond,
		Concurrency: 2,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	job.Start(ctx)
	defer job.Stop()

	require.Eventually(t, func() bool {
		return gaugeValue(t, "dapi_mlnode_ping_up", map[string]string{"node_id": "node-pub"}) == 1
	}, 3*time.Second, 50*time.Millisecond)

	inv.set(nil)
	require.Eventually(t, func() bool {
		return !metricHasLabel(t, "dapi_mlnode_ping_up", "node_id", "node-pub")
	}, 3*time.Second, 50*time.Millisecond)
}

func TestKillSwitch(t *testing.T) {
	inv := &stubInventory{nodes: []broker.NodeResponse{{
		Node: broker.Node{Host: "127.0.0.1", PoCPort: 1, Id: "dead"},
	}}}
	job := mlnodeping.New(inv, mlnodeping.Config{
		Interval: 200 * time.Millisecond,
		Timeout:  50 * time.Millisecond,
		Disabled: true,
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		job.Start(context.Background())
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("disabled Start should return immediately")
	}
	job.Stop()
}

func splitHostPort(t *testing.T, raw string) (string, int) {
	t.Helper()
	u := strings.TrimPrefix(raw, "http://")
	host, portStr, ok := strings.Cut(u, ":")
	require.True(t, ok)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)
	return host, port
}

func gather() []*dto.MetricFamily {
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		return nil
	}
	return mfs
}

func gaugeValue(t *testing.T, name string, labels map[string]string) float64 {
	t.Helper()
	for _, mf := range gather() {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			if labelsMatch(m, labels) && m.Gauge != nil {
				return m.Gauge.GetValue()
			}
		}
	}
	return -1
}

func metricHasLabel(t *testing.T, name, label, value string) bool {
	t.Helper()
	for _, mf := range gather() {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, lp := range m.GetLabel() {
				if lp.GetName() == label && lp.GetValue() == value {
					return true
				}
			}
		}
	}
	return false
}

func labelsMatch(m *dto.Metric, want map[string]string) bool {
	if m == nil || len(m.GetLabel()) != len(want) {
		return false
	}
	for _, lp := range m.GetLabel() {
		if want[lp.GetName()] != lp.GetValue() {
			return false
		}
	}
	return true
}
