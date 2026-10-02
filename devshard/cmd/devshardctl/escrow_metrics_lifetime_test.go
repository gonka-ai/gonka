package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

// An escrow is not long-lived: the gateway retires one and mints its replacement whenever the balance
// runs out. Every Prometheus child labelled with the old id used to outlive the escrow, so a gateway
// left running grew its series set for as long as it kept rotating (gonka-ai/gonka#1753).

const metricsLifetimeModel = "Qwen/Test"

// Test flow:
//  1. Put an escrow in service, let it record the series a serving escrow produces, retire it, mint the next -- five rotations.
//  2. Leave a sixth escrow in service.
//  3. Assert every rotated-out id is gone from the registry while the live one is still exported: this is the growth the fix exists to stop.
func TestGatewayKeepsNoSeriesForEscrowsThatRotatedOut(t *testing.T) {
	const rotations = 5
	gateway := NewGateway(nil, NewGatewayLimiter(0, 0), metricsLifetimeModel)

	rotatedOut := make([]string, 0, rotations)
	for rotation := 1; rotation <= rotations; rotation++ {
		escrowID := fmt.Sprint(rotation)
		registerEscrowRuntime(gateway, newEscrowRuntime(escrowID))
		recordEscrowSeries(gateway.metrics, escrowID)
		require.True(t, gateway.retireRuntime(escrowID, "balance_exhausted"))
		rotatedOut = append(rotatedOut, escrowID)
	}
	liveEscrowID := fmt.Sprint(rotations + 1)
	registerEscrowRuntime(gateway, newEscrowRuntime(liveEscrowID))
	recordEscrowSeries(gateway.metrics, liveEscrowID)

	for _, escrowID := range rotatedOut {
		require.Empty(t, escrowSeries(t, gateway, escrowID),
			"escrow %s rotated out but its series are still exported", escrowID)
	}
	require.NotEmpty(t, escrowSeries(t, gateway, liveEscrowID),
		"the escrow still in service lost its series")
}

// Test flow:
//  1. Register an escrow that has already recorded series, and check the series are really there.
//  2. Take it out of service one of the four ways the gateway can: retire, deactivate, finalize, clean.
//  3. Assert nothing labelled with that escrow survives beyond what live state emits on its own.
//  4. Assert its dispatcher was stopped too: a ghost burn after the delete would recreate what was just deleted.
func TestEveryWayAnEscrowLeavesServiceForgetsItsSeries(t *testing.T) {
	exits := []struct {
		name             string
		isActiveInStore  bool
		takeOutOfService func(t *testing.T, gateway *Gateway, rt *devshardRuntime)
	}{
		{
			name: "retired",
			takeOutOfService: func(t *testing.T, gateway *Gateway, rt *devshardRuntime) {
				require.True(t, gateway.retireRuntime(rt.id, "balance_exhausted"))
			},
		},
		{
			name:            "deactivated",
			isActiveInStore: true,
			takeOutOfService: func(t *testing.T, gateway *Gateway, rt *devshardRuntime) {
				rec := httptest.NewRecorder()
				gateway.handleAdminDeactivateDevshard(rec, httptest.NewRequest(http.MethodPost, "/v1/admin/devshards/"+rt.id+"/deactivate", nil), rt.id)
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			},
		},
		{
			// Finalize keeps the runtime registered on purpose, so its live-state gauges stay; the
			// recorded series must go all the same.
			name:            "finalized",
			isActiveInStore: true,
			takeOutOfService: func(t *testing.T, gateway *Gateway, rt *devshardRuntime) {
				gateway.markDevshardInactiveAfterFinalize(rt.id, rt)
			},
		},
		{
			name: "cleaned",
			takeOutOfService: func(t *testing.T, gateway *Gateway, rt *devshardRuntime) {
				rec := httptest.NewRecorder()
				gateway.handleAdminCleanDevshard(rec, httptest.NewRequest(http.MethodPost, "/v1/admin/devshards/"+rt.id+"/clean", nil), rt.id)
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			},
		},
	}

	for _, exit := range exits {
		t.Run(exit.name, func(t *testing.T) {
			const escrowID = "12"
			gateway, rt := newEscrowMetricsGateway(t, escrowID, exit.isActiveInStore)
			liveStateSeries := escrowSeries(t, gateway, escrowID)
			recordEscrowSeries(gateway.metrics, escrowID)
			require.Greater(t, len(escrowSeries(t, gateway, escrowID)), len(liveStateSeries),
				"the escrow recorded no series, so forgetting them would prove nothing")

			exit.takeOutOfService(t, gateway, rt)

			for _, series := range escrowSeries(t, gateway, escrowID) {
				require.Contains(t, liveStateSeries, series,
					"an escrow that was %s left %s behind", exit.name, series)
			}
			require.True(t, rt.proxy.redundancy.stopped.Load(),
				"an escrow that was %s kept its dispatcher running, which can record the series again", exit.name)
		})
	}
}

// Test flow:
//  1. Skip an escrow at startup, which records it without ever building a runtime for it.
//  2. Clean it, the only way an operator removes an escrow that was never resident.
//  3. Assert the startup series is forgotten: this path has no runtime, so it cannot lean on the retire funnel.
func TestCleaningAnEscrowThatWasNeverResidentForgetsItsSeries(t *testing.T) {
	const escrowID = "12"
	gateway, _ := newEscrowMetricsGateway(t, escrowID, false)
	gateway.mu.Lock()
	delete(gateway.runtimes, escrowID)
	gateway.runtimeOrder = nil
	gateway.mu.Unlock()
	gateway.metrics.RecordStartupSkippedEscrow(escrowID, metricsLifetimeModel, "inactive")
	require.NotEmpty(t, escrowSeries(t, gateway, escrowID))

	rec := httptest.NewRecorder()
	gateway.handleAdminCleanDevshard(rec, httptest.NewRequest(http.MethodPost, "/v1/admin/devshards/"+escrowID+"/clean", nil), escrowID)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	require.Empty(t, escrowSeries(t, gateway, escrowID),
		"cleaning an escrow that was skipped at startup left its series behind")
}

