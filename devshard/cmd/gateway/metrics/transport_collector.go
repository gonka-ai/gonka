package metrics

import (
	"github.com/prometheus/client_golang/prometheus"

	"devshard/transport"
)

type HostConnectionSource interface {
	Snapshots() []transport.HostConnectionSnapshot
}

type TransportCollector struct {
	connections HostConnectionSource

	open  *prometheus.Desc
	state *prometheus.Desc
}

func NewTransportCollector(connections HostConnectionSource) *TransportCollector {
	return &TransportCollector{
		connections: connections,
		open:        gaugeDesc("devshard_host_transport_open_connections", "Connections currently open to a host address.", "address"),
		state:       gaugeDesc("devshard_host_transport_connections", "Connections to a host address by lifecycle state.", "address", "state"),
	}
}

func (c *TransportCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.open
	ch <- c.state
}

func (c *TransportCollector) Collect(ch chan<- prometheus.Metric) {
	for _, snapshot := range c.connections.Snapshots() {
		gauge(ch, c.open, float64(snapshot.OpenTotal), snapshot.Address)
		gauge(ch, c.state, float64(snapshot.Active), snapshot.Address, "active")
		gauge(ch, c.state, float64(snapshot.Idle), snapshot.Address, "idle")
		gauge(ch, c.state, float64(snapshot.HoldAfterClose), snapshot.Address, "hold_after_close")
	}
}
