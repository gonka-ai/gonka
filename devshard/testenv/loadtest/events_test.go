package loadtest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"devshard/testenv/config"
	"devshard/testenv/gatewayphase"

	"github.com/stretchr/testify/require"
)

// Test flow:
// 1. Load the catch-up scenario and take its single host fault as the valid baseline.
// 2. Check that two adjacent pauses on one host validate.
// 3. Break one field at a time and expect validation to fail.
func TestHostFaultValidation(t *testing.T) {
	scenario, err := LoadScenario(filepath.Join("scenarios", "catch-up.yaml"))
	require.NoError(t, err)
	require.Len(t, scenario.Workload.HostFaults, 1)
	valid := scenario.Workload.HostFaults[0]
	t.Run("adjacent pauses", func(t *testing.T) {
		s := scenario
		next := valid
		next.StartAfter = "150s"
		s.Workload.HostFaults = []HostFault{valid, next}
		require.NoError(t, s.Validate())
	})
	for _, tc := range []struct {
		name   string
		change func(*Scenario)
	}{
		{"host", func(s *Scenario) { s.Workload.HostFaults[0].Host = "mock-chain" }},
		{"outside topology", func(s *Scenario) { s.Workload.HostFaults[0].Host = "versiond-4" }},
		{"implicit topology", func(s *Scenario) { s.Topology.Participants = 0 }},
		{"mode", func(s *Scenario) { s.Workload.HostFaults[0].Mode = "stop" }},
		{"negative start", func(s *Scenario) { s.Workload.HostFaults[0].StartAfter = "-1s" }},
		{"duration", func(s *Scenario) { s.Workload.HostFaults[0].Duration = "0s" }},
		{"invalid duration", func(s *Scenario) { s.Workload.HostFaults[0].Duration = "oops" }},
		{"outside workload", func(s *Scenario) { s.Workload.HostFaults[0].StartAfter = "5m" }},
		{"overlap", func(s *Scenario) { s.Workload.HostFaults = append(s.Workload.HostFaults, valid) }},
		{"overlaps an event", func(s *Scenario) {
			s.Workload.Events = []Event{{Action: "throttle", Target: "versiond-2", At: "100s", Until: "120s", Params: map[string]any{"cpus": 0.2}}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := scenario
			s.Workload.HostFaults = []HostFault{valid}
			tc.change(&s)
			require.Error(t, s.Validate())
		})
	}
}

func eventScenario() Scenario {
	scenario := testScenario()
	scenario.Topology.Participants = 4
	scenario.Topology.MockML.Nodes = []MockMLNode{
		{Name: "mock-openai-0", Profile: "fast", Host: "versiond-0"},
		{Name: "mock-openai-1", Profile: "fast", Host: "versiond-3"},
		{Name: "mock-openai-2", Profile: "fast"},
	}
	scenario.Workload.Duration = "10m"
	scenario.Cases = []Case{{ID: "receipt-then-empty-stream"}, {ID: "gateway-killed-mid-race", KnownBug: "#1988"}}
	return scenario
}

func allActionEvents() []Event {
	return []Event{
		{ID: "pause", Action: "pause", Target: "versiond-2", At: "0s", Until: "30s"},
		{ID: "throttle", Action: "throttle", Target: "versiond-2", At: "30s", Until: "60s", Params: map[string]any{"cpus": 0.2}},
		{ID: "restart", Action: "container_restart", Target: "versiond-2", At: "60s"},
		{ID: "kill", Action: "container_kill", Target: "versiond-2", At: "90s", Until: "100s", Params: map[string]any{"signal": "TERM"}},
		{ID: "gateway", Case: "gateway-killed-mid-race", Action: "gateway_restart", At: "90s"},
		{ID: "ml", Case: "receipt-then-empty-stream", Action: "ml_fault", Target: "mock-openai-1", At: "0s", Until: "60s", Params: map[string]any{"patch": map[string]any{"empty_stream": true}}},
		{ID: "trip", Action: "tripfile", Target: "versiond-3", At: "60s", Until: "90s", Params: map[string]any{"file": "devshard-fault-drop-payload"}},
		{ID: "chain", Action: "chain_participant", Target: "versiond-4", At: "0s", Until: "30s", Params: map[string]any{"mode": "absent"}},
		{ID: "epoch", Action: "epoch_advance", At: "2m", Until: "3m"},
		{ID: "challenge", Action: "dapi_challenge", Target: "versiond-4", At: "30s", Until: "60s"},
		{ID: "marker", Action: "marker", Target: "versiond-4", At: "60s", Until: "90s"},
	}
}

// Test flow:
// 1. Build a scenario that uses every action once, with ML node bindings and declared cases.
// 2. Check that it validates and that global targets are filled in.
// 3. Break one rule at a time (target, window, params, ids, cases, overlaps) and expect the named error.
func TestEventValidation(t *testing.T) {
	scenario := eventScenario()
	scenario.Workload.Events = allActionEvents()
	require.NoError(t, scenario.Validate())
	events := scenario.TimelineEvents()
	require.Equal(t, "devshardctl", events[4].Target)
	require.Equal(t, "mock-chain", events[8].Target)
	for _, tc := range []struct {
		name  string
		event Event
		want  string
	}{
		{"unknown action", Event{Action: "explode", Target: "versiond-1", At: "5m"}, "unknown action"},
		{"host target", Event{Action: "pause", Target: "mock-chain", At: "5m", Until: "6m"}, "target must be a versiond host"},
		{"host outside topology", Event{Action: "pause", Target: "versiond-5", At: "5m", Until: "6m"}, "target must be a versiond host"},
		{"ml node target", Event{Action: "ml_fault", Target: "mock-openai-9", At: "5m", Until: "6m", Params: map[string]any{"patch": map[string]any{"hang": true}}}, "target must be a mock_ml node"},
		{"global target", Event{Action: "gateway_restart", Target: "versiond-1", At: "5m"}, "target must be empty or devshardctl"},
		{"missing until", Event{Action: "pause", Target: "versiond-1", At: "5m"}, "requires until"},
		{"until on instant", Event{Action: "container_restart", Target: "versiond-1", At: "5m", Until: "6m"}, "does not take until"},
		{"until before at", Event{Action: "pause", Target: "versiond-1", At: "5m", Until: "4m"}, "until must be after at"},
		{"until outside", Event{Action: "pause", Target: "versiond-1", At: "5m", Until: "11m"}, "until must be after at"},
		{"at outside", Event{Action: "container_restart", Target: "versiond-1", At: "10m"}, "at must be"},
		{"negative at", Event{Action: "container_restart", Target: "versiond-1", At: "-1s"}, "at must be"},
		{"throttle cpus", Event{Action: "throttle", Target: "versiond-1", At: "5m", Until: "6m"}, "params.cpus"},
		{"signal", Event{Action: "container_kill", Target: "versiond-1", At: "5m", Params: map[string]any{"signal": "kill -9"}}, "params.signal"},
		{"empty patch", Event{Action: "ml_fault", Target: "mock-openai-2", At: "5m", Until: "6m", Params: map[string]any{"patch": map[string]any{}}}, "non-empty params.patch"},
		{"patch value", Event{Action: "ml_fault", Target: "mock-openai-2", At: "5m", Until: "6m", Params: map[string]any{"patch": map[string]any{"hang": []any{true}}}}, "params.patch.hang"},
		{"tripfile path", Event{Action: "tripfile", Target: "versiond-1", At: "5m", Until: "6m", Params: map[string]any{"file": "../etc/passwd"}}, "bare file name"},
		{"participant mode", Event{Action: "chain_participant", Target: "versiond-1", At: "5m", Until: "6m", Params: map[string]any{"mode": "ok"}}, "params.mode"},
		{"duplicate id", Event{ID: "pause", Action: "container_restart", Target: "versiond-1", At: "5m"}, "duplicate id"},
		{"undeclared case", Event{Case: "host-public-port-down", Action: "container_restart", Target: "versiond-1", At: "5m"}, "not declared"},
		{"same host overlap", Event{Action: "dapi_challenge", Target: "versiond-2", At: "20s", Until: "40s"}, "overlap on versiond-2"},
		{"bound node overlaps its host", Event{Action: "pause", Target: "versiond-3", At: "50s", Until: "70s"}, "overlap on versiond-3"},
		{"global events overlap", Event{Action: "epoch_advance", At: "90s"}, "overlap on global"},
		{"global window overlap", Event{Action: "gateway_restart", At: "150s"}, "overlap on global"},
		{"phases value", Event{Action: "epoch_advance", At: "5m", Until: "6m", Params: map[string]any{"phases": "0s=Sleeping"}}, "params.phases"},
		{"phases without until", Event{Action: "epoch_advance", At: "5m", Params: map[string]any{"phases": "0s=PoCGenerate"}}, "needs until"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := eventScenario()
			s.Workload.Events = append(allActionEvents(), tc.event)
			require.ErrorContains(t, s.Validate(), tc.want)
		})
	}
	for _, tc := range []struct {
		name  string
		event Event
	}{
		{"other host may overlap", Event{Action: "pause", Target: "versiond-1", At: "10s", Until: "50s"}},
		{"unbound node has its own lane", Event{Action: "ml_fault", Target: "mock-openai-2", At: "0s", Until: "60s", Params: map[string]any{"patch": map[string]any{"http_status": 429}}}},
		{"back to back on one host", Event{Action: "pause", Target: "versiond-2", At: "100s", Until: "2m"}},
		{"global next to global window", Event{Action: "gateway_restart", At: "3m"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := eventScenario()
			s.Workload.Events = append(allActionEvents(), tc.event)
			require.NoError(t, s.Validate())
		})
	}
}

