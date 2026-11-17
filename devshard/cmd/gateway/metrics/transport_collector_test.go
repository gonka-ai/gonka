package metrics

import (
	"testing"

	"devshard/transport"
)

type fixedConnections struct {
	snapshots []transport.HostConnectionSnapshot
}

func (f fixedConnections) Snapshots() []transport.HostConnectionSnapshot { return f.snapshots }

// Test flow:
//  1. Register a `TransportCollector` backed by a `fixedConnections` fake reporting one host's connection snapshot.
//  2. Assert the open-connections gauge and the per-state connection gauges (active, idle, hold-after-close) report the snapshot's values.
func TestTransportCollectorReportsOneSeriesPerConnectionState(t *testing.T) {
	telemetry := New()
	telemetry.Register(NewTransportCollector(fixedConnections{snapshots: []transport.HostConnectionSnapshot{
		{Address: "host-a:8000", Active: 3, Idle: 2, HoldAfterClose: 1, OpenTotal: 5},
	}}))

	expectGauge(t, telemetry, "devshard_host_transport_open_connections", labels{"address": "host-a:8000"}, 5)
	expectGauge(t, telemetry, "devshard_host_transport_connections", labels{"address": "host-a:8000", "state": "active"}, 3)
	expectGauge(t, telemetry, "devshard_host_transport_connections", labels{"address": "host-a:8000", "state": "idle"}, 2)
	expectGauge(t, telemetry, "devshard_host_transport_connections", labels{"address": "host-a:8000", "state": "hold_after_close"}, 1)
}
