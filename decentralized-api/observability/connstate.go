package observability

import (
	"net"
	"net/http"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	connStateOnce        sync.Once
	httpConnections      *prometheus.GaugeVec
	httpConnectionsTotal *prometheus.CounterVec
)

func initConnStateMetrics() {
	connStateOnce.Do(func() {
		httpConnections = prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "decentralized_api_http_connections",
			Help: "Current HTTP connections by server and state.",
		}, []string{"server", "state"})
		httpConnectionsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "decentralized_api_http_connections_total",
			Help: "Total HTTP connection state transitions by server and state.",
		}, []string{"server", "state"})
		prometheus.MustRegister(httpConnections, httpConnectionsTotal)
	})
}

// ConnState returns an http.Server.ConnState hook that tracks connection
// lifecycle gauges/counters for the named server (e.g. "ml", "public").
func ConnState(server string) func(net.Conn, http.ConnState) {
	initConnStateMetrics()
	var mu sync.Mutex
	states := make(map[net.Conn]string)

	return func(conn net.Conn, state http.ConnState) {
		next := connStateLabel(state)
		if next == "" {
			return
		}

		mu.Lock()
		defer mu.Unlock()

		if prev := states[conn]; prev != "" {
			httpConnections.WithLabelValues(server, prev).Dec()
		}
		if state == http.StateClosed || state == http.StateHijacked {
			delete(states, conn)
			httpConnectionsTotal.WithLabelValues(server, next).Inc()
			return
		}
		states[conn] = next
		httpConnections.WithLabelValues(server, next).Inc()
		httpConnectionsTotal.WithLabelValues(server, next).Inc()
	}
}

func connStateLabel(state http.ConnState) string {
	switch state {
	case http.StateNew:
		return "new"
	case http.StateActive:
		return "active"
	case http.StateIdle:
		return "idle"
	case http.StateHijacked:
		return "hijacked"
	case http.StateClosed:
		return "closed"
	default:
		return ""
	}
}