type fakeEventCalls struct {
	mu    sync.Mutex
	calls []string
	fail  map[string]error
	hook  func(string)
}

func (f *fakeEventCalls) record(ctx context.Context, call string) error {
	f.mu.Lock()
	f.calls = append(f.calls, call)
	err := f.fail[call]
	hook := f.hook
	f.mu.Unlock()
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if hook != nil {
		hook(call)
	}
	return err
}

func (f *fakeEventCalls) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeEventCalls) Compose(ctx context.Context, args ...string) error {
	return f.record(ctx, "compose "+strings.Join(args, " "))
}

func (f *fakeEventCalls) Throttle(ctx context.Context, host string, cpus float64) error {
	return f.record(ctx, fmt.Sprintf("throttle %s %g", host, cpus))
}

func (f *fakeEventCalls) Tripfile(ctx context.Context, host, path string, present bool) error {
	return f.record(ctx, fmt.Sprintf("tripfile %s %s %t", host, path, present))
}

func (f *fakeEventCalls) MLFault(ctx context.Context, node string, patch map[string]any) error {
	body, _ := json.Marshal(patch)
	return f.record(ctx, fmt.Sprintf("ml_fault %s %s", node, body))
}

func (f *fakeEventCalls) ChainParticipant(ctx context.Context, address, mode string) error {
	return f.record(ctx, fmt.Sprintf("participant %s %s", address, mode))
}