// Test flow:
//  1. Scan the package's production sources for the two steps that end an escrow's series.
//  2. Assert a runtime leaves the registry in one place, and its series are forgotten in one place.
//  3. Both are what keeps the next exit path from leaking again, and what keeps the forget off g.mu and after the dispatcher stops.
func TestAnEscrowLeavesTheRegistryAndForgetsItsSeriesInOnePlaceEach(t *testing.T) {
	removals := productionCallSites(t, "delete(g.runtimes,")
	require.Len(t, removals, 1, "a runtime must leave the registry in one place: %v", removals)
	require.Contains(t, removals[0], "dropRegisteredRuntimeLocked")

	forgets := productionCallSites(t, "metrics.ForgetEscrow(")
	require.Len(t, forgets, 1, "an escrow's series must be forgotten in one place: %v", forgets)
	require.Contains(t, forgets[0], "forgetRetiredEscrowMetrics")
}

// newEscrowMetricsGateway builds a gateway holding one registered escrow with a live dispatcher, and a
// store row so the admin routes accept it.
func newEscrowMetricsGateway(t *testing.T, escrowID string, isActiveInStore bool) (*Gateway, *devshardRuntime) {
	t.Helper()
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	require.NoError(t, store.Initialize(GatewaySettings{DefaultModel: metricsLifetimeModel}, []GatewayDevshardState{
		{RuntimeConfig: RuntimeConfig{ID: escrowID, PrivateKeyHex: "secret", Model: metricsLifetimeModel}, Active: isActiveInStore},
	}))

	rt := newEscrowRuntime(escrowID)
	gateway := NewGateway([]*devshardRuntime{rt}, NewGatewayLimiter(0, 0), metricsLifetimeModel)
	gateway.store = store
	return gateway, rt
}

func newEscrowRuntime(escrowID string) *devshardRuntime {
	rt := &devshardRuntime{
		id:    escrowID,
		model: metricsLifetimeModel,
		proxy: &Proxy{redundancy: &Redundancy{}},
	}
	rt.active.Store(true)
	return rt
}

// registerEscrowRuntime mirrors the gateway's own registration point in addCreatedEscrowRuntime.
func registerEscrowRuntime(gateway *Gateway, rt *devshardRuntime) {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	gateway.runtimes[rt.id] = rt
	gateway.runtimeOrder = append(gateway.runtimeOrder, rt)
	gateway.sortRuntimeOrderLocked()
}

// recordEscrowSeries produces what a serving escrow leaves in the registry: a slot decision and a picker choice.
func recordEscrowSeries(metrics *DevshardMetrics, escrowID string) {
	metrics.RecordGatewaySlotDecision(GatewaySlotDecisionMetric{
		ParticipantKey: "participant-1",
		Model:          metricsLifetimeModel,
		EscrowID:       escrowID,
		Decision:       "real_send",
		Reason:         "primary",
		QuarantineMode: "none",
	})
	metrics.RecordPickerChoice(escrowID, metricsLifetimeModel)
}

// escrowSeries returns every exported series carrying this escrow's id, named so a failure says which
// metric leaked. It reads the whole registry rather than a list of metric names, so a series added
// later is covered without anyone remembering to add it here.
func escrowSeries(t *testing.T, gateway *Gateway, escrowID string) []string {
	t.Helper()
	families, err := gateway.metrics.registry.Gather()
	require.NoError(t, err)
	var series []string
	for _, family := range families {
		for _, metric := range family.GetMetric() {
			if !metricCarriesEscrowID(metric, escrowID) {
				continue
			}
			series = append(series, family.GetName()+metricLabelsText(metric))
		}
	}
	sort.Strings(series)
	return series
}

func metricCarriesEscrowID(metric *dto.Metric, escrowID string) bool {
	for _, label := range metric.GetLabel() {
		if label.GetName() != "escrow_id" && label.GetName() != "devshard_id" {
			continue
		}
		if label.GetValue() == escrowID {
			return true
		}
	}
	return false
}

func metricLabelsText(metric *dto.Metric) string {
	pairs := make([]string, 0, len(metric.GetLabel()))
	for _, label := range metric.GetLabel() {
		pairs = append(pairs, label.GetName()+"="+label.GetValue())
	}
	sort.Strings(pairs)
	return "{" + strings.Join(pairs, ",") + "}"
}

// productionCallSites returns the enclosing function of every non-test line containing call.
func productionCallSites(t *testing.T, call string) []string {
	t.Helper()
	sources, err := filepath.Glob("*.go")
	require.NoError(t, err)
	var callers []string
	for _, source := range sources {
		if strings.HasSuffix(source, "_test.go") {
			continue
		}
		body, readErr := os.ReadFile(source)
		require.NoError(t, readErr)
		enclosing := ""
		for _, line := range strings.Split(string(body), "\n") {
			if strings.HasPrefix(line, "func ") {
				enclosing = line
			}
			if strings.Contains(line, call) && !strings.HasPrefix(line, "func ") {
				callers = append(callers, source+" "+enclosing)
			}
		}
	}
	return callers
}
