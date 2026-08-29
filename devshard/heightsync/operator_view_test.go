package heightsync_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"

	"devshard/heightsync"
	"devshard/types"
)

func TestTurnTracker_AckFirstSetsOpen(t *testing.T) {
	reg := prometheus.NewRegistry()
	require.NoError(t, heightsync.RegisterLogPlaneMetrics(reg))

	tr := heightsync.NewTurnTracker(4, 3, heightsync.DefaultHeartbeatConfig())
	tr.Observe(14, []*types.DevshardTx{{
		Tx: &types.DevshardTx_HeightAck{HeightAck: &types.MsgHeightAck{
			TurnSeq:        7,
			RefNonce:       10,
			SlotId:         0,
			ObservedHeight: 500,
			SyncState:      types.SyncState_SYNCED,
		}},
	}}, 500)

	rec := tr.Record(7)
	require.NotNil(t, rec)
	require.Equal(t, heightsync.TurnOpen, rec.State)

	families, err := reg.Gather()
	require.NoError(t, err)
	require.Equal(t, 1.0, turnStateGaugeValue(t, families, "open"))
}

func turnStateGaugeValue(t *testing.T, families []*dto.MetricFamily, state string) float64 {
	t.Helper()
	for _, f := range families {
		if f.GetName() != heightsync.MetricTurnState {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, lp := range m.GetLabel() {
				if lp.GetName() == "state" && lp.GetValue() == state {
					return m.GetGauge().GetValue()
				}
			}
		}
	}
	t.Fatalf("turn_state{%s} not found", state)
	return 0
}

func TestOperatorView_DurationsMarshalAsMilliseconds(t *testing.T) {
	view := heightsync.OperatorView{
		DevshardID:  "12",
		Freshness:   60 * time.Second,
		IdleTimeout: 12 * time.Second,
		Tips: []heightsync.OriginTip{{
			Slot: 0, Originator: "a", Height: 1, Age: 90 * time.Second, Fresh: true,
		}},
		Contacts: []heightsync.SlotContact{{
			Slot: 1, SinceContact: 13 * time.Second, LastAt: time.Unix(1, 0),
		}},
		CadenceEvents: []heightsync.CadenceEvent{{
			Event:              heightsync.CadenceHeartbeatOpened,
			TurnSeq:            1,
			DurationToTurnover: 5 * time.Second,
		}},
	}
	raw, err := json.Marshal(view)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, float64(60_000), decoded["freshness"])
	require.Equal(t, float64(12_000), decoded["idle_timeout"])

	tips := decoded["tips"].([]any)
	tip0 := tips[0].(map[string]any)
	require.Equal(t, float64(90_000), tip0["age"])

	contacts := decoded["contacts"].([]any)
	c0 := contacts[0].(map[string]any)
	require.Equal(t, float64(13_000), c0["since_contact"])

	events := decoded["cadence_events"].([]any)
	e0 := events[0].(map[string]any)
	require.Equal(t, float64(5_000), e0["duration_to_turnover"])
}