func (f *fakeEventCalls) EpochAdvance(ctx context.Context) error {
	return f.record(ctx, "epoch_advance")
}

func (f *fakeEventCalls) GatewayPhase(ctx context.Context, steps []gatewayphase.StepJSON) error {
	body, _ := json.Marshal(steps)
	return f.record(ctx, fmt.Sprintf("phase %s", body))
}

func (f *fakeEventCalls) DAPIChallenge(ctx context.Context, host string, challenged bool) error {
	return f.record(ctx, fmt.Sprintf("challenge %s %t", host, challenged))
}

func plannedTestEvent(event Event) plannedEvent {
	return plannedEvent{Event: event, address: "gonka1p4", tripPath: "/tmp/devshard-fault-drop-payload"}
}

// Test flow:
// 1. Run each action through executeEvent with fakes and a zero-length window.
// 2. Check the apply call and its paired revert call, in order.
// 3. Check the record has start and end times and no error.
func TestExecuteEventDispatch(t *testing.T) {
	for _, tc := range []struct {
		event Event
		want  []string
	}{
		{Event{Action: "pause", Target: "versiond-2"}, []string{"compose pause versiond-2", "compose unpause versiond-2"}},
		{Event{Action: "throttle", Target: "versiond-2", Params: map[string]any{"cpus": 0.25}}, []string{"throttle versiond-2 0.25", "throttle versiond-2 0"}},
		{Event{Action: "container_restart", Target: "versiond-2"}, []string{"compose restart versiond-2", "compose start versiond-2"}},
		{Event{Action: "container_kill", Target: "versiond-2"}, []string{"compose kill -s KILL versiond-2", "compose start versiond-2"}},
		{Event{Action: "gateway_restart", Target: "devshardctl", Params: map[string]any{"signal": "TERM"}}, []string{"compose kill -s TERM devshardctl", "compose start devshardctl"}},
		{Event{Action: "ml_fault", Target: "mock-openai-1", Params: map[string]any{"patch": map[string]any{"pause_stream": true, "http_status": 429, "sse_error_message": "boom"}}},
			[]string{`ml_fault mock-openai-1 {"http_status":429,"pause_stream":true,"sse_error_message":"boom"}`, `ml_fault mock-openai-1 {"http_status":0,"pause_stream":false,"sse_error_message":""}`}},
		{Event{Action: "tripfile", Target: "versiond-3", Params: map[string]any{"file": "devshard-fault-drop-payload"}}, []string{"tripfile versiond-3 /tmp/devshard-fault-drop-payload true", "tripfile versiond-3 /tmp/devshard-fault-drop-payload false"}},
		{Event{Action: "chain_participant", Target: "versiond-4", Params: map[string]any{"mode": "unavailable"}}, []string{"participant gonka1p4 unavailable", "participant gonka1p4 ok"}},
		{Event{Action: "epoch_advance", Target: "mock-chain"}, []string{"epoch_advance"}},
		{Event{Action: "dapi_challenge", Target: "versiond-4"}, []string{"challenge versiond-4 true", "challenge versiond-4 false"}},
		{Event{Action: "marker", Target: "versiond-5"}, nil},
	} {
		t.Run(tc.event.Action, func(t *testing.T) {
			fake := &fakeEventCalls{}
			record := executeEvent(context.Background(), time.Now(), plannedTestEvent(tc.event), fake, fake)
			require.Equal(t, tc.want, fake.Calls())
			require.Empty(t, record.Error)
			require.NotNil(t, record.StartedAt)
			require.NotNil(t, record.EndedAt)
			require.False(t, record.EndedAt.Before(*record.StartedAt))
		})
	}
}

