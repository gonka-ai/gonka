package accounting

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

func TestCollectorExportsCurrentEpochBookQuery(t *testing.T) {
	book := NewBook()
	registerTestEscrow(t, book, "current", 5, "model-a", EscrowActive)
	registerTestEscrow(t, book, "old", 4, "model-a", EscrowSettled)
	require.NoError(t, book.Apply(DiffApplied{EscrowID: "current", Nonce: 1, Inference: true}))
	require.NoError(t, book.Apply(Ghost{
		EscrowID:      "current",
		Nonce:         1,
		DispatchPhase: PhasePoC,
		Quarantine:    QuarantineProbe,
		Reason:        NoSendPoCUnavailable,
		DetailReason:  "no_host",
	}))
	require.NoError(t, book.Apply(DiffApplied{EscrowID: "current", Nonce: 2}))
	require.NoError(t, book.Apply(DiffApplied{EscrowID: "old", Nonce: 1}))
	require.Error(t, book.Apply(RealSend{EscrowID: "current", Nonce: 99}))

	registry := prometheus.NewPedanticRegistry()
	registry.MustRegister(NewCollector(book, func(context.Context) (uint64, error) {
		return 5, nil
	}))
	families, err := registry.Gather()
	require.NoError(t, err)

	assigned := metricFamily(t, families, "devshard_accounting_assigned_nonces")
	require.Len(t, assigned.GetMetric(), 2)
	var assignedTotal float64
	for _, metric := range assigned.GetMetric() {
		assignedTotal += metric.GetGauge().GetValue()
	}
	require.Equal(t, float64(2), assignedTotal)

	dispositions := metricFamily(t, families, "devshard_accounting_disposition")
	require.Len(t, dispositions.GetMetric(), 2)
	for _, metric := range dispositions.GetMetric() {
		for _, label := range metric.GetLabel() {
			require.NotEqual(t, "escrow_id", label.GetName())
			require.NotEqual(t, "detail_reason", label.GetName())
		}
	}
	writerErrors := metricFamily(t, families, "devshard_accounting_writer_errors")
	require.Len(t, writerErrors.GetMetric(), 1)
	require.Empty(t, writerErrors.GetMetric()[0].GetLabel(),
		"snapshot writer errors are book-global, not per participant")
	recordingErrors := metricFamily(t, families, "devshard_accounting_recording_errors")
	require.Equal(t, float64(1), recordingErrors.GetMetric()[0].GetGauge().GetValue())
	require.Empty(t, recordingErrors.GetMetric()[0].GetLabel())
	epochError := metricFamily(t, families, "devshard_accounting_current_epoch_error")
	require.Zero(t, epochError.GetMetric()[0].GetGauge().GetValue())
	recordedInvalid := metricFamily(t, families, "devshard_accounting_recorded_invalid_transitions")
	require.Len(t, recordedInvalid.GetMetric(), 2)
}

func TestCollectorReportsUnavailableCurrentEpoch(t *testing.T) {
	registry := prometheus.NewPedanticRegistry()
	registry.MustRegister(NewCollector(NewBook(), func(context.Context) (uint64, error) {
		return 0, context.Canceled
	}))
	families, err := registry.Gather()
	require.NoError(t, err)
	epochError := metricFamily(t, families, "devshard_accounting_current_epoch_error")
	require.Equal(t, float64(1), epochError.GetMetric()[0].GetGauge().GetValue())
}

func metricFamily(t *testing.T, families []*dto.MetricFamily, name string) *dto.MetricFamily {
	t.Helper()
	for _, family := range families {
		if family.GetName() == name {
			return family
		}
	}
	t.Fatalf("metric family %q not found", name)
	return nil
}