// Test flow:
// 1. Run a pause whose command takes effect at once and returns 20ms later.
// 2. Require the recorded window to start no later than the moment the fault took effect.
func TestExecuteEventStartsTheWindowBeforeTheFaultTakesEffect(t *testing.T) {
	var tookEffect time.Time
	fake := &fakeEventCalls{hook: func(call string) {
		if call == "compose pause versiond-2" {
			tookEffect = time.Now()
			time.Sleep(20 * time.Millisecond)
		}
	}}
	event := plannedTestEvent(Event{ID: "pause", Action: "pause", Target: "versiond-2"})
	event.window = eventWindow{start: 0, end: 30 * time.Millisecond}

	record := executeEvent(context.Background(), time.Now(), event, fake, fake)

	require.Empty(t, record.Error)
	require.NotNil(t, record.StartedAt)
	require.False(t, record.StartedAt.After(tookEffect), "window starts %s after the fault took effect", record.StartedAt.Sub(tookEffect))
}

// Test flow:
// 1. Run a windowed epoch_advance with default phases and one with params.phases.
// 2. Check the apply sets the PoC timeline, and the revert advances the epoch then clears the timeline.
func TestExecuteEventWindowedEpochAdvance(t *testing.T) {
	for _, tc := range []struct {
		params map[string]any
		apply  string
	}{
		{nil, `phase [{"at":"0s","phase":"PoCGenerate"},{"at":"20ms","phase":"PoCValidate"}]`},
		{map[string]any{"phases": "0s=PoCGenerate,10ms=PoCValidateWindDown"}, `phase [{"at":"0s","phase":"PoCGenerate"},{"at":"10ms","phase":"PoCValidateWindDown"}]`},
	} {
		fake := &fakeEventCalls{}
		event := plannedTestEvent(Event{ID: "c8", Action: "epoch_advance", Target: "mock-chain", Params: tc.params})
		event.window = eventWindow{start: 0, end: 30 * time.Millisecond}
		record := executeEvent(context.Background(), time.Now(), event, fake, fake)
		require.Empty(t, record.Error)
		require.Equal(t, []string{tc.apply, "epoch_advance", "phase null"}, fake.Calls())
		require.NotNil(t, record.EndedAt)
		require.GreaterOrEqual(t, record.EndedAt.Sub(*record.StartedAt), 20*time.Millisecond)
	}
}

// Test flow:
// 1. Run a windowed pause event with fakes in four modes: success, cancel inside the window, apply error, revert error.
// 2. Check the revert always runs after the apply, even after cancel or an apply error.
// 3. Check the error is captured in the record and the times reflect what happened.
func TestExecuteEventLifecycle(t *testing.T) {
	for _, mode := range []string{"success", "cancel", "apply error", "revert error"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			fake := &fakeEventCalls{fail: map[string]error{}}
			switch mode {
			case "cancel":
				fake.hook = func(call string) {
					if call == "compose pause versiond-2" {
						cancel()
					}
				}
			case "apply error":
				fake.fail["compose pause versiond-2"] = errors.New("pause failed")
			case "revert error":
				fake.fail["compose unpause versiond-2"] = errors.New("unpause failed")
			}
			event := plannedTestEvent(Event{ID: "e1", Case: "host-paused-then-catches-up", Action: "pause", Target: "versiond-2"})
			event.window = eventWindow{start: 0, end: time.Hour}
			if mode != "cancel" {
				event.window.end = time.Millisecond
			}
			record := executeEvent(ctx, time.Now(), event, fake, fake)
			calls := fake.Calls()
			require.Equal(t, "compose pause versiond-2", calls[0])
			require.Equal(t, "compose unpause versiond-2", calls[len(calls)-1])
			require.Equal(t, "host-paused-then-catches-up", record.Case)
			require.Equal(t, event.window.end, record.PlannedEnd.Sub(record.PlannedStart))
			switch mode {
			case "success":
				require.Empty(t, record.Error)
				require.NotNil(t, record.StartedAt)
				require.NotNil(t, record.EndedAt)
			case "cancel":
				require.Contains(t, record.Error, context.Canceled.Error())
				require.NotNil(t, record.StartedAt)
				require.NotNil(t, record.EndedAt)
				require.Len(t, calls, 2)
			case "apply error":
				require.Contains(t, record.Error, "pause failed")
				require.Nil(t, record.StartedAt)
				require.Len(t, calls, 2)
			case "revert error":
				require.Contains(t, record.Error, "unpause failed")
				require.Nil(t, record.EndedAt)
				require.Equal(t, []string{"compose pause versiond-2", "compose unpause versiond-2", "compose unpause versiond-2"}, calls)
			}
		})
	}
}

// Test flow:
// 1. Cancel the context before an event's start time.
// 2. Check nothing is applied or reverted and the record carries the cancel error.
func TestExecuteEventCancelledBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fake := &fakeEventCalls{}
	event := plannedTestEvent(Event{ID: "late", Action: "pause", Target: "versiond-2"})
	event.window = eventWindow{start: time.Hour, end: 2 * time.Hour}
	record := executeEvent(ctx, time.Now(), event, fake, fake)
	require.Empty(t, fake.Calls())
	require.Contains(t, record.Error, context.Canceled.Error())
	require.Nil(t, record.StartedAt)
}

// Test flow:
// 1. Plan two events on one host lane listed out of order, one on a bound ML node of another host, and one global.
// 2. Run the lanes with fakes.
// 3. Check the host lane ran in start order and each record sits at its event's index.
func TestRunEventLanesOrdering(t *testing.T) {
	events := []plannedEvent{
		{Event: Event{ID: "second", Action: "pause", Target: "versiond-2"}, window: eventWindow{start: 20 * time.Millisecond, end: 25 * time.Millisecond}},
		{Event: Event{ID: "first", Action: "dapi_challenge", Target: "versiond-2"}, window: eventWindow{start: 0, end: 10 * time.Millisecond}},
		{Event: Event{ID: "ml", Action: "ml_fault", Target: "mock-openai-1", Params: map[string]any{"patch": map[string]any{"hang": true}}}, window: eventWindow{start: 0, end: 5 * time.Millisecond}},
		{Event: Event{ID: "epoch", Action: "epoch_advance", Target: "mock-chain"}},
	}
	fake := &fakeEventCalls{}
	records := runEventLanes(context.Background(), time.Now(), events, map[string]string{"mock-openai-1": "versiond-3"}, fake, fake)
	require.Len(t, records, 4)
	for i, record := range records {
		require.Equal(t, events[i].ID, record.ID)
		require.Empty(t, record.Error)
	}
	var hostCalls []string
	for _, call := range fake.Calls() {
		if strings.Contains(call, "versiond-2") {
			hostCalls = append(hostCalls, call)
		}
	}
	require.Equal(t, []string{"challenge versiond-2 true", "challenge versiond-2 false", "compose pause versiond-2", "compose unpause versiond-2"}, hostCalls)
}

// Test flow:
// 1. Plan a background marker over the whole run and a pause inside it, both on one host.
// 2. Run the lanes with fakes.
// 3. Check the pause applies at its own start, not after the marker's window ends.
func TestRunEventLanesBackgroundMarkerDoesNotBlockHost(t *testing.T) {
	events := []plannedEvent{
		{Event: Event{ID: "budget", Action: "marker", Target: "versiond-2", Params: map[string]any{"background": true}}, window: eventWindow{start: 0, end: 400 * time.Millisecond}},
		{Event: Event{ID: "pause", Action: "pause", Target: "versiond-2"}, window: eventWindow{start: 20 * time.Millisecond, end: 40 * time.Millisecond}},
	}
	fake := &fakeEventCalls{}
	started := time.Now()
	records := runEventLanes(context.Background(), started, events, nil, fake, fake)
	require.Len(t, records, 2)
	require.Empty(t, records[1].Error)
	require.NotNil(t, records[1].StartedAt)
	require.Less(t, records[1].StartedAt.Sub(started), 200*time.Millisecond, "pause waited for the background marker")
}

// Test flow:
// 1. Plan events against a generated config with an HA pair, a custom fault dir and a bound node.
// 2. Check the participant address resolves to the HA identity and the trip path uses the host's fault dir.
// 3. Check a target missing from the generated topology is rejected.
func TestPlanEvents(t *testing.T) {
	scenario := eventScenario()
	scenario.Workload.Events = []Event{
		{ID: "chain", Action: "chain_participant", Target: "versiond-1", At: "0s", Until: "10s", Params: map[string]any{"mode": "absent"}},
		{ID: "trip", Action: "tripfile", Target: "versiond-2", At: "0s", Until: "10s", Params: map[string]any{"file": "devshard-fault-forge-challenge"}},
	}
	require.NoError(t, scenario.Validate())
	cfg := &config.File{Hosts: []config.HostCfg{
		{ID: "versiond-0", Address: "gonka1ha", KeyName: "versiond-0"},
		{ID: "versiond-1", Address: "gonka1replica", KeyName: "versiond-0"},
		{ID: "versiond-2", Address: "gonka1solo", KeyName: "versiond-2", Env: map[string]string{"DEVSHARD_TESTENV_EXECUTOR_FAULT_DIR": "/faults"}},
	}}
	planned, nodeHosts, err := planEvents(scenario, cfg)
	require.NoError(t, err)
	require.Equal(t, "versiond-3", nodeHosts["mock-openai-1"])
	require.Equal(t, "gonka1ha", planned[0].address)
	require.Equal(t, "/faults/devshard-fault-forge-challenge", planned[1].tripPath)
	require.Equal(t, eventWindow{start: 0, end: 10 * time.Second}, planned[1].window)
	cfg.Hosts = cfg.Hosts[:2]
	_, _, err = planEvents(scenario, cfg)
	require.ErrorContains(t, err, "absent from generated topology")
}

// Test flow:
// 1. Convert a host_faults entry and two events into the timeline.
// 2. Check the alias becomes a pause event with until = start_after + duration and default IDs are assigned.
func TestTimelineEventsAlias(t *testing.T) {
	scenario := eventScenario()
	scenario.Workload.HostFaults = []HostFault{{Host: "versiond-2", Mode: "pause", StartAfter: "90s", Duration: "60s"}}
	scenario.Workload.Events = []Event{{Action: "epoch_advance", At: "5m"}, {ID: "named", Action: "container_restart", Target: "versiond-1", At: "1m"}}
	events := scenario.TimelineEvents()
	require.Equal(t, Event{ID: "host_faults-1", Action: "pause", Target: "versiond-2", At: "90s", Until: "2m30s"}, events[0])
	require.Equal(t, "event-1", events[1].ID)
	require.Equal(t, "named", events[2].ID)
	require.NoError(t, scenario.Validate())
}

// Test flow:
// 1. Write one finished and one never-started record to events.json.
// 2. Decode the file as generic JSON.
// 3. Check every record has exactly the shared schema keys, with null times for the unstarted event.
func TestWriteEventRecordsSchema(t *testing.T) {
	dir := t.TempDir()
	started := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	ended := started.Add(time.Minute)
	records := []EventRecord{
		{ID: "e1", Case: "receipt-then-empty-stream", Action: "ml_fault", Target: "mock-openai-1", Params: map[string]any{"patch": map[string]any{"hang": true}}, PlannedStart: started, PlannedEnd: ended, StartedAt: &started, EndedAt: &ended},
		{ID: "e2", Action: "epoch_advance", Target: "mock-chain", PlannedStart: ended, PlannedEnd: ended, Error: "context canceled"},
	}
	require.NoError(t, writeEventRecords(dir, records))
	body, err := os.ReadFile(filepath.Join(dir, "events.json"))
	require.NoError(t, err)
	var decoded []map[string]any
	require.NoError(t, json.Unmarshal(body, &decoded))
	require.Len(t, decoded, 2)
	want := []string{"action", "case", "ended_at", "error", "id", "params", "planned_end", "planned_start", "started_at", "target"}
	for _, record := range decoded {
		keys := make([]string, 0, len(record))
		for key := range record {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		require.Equal(t, want, keys)
	}
	require.Equal(t, "2026-10-13T12:00:00Z", decoded[0]["started_at"])
	require.Nil(t, decoded[1]["started_at"])
	require.Nil(t, decoded[1]["ended_at"])
	require.NoError(t, writeEventRecords(dir, nil))
	body, err = os.ReadFile(filepath.Join(dir, "events.json"))
	require.NoError(t, err)
	require.Equal(t, "[]\n", string(body))
}

// Test flow:
// 1. Point the HTTP mock control at one fake server standing in for mock-openai, mock-chain and mock-dapi.
// 2. Call every method, including a pause_stream revert.
// 3. Check the routes and JSON bodies, and that a non-2xx reply becomes an error.
func TestHTTPMockControlRoutes(t *testing.T) {
	var mu sync.Mutex
	var got []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		encoded, _ := json.Marshal(body)
		mu.Lock()
		got = append(got, r.Method+" "+r.URL.Path+" "+string(encoded))
		mu.Unlock()
		if r.URL.Path == "/testenv/participant" && body["mode"] == "broken" {
			http.Error(w, "unknown mode", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	control := httpMockControl{client: server.Client(), chainURL: server.URL, dapiURL: server.URL, mlURLs: map[string]string{"mock-openai-1": server.URL}}
	ctx := context.Background()
	require.NoError(t, control.MLFault(ctx, "mock-openai-1", map[string]any{"pause_stream": true}))
	require.NoError(t, control.MLFault(ctx, "mock-openai-1", map[string]any{"pause_stream": false}))
	require.NoError(t, control.ChainParticipant(ctx, "gonka1p4", "absent"))
	require.NoError(t, control.EpochAdvance(ctx))
	require.NoError(t, control.DAPIChallenge(ctx, "versiond-4", true))
	require.NoError(t, control.GatewayPhase(ctx, []gatewayphase.StepJSON{{At: "0s", Phase: "PoCGenerate"}}))
	require.NoError(t, control.GatewayPhase(ctx, nil))
	require.ErrorContains(t, control.ChainParticipant(ctx, "gonka1p4", "broken"), "400")
	require.ErrorContains(t, control.MLFault(ctx, "mock-openai-9", map[string]any{"hang": true}), "unknown mock_ml node")
	require.Equal(t, []string{
		`POST /testenv/fault {"pause_stream":true}`,
		`POST /testenv/fault {"pause_stream":false}`,
		`POST /testenv/stream/release {}`,
		`POST /testenv/participant {"address":"gonka1p4","mode":"absent"}`,
		`POST /testenv/epoch {"advance":true}`,
		`POST /testenv/challenge {"host":"versiond-4","on":true}`,
		`POST /testenv/phase {"steps":[{"at":"0s","phase":"PoCGenerate"}]}`,
		`POST /testenv/phase {"steps":[]}`,
		`POST /testenv/participant {"address":"gonka1p4","mode":"broken"}`,
	}, got)
	cfg := &config.File{}
	cfg.MockChain.TestenvPort = 9001
	cfg.MockDapi.HTTPPort = 9100
	cfg.MockOpenAI.HTTPPort = 8000
	cfg.MockOpenAI.Nodes = []config.MockOpenAINodeCfg{{Name: "mock-openai-0"}, {Name: "mock-openai-1"}}
	built := newHTTPMockControl(cfg)
	require.Equal(t, "http://127.0.0.1:9001", built.chainURL)
	require.Equal(t, "http://127.0.0.1:9100", built.dapiURL)
	require.Equal(t, "http://127.0.0.1:8001", built.mlURLs["mock-openai-1"])
}

// Test flow:
// 1. Start an event runner whose lanes block until cancel, then finish it with cancel.
// 2. Check the error is reported and events.json is written.
func TestEventRunnerManifestOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	runner := &eventRunner{cancel: cancel, done: make(chan struct{}), outputDir: t.TempDir()}
	go func() {
		<-ctx.Done()
		runner.records = []EventRecord{{ID: "host_faults-1", Action: "pause", Target: "versiond-2", Error: ctx.Err().Error()}}
		close(runner.done)
	}()
	records, err := runner.finish(true)
	require.ErrorContains(t, err, "host_faults-1")
	require.Len(t, records, 1)
	require.FileExists(t, filepath.Join(runner.outputDir, "events.json"))
}

// Test flow:
// 1. Run a windowed pause event in three modes with the log captured: success, apply error, revert error.
// 2. Check each mode logs applied and reverted, or an error line naming the failed step, when it happens.
func TestExecuteEventLogsEveryStage(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	for _, tc := range []struct {
		fail string
		want []string
	}{
		{"", []string{"event=e1 action=pause target=versiond-2 stage=applied", "event=e1 action=pause target=versiond-2 stage=reverted"}},
		{"compose pause versiond-2", []string{"event=e1 action=pause target=versiond-2 stage=error step=apply", "pause failed"}},
		{"compose unpause versiond-2", []string{"stage=applied", "event=e1 action=pause target=versiond-2 stage=error step=revert", "pause failed"}},
	} {
		buf.Reset()
		fake := &fakeEventCalls{fail: map[string]error{}}
		if tc.fail != "" {
			fake.fail[tc.fail] = errors.New("pause failed")
		}
		event := plannedTestEvent(Event{ID: "e1", Action: "pause", Target: "versiond-2"})
		event.window = eventWindow{start: 0, end: time.Millisecond}
		executeEvent(context.Background(), time.Now(), event, fake, fake)
		for _, want := range tc.want {
			require.Contains(t, buf.String(), want)
		}
	}
}

// Test flow:
// 1. Ask for the step timeout of every Docker action and of the HTTP mock actions.
// 2. Check Docker actions get the longer Docker timeout, since the Docker CLI stalls past 15 s under load.
func TestEventStepTimeout(t *testing.T) {
	for _, action := range []string{"pause", "throttle", "container_restart", "container_kill", "gateway_restart", "tripfile"} {
		require.Equal(t, dockerEventTimeout, eventStepTimeout(action), action)
	}
	for _, action := range []string{"ml_fault", "chain_participant", "epoch_advance", "dapi_challenge", "marker"} {
		require.Equal(t, eventCommandTimeout, eventStepTimeout(action), action)
	}
	require.Greater(t, dockerEventTimeout, eventCommandTimeout)
}

// Test flow:
// 1. Finish an event runner holding one fired event and two that never applied, one of them with no error recorded.
// 2. Check finish fails naming both unfired events and logs the fired count and a not_fired line for each.
func TestEventRunnerFailsOnUnfiredEvents(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	now := time.Now().UTC()
	runner := &eventRunner{cancel: func() {}, done: make(chan struct{}), outputDir: t.TempDir()}
	runner.records = []EventRecord{
		{ID: "c1", Action: "ml_fault", Target: "mock-openai-3", StartedAt: &now, EndedAt: &now},
		{ID: "c5", Action: "pause", Target: "versiond-5", Error: "signal: killed"},
		{ID: "c6", Action: "gateway_restart", Target: "devshardctl"},
	}
	close(runner.done)
	_, err := runner.finish(false)
	require.ErrorContains(t, err, "c5")
	require.ErrorContains(t, err, "event c6 (gateway_restart devshardctl): never applied")
	require.Contains(t, buf.String(), "stage=events fired=1/3")
	require.Contains(t, buf.String(), "event=c5 action=pause target=versiond-5 stage=not_fired")
	require.Contains(t, buf.String(), "event=c6 action=gateway_restart target=devshardctl stage=not_fired")
}
