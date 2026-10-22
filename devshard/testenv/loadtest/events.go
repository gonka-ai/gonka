package loadtest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"devshard/testenv/config"
	"devshard/testenv/gatewayphase"
)

// HostFault freezes a whole versiond container, including its devshardd child.
// It is the original form of a pause event and stays accepted as an alias.
type HostFault struct {
	Host       string `yaml:"host" json:"host"`
	Mode       string `yaml:"mode" json:"mode"`
	StartAfter string `yaml:"start_after" json:"start_after"`
	Duration   string `yaml:"duration" json:"duration"`
	Recovery   string `yaml:"recovery,omitempty" json:"recovery,omitempty"`
}

// Event is one entry of workload.events. At and Until are offsets from the
// workload start; Until is the planned revert time for windowed actions.
type Event struct {
	ID       string         `yaml:"id"`
	Case     string         `yaml:"case"`
	Action   string         `yaml:"action"`
	Target   string         `yaml:"target"`
	At       string         `yaml:"at"`
	Until    string         `yaml:"until"`
	Recovery string         `yaml:"recovery,omitempty"`
	Params   map[string]any `yaml:"params"`
}

// EventRecord is one events.json entry. The schema is shared with the
// invariant evaluators. Recovery is optional for compatibility with older run artifacts.
type EventRecord struct {
	ID           string         `json:"id"`
	Case         string         `json:"case"`
	Action       string         `json:"action"`
	Target       string         `json:"target"`
	Params       map[string]any `json:"params"`
	Recovery     string         `json:"recovery,omitempty"`
	PlannedStart time.Time      `json:"planned_start"`
	PlannedEnd   time.Time      `json:"planned_end"`
	StartedAt    *time.Time     `json:"started_at"`
	EndedAt      *time.Time     `json:"ended_at"`
	Error        string         `json:"error"`
}

const (
	targetHost = iota
	targetMLNode
	targetGlobal
)

const (
	windowRequired = iota
	windowOptional
	windowNone
)

type eventActionSpec struct {
	target int
	window int
}

var eventActions = map[string]eventActionSpec{
	"pause":             {targetHost, windowRequired},
	"throttle":          {targetHost, windowRequired},
	"container_restart": {targetHost, windowNone},
	"container_kill":    {targetHost, windowOptional},
	"gateway_restart":   {targetGlobal, windowOptional},
	"ml_fault":          {targetMLNode, windowRequired},
	"tripfile":          {targetHost, windowRequired},
	"chain_participant": {targetHost, windowRequired},
	"epoch_advance":     {targetGlobal, windowOptional},
	"dapi_challenge":    {targetHost, windowRequired},
	"marker":            {targetHost, windowRequired},
}

var globalEventTargets = map[string]string{
	"gateway_restart": "devshardctl",
	"epoch_advance":   "mock-chain",
}

var signalPattern = regexp.MustCompile(`^(SIG)?[A-Z0-9]+$`)

// TimelineEvents returns host_faults converted to pause events followed by
// workload.events, with defaulted IDs and global targets.
func (s Scenario) TimelineEvents() []Event {
	events := make([]Event, 0, len(s.Workload.HostFaults)+len(s.Workload.Events))
	for i, fault := range s.Workload.HostFaults {
		event := Event{ID: fmt.Sprintf("host_faults-%d", i+1), Action: "pause", Target: fault.Host, At: fault.StartAfter, Until: fault.Duration}
		event.Recovery = fault.Recovery
		start, e1 := time.ParseDuration(fault.StartAfter)
		duration, e2 := time.ParseDuration(fault.Duration)
		if e1 == nil && e2 == nil {
			event.Until = (start + duration).String()
		}
		events = append(events, event)
	}
	for i, event := range s.Workload.Events {
		if event.ID == "" {
			event.ID = fmt.Sprintf("event-%d", i+1)
		}
		if target, ok := globalEventTargets[event.Action]; ok && event.Target == "" {
			event.Target = target
		}
		events = append(events, event)
	}
	return events
}

func validateHostFaults(topology Topology, faults []HostFault, workload time.Duration) error {
	ends := map[string]time.Duration{}
	for i, fault := range faults {
		if topology.Participants == 0 || !validTopologyHost(fault.Host, topology.Participants) {
			return fmt.Errorf("workload.host_faults[%d]: host must belong to an explicit topology.participants", i)
		}
		start, e1 := time.ParseDuration(fault.StartAfter)
		duration, e2 := time.ParseDuration(fault.Duration)
		if fault.Mode != "pause" || e1 != nil || e2 != nil || start < 0 || duration <= 0 || start+duration > workload || duration > workload-start {
			return fmt.Errorf("workload.host_faults[%d]: requires mode pause, non-negative start_after, positive duration, and pause within workload", i)
		}
		if end, ok := ends[fault.Host]; ok && start < end {
			return fmt.Errorf("workload.host_faults[%d]: windows for %s must be ordered and not overlap", i, fault.Host)
		}
		ends[fault.Host] = start + duration
	}
	return nil
}

func validTopologyHost(host string, participants int) bool {
	for index := 0; index <= participants; index++ {
		if host == fmt.Sprintf("versiond-%d", index) {
			return true
		}
	}
	return false
}

type eventWindow struct {
	start, end time.Duration
}

// overlaps treats an instant event as a 1ns window so two instants at the same
// offset collide, while back-to-back windows do not.
func (w eventWindow) overlaps(other eventWindow) bool {
	return w.start < max(other.end, other.start+1) && other.start < max(w.end, w.start+1)
}

func parseEventWindow(event Event, spec eventActionSpec, workload time.Duration) (eventWindow, error) {
	start, err := time.ParseDuration(event.At)
	if err != nil || start < 0 || start >= workload {
		return eventWindow{}, fmt.Errorf("at must be a non-negative duration inside the workload")
	}
	if event.Until == "" {
		if spec.window == windowRequired {
			return eventWindow{}, fmt.Errorf("action %s requires until", event.Action)
		}
		return eventWindow{start: start, end: start}, nil
	}
	if spec.window == windowNone {
		return eventWindow{}, fmt.Errorf("action %s does not take until", event.Action)
	}
	end, err := time.ParseDuration(event.Until)
	if err != nil || end <= start || end > workload {
		return eventWindow{}, fmt.Errorf("until must be after at and inside the workload")
	}
	return eventWindow{start: start, end: end}, nil
}

// eventLane names the resource an event occupies. Events in one lane run in
// order and must not overlap: one lane per host, plus one for global actions.
func eventLane(event Event, nodeHosts map[string]string) string {
	switch eventActions[event.Action].target {
	case targetGlobal:
		return "global"
	case targetMLNode:
		if host := nodeHosts[event.Target]; host != "" {
			return host
		}
	}
	return event.Target
}

func validateEvents(s Scenario, workload time.Duration) error {
	nodeHosts := map[string]string{}
	for _, node := range s.Topology.MockML.Nodes {
		nodeHosts[node.Name] = node.Host
	}
	cases := map[string]bool{}
	for _, c := range s.Cases {
		cases[c.ID] = true
	}
	events := s.TimelineEvents()
	windows := make([]eventWindow, len(events))
	ids := map[string]bool{}
	for i, event := range events {
		prefix := fmt.Sprintf("workload.events %q", event.ID)
		spec, ok := eventActions[event.Action]
		if !ok {
			return fmt.Errorf("%s: unknown action %q", prefix, event.Action)
		}
		if ids[event.ID] {
			return fmt.Errorf("%s: duplicate id", prefix)
		}
		ids[event.ID] = true
		if event.Case != "" && len(s.Cases) > 0 && !cases[event.Case] {
			return fmt.Errorf("%s: case %q is not declared in cases", prefix, event.Case)
		}
		switch spec.target {
		case targetHost:
			if s.Topology.Participants == 0 || !validTopologyHost(event.Target, s.Topology.Participants) {
				return fmt.Errorf("%s: target must be a versiond host of an explicit topology.participants", prefix)
			}
		case targetMLNode:
			if _, ok := nodeHosts[event.Target]; !ok {
				return fmt.Errorf("%s: target must be a mock_ml node", prefix)
			}
		case targetGlobal:
			if event.Target != globalEventTargets[event.Action] {
				return fmt.Errorf("%s: target must be empty or %s", prefix, globalEventTargets[event.Action])
			}
		}
		window, err := parseEventWindow(event, spec, workload)
		if err != nil {
			return fmt.Errorf("%s: %w", prefix, err)
		}
		windows[i] = window
		if err := validateEventParams(event); err != nil {
			return fmt.Errorf("%s: %w", prefix, err)
		}
	}
	for i := range events {
		for j := 0; j < i; j++ {
			if isBackground(events[i].Params) || isBackground(events[j].Params) {
				continue
			}
			if eventLane(events[i], nodeHosts) == eventLane(events[j], nodeHosts) && windows[i].overlaps(windows[j]) {
				return fmt.Errorf("workload.events %q and %q overlap on %s", events[j].ID, events[i].ID, eventLane(events[i], nodeHosts))
			}
		}
	}
	return nil
}

// isBackground marks a marker for a condition that holds all run, such as a
// boot-time peer budget: it may overlap other events on its host, which keep
// the misses, quarantines and requests that fall in their own windows.
func isBackground(params map[string]any) bool {
	background, _ := params["background"].(bool)
	return background
}

// markerObservations are the faults-fired-and-ml-nodes-bound signals a marker can name in params.observe.
var markerObservations = map[string]bool{"host_rate_limited": true}

// defaultPauseRecovery bounds expected catch-up burns after unpause independently of timeout
// sweeping. It matches user.catchUpChunkTimeout; scenarios can choose a different bound.
const defaultPauseRecovery = 60 * time.Second

func parsePauseRecovery(raw string) (time.Duration, error) {
	if raw == "" {
		return defaultPauseRecovery, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("recovery must be a non-negative duration")
	}
	return d, nil
}

func validateEventParams(event Event) error {
	if event.Recovery != "" {
		if event.Action != "pause" {
			return fmt.Errorf("recovery is only for pause events")
		}
		if _, err := parsePauseRecovery(event.Recovery); err != nil {
			return err
		}
	}
	if isBackground(event.Params) && event.Action != "marker" {
		return fmt.Errorf("params.background is only for marker events")
	}
	switch event.Action {
	case "marker":
		if raw, ok := event.Params["observe"]; ok {
			if observe, _ := raw.(string); !markerObservations[observe] {
				return fmt.Errorf("params.observe must be host_rate_limited")
			}
		}
	case "throttle":
		if cpus, ok := numberParam(event.Params, "cpus"); !ok || cpus <= 0 {
			return fmt.Errorf("throttle requires params.cpus > 0")
		}
	case "container_kill", "gateway_restart":
		if raw, ok := event.Params["signal"]; ok {
			signal, isString := raw.(string)
			if !isString || !signalPattern.MatchString(signal) {
				return fmt.Errorf("params.signal must be a signal name such as KILL")
			}
		}
	case "ml_fault":
		patch, ok := event.Params["patch"].(map[string]any)
		if !ok || len(patch) == 0 {
			return fmt.Errorf("ml_fault requires a non-empty params.patch")
		}
		if _, err := zeroPatch(patch); err != nil {
			return err
		}
	case "tripfile":
		file, _ := event.Params["file"].(string)
		if file == "" || strings.ContainsAny(file, `/\`) || file == "." || file == ".." {
			return fmt.Errorf("tripfile requires params.file as a bare file name")
		}
	case "epoch_advance":
		if raw, ok := event.Params["phases"]; ok {
			phases, isString := raw.(string)
			if !isString || event.Until == "" {
				return fmt.Errorf("params.phases must be a string such as 0s=PoCGenerate,40s=PoCValidate and needs until")
			}
			if _, err := gatewayphase.ParseSteps(phases); err != nil {
				return fmt.Errorf("params.phases: %w", err)
			}
		}
	case "chain_participant":
		if mode, _ := event.Params["mode"].(string); mode != "unavailable" && mode != "absent" {
			return fmt.Errorf("chain_participant requires params.mode unavailable or absent")
		}
	}
	return nil
}

// pocPhaseSteps is the phase timeline of a windowed epoch_advance:
// params.phases ("at=phase,...", offsets from the window start) or, by
// default, PoCGenerate for the first two thirds and PoCValidate after.
func pocPhaseSteps(params map[string]any, window time.Duration) []gatewayphase.StepJSON {
	if raw, _ := params["phases"].(string); raw != "" {
		if parsed, err := gatewayphase.ParseSteps(raw); err == nil {
			steps := make([]gatewayphase.StepJSON, 0, len(parsed))
			for _, step := range parsed {
				steps = append(steps, gatewayphase.StepJSON{At: step.At.String(), Phase: step.Phase, ConfirmationPoCPhase: step.ConfirmationPoCPhase})
			}
			return steps
		}
	}
	return []gatewayphase.StepJSON{
		{At: "0s", Phase: gatewayphase.PhasePoCGenerate},
		{At: (window * 2 / 3).String(), Phase: gatewayphase.PhasePoCValidate},
	}
}

func numberParam(params map[string]any, key string) (float64, bool) {
	switch value := params[key].(type) {
	case int:
		return float64(value), true
	case float64:
		return value, true
	case string:
		parsed, err := strconv.ParseFloat(value, 64)
		return parsed, err == nil
	}
	return 0, false
}

func eventSignal(params map[string]any) string {
	if signal, _ := params["signal"].(string); signal != "" {
		return signal
	}
	return "KILL"
}

// zeroPatch builds the revert for a mock-openai FaultPatch: every field the
// event set is sent back at its zero value.
func zeroPatch(patch map[string]any) (map[string]any, error) {
	revert := make(map[string]any, len(patch))
	for key, value := range patch {
		switch value.(type) {
		case bool:
			revert[key] = false
		case int, float64:
			revert[key] = 0
		case string:
			revert[key] = ""
		default:
			return nil, fmt.Errorf("params.patch.%s must be a bool, number or string", key)
		}
	}
	return revert, nil
}

// eventOps performs container-level actions; tests replace it with fakes.
type eventOps interface {
	Compose(ctx context.Context, args ...string) error
	CPULimit(ctx context.Context, host string) (float64, error)
	Throttle(ctx context.Context, host string, cpus float64) error
	Tripfile(ctx context.Context, host, path string, present bool) error
}

// mockControl drives the mock admin routes: ml_fault on mock-openai,
// participant and epoch on mock-chain, challenge and phase on mock-dapi.
type mockControl interface {
	MLFault(ctx context.Context, node string, patch map[string]any) error
	ChainParticipant(ctx context.Context, address, mode string) error
	EpochAdvance(ctx context.Context) error
	DAPIChallenge(ctx context.Context, host string, challenged bool) error
	GatewayPhase(ctx context.Context, steps []gatewayphase.StepJSON) error
}

// plannedEvent is an event resolved against the generated topology.
type plannedEvent struct {
	Event
	window   eventWindow
	address  string
	tripPath string
}

type eventStep func(context.Context) error

func eventSteps(event plannedEvent, ops eventOps, mocks mockControl) (apply, revert eventStep) {
	target := event.Target
	switch event.Action {
	case "pause":
		return func(ctx context.Context) error { return ops.Compose(ctx, "pause", target) },
			func(ctx context.Context) error { return ops.Compose(ctx, "unpause", target) }
	case "throttle":
		cpus, _ := numberParam(event.Params, "cpus")
		// The revert puts back the limit read before the throttle; if that read failed nothing was throttled, so there is nothing to put back.
		var before float64
		read := false
		return func(ctx context.Context) error {
				limit, err := ops.CPULimit(ctx, target)
				if err != nil {
					return err
				}
				before, read = limit, true
				return ops.Throttle(ctx, target, cpus)
			},
			func(ctx context.Context) error {
				if !read {
					return nil
				}
				return ops.Throttle(ctx, target, before)
			}
	case "container_restart":
		return func(ctx context.Context) error { return ops.Compose(ctx, "restart", target) },
			func(ctx context.Context) error { return ops.Compose(ctx, "start", target) }
	case "container_kill", "gateway_restart":
		signal := eventSignal(event.Params)
		return func(ctx context.Context) error { return ops.Compose(ctx, "kill", "-s", signal, target) },
			func(ctx context.Context) error { return ops.Compose(ctx, "start", target) }
	case "ml_fault":
		patch := event.Params["patch"].(map[string]any)
		revertPatch, _ := zeroPatch(patch)
		return func(ctx context.Context) error { return mocks.MLFault(ctx, target, patch) },
			func(ctx context.Context) error { return mocks.MLFault(ctx, target, revertPatch) }
	case "tripfile":
		return func(ctx context.Context) error { return ops.Tripfile(ctx, target, event.tripPath, true) },
			func(ctx context.Context) error { return ops.Tripfile(ctx, target, event.tripPath, false) }
	case "chain_participant":
		mode, _ := event.Params["mode"].(string)
		return func(ctx context.Context) error { return mocks.ChainParticipant(ctx, event.address, mode) },
			func(ctx context.Context) error { return mocks.ChainParticipant(ctx, event.address, "ok") }
	case "epoch_advance":
		if event.window.end <= event.window.start {
			return func(ctx context.Context) error { return mocks.EpochAdvance(ctx) }, nil
		}
		// A windowed advance reports PoC phases for the window, then moves
		// the chain to the next epoch and returns the stub to Inference.
		steps := pocPhaseSteps(event.Params, event.window.end-event.window.start)
		return func(ctx context.Context) error { return mocks.GatewayPhase(ctx, steps) },
			func(ctx context.Context) error {
				if err := mocks.EpochAdvance(ctx); err != nil {
					return err
				}
				return mocks.GatewayPhase(ctx, nil)
			}
	case "dapi_challenge":
		return func(ctx context.Context) error { return mocks.DAPIChallenge(ctx, target, true) },
			func(ctx context.Context) error { return mocks.DAPIChallenge(ctx, target, false) }
	case "marker":
		// A marker injects nothing. It gives a fault that the topology or the
		// traffic causes, such as a tight peer budget, a window and a case.
		noop := func(context.Context) error { return nil }
		return noop, noop
	}
	return func(context.Context) error { return fmt.Errorf("unknown action %q", event.Action) }, nil
}

const eventCommandTimeout = 15 * time.Second

// dockerEventTimeout bounds one Docker CLI step. Under full load a compose
// pause or kill took longer than 15 s, so the apply timed out without a trace.
const dockerEventTimeout = 90 * time.Second

func eventStepTimeout(action string) time.Duration {
	switch action {
	case "pause", "throttle", "container_restart", "container_kill", "gateway_restart", "tripfile":
		return dockerEventTimeout
	}
	return eventCommandTimeout
}

func logEventError(event plannedEvent, step string, began time.Time, err error) {
	log.Printf("loadtest: event=%s action=%s target=%s stage=error step=%s took=%s error=%q", event.ID, event.Action, event.Target, step, time.Since(began).Round(time.Millisecond), err.Error())
}

func executeEvent(ctx context.Context, started time.Time, event plannedEvent, ops eventOps, mocks mockControl) (record EventRecord) {
	record = EventRecord{
		ID: event.ID, Case: event.Case, Action: event.Action, Target: event.Target, Params: event.Params,
		Recovery:     event.Recovery,
		PlannedStart: started.Add(event.window.start).UTC(), PlannedEnd: started.Add(event.window.end).UTC(),
	}
	var runErr error
	defer func() {
		if runErr != nil {
			record.Error = runErr.Error()
		}
	}()
	if !waitForArrival(ctx, started.Add(event.window.start)) {
		runErr = ctx.Err()
		return
	}
	apply, revert := eventSteps(event, ops, mocks)
	reverted := revert == nil
	// The revert also runs after a failed or cancelled apply, because a
	// failure can be ambiguous; it uses its own context so cancel still cleans up.
	defer func() {
		if reverted {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), eventStepTimeout(event.Action))
		defer cancel()
		began := time.Now()
		cleanupErr := revert(cleanup)
		if cleanupErr != nil {
			logEventError(event, "cleanup", began, cleanupErr)
		} else if record.StartedAt != nil {
			ended := time.Now().UTC()
			record.EndedAt = &ended
			log.Printf("loadtest: event=%s action=%s target=%s stage=reverted step=cleanup", event.ID, event.Action, event.Target)
		}
		runErr = errors.Join(runErr, cleanupErr)
	}()
	began := time.Now()
	runErr = withCommandTimeout(ctx, eventStepTimeout(event.Action), apply)
	if runErr != nil {
		logEventError(event, "apply", began, runErr)
		return
	}
	// The fault takes effect somewhere inside apply, so its window opens when apply began.
	applied, faultStart := time.Now().UTC(), began.UTC()
	record.StartedAt = &faultStart
	log.Printf("loadtest: event=%s action=%s target=%s stage=applied took=%s", event.ID, event.Action, event.Target, applied.Sub(began).Round(time.Millisecond))
	if revert == nil {
		record.EndedAt = &applied
		return
	}
	if !waitForArrival(ctx, started.Add(event.window.end)) {
		runErr = ctx.Err()
		return
	}
	began = time.Now()
	runErr = withCommandTimeout(ctx, eventStepTimeout(event.Action), revert)
	if runErr != nil {
		logEventError(event, "revert", began, runErr)
		return
	}
	reverted = true
	ended := time.Now().UTC()
	record.EndedAt = &ended
	log.Printf("loadtest: event=%s action=%s target=%s stage=reverted", event.ID, event.Action, event.Target)
	return
}

func withCommandTimeout(ctx context.Context, timeout time.Duration, step eventStep) error {
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return step(commandCtx)
}

// runEventLanes executes each lane in start order; lanes run concurrently.
// records[i] belongs to events[i].
func runEventLanes(ctx context.Context, started time.Time, events []plannedEvent, nodeHosts map[string]string, ops eventOps, mocks mockControl) []EventRecord {
	records := make([]EventRecord, len(events))
	lanes := map[string][]int{}
	for i, event := range events {
		lane := eventLane(event.Event, nodeHosts)
		if isBackground(event.Params) {
			// A background marker holds its window all run; on the host's lane
			// it would delay every other event on that host until it reverts.
			lane = "background/" + event.ID
		}
		lanes[lane] = append(lanes[lane], i)
	}
	var wg sync.WaitGroup
	for _, indices := range lanes {
		sort.SliceStable(indices, func(a, b int) bool { return events[indices[a]].window.start < events[indices[b]].window.start })
		wg.Add(1)
		go func(indices []int) {
			defer wg.Done()
			for _, i := range indices {
				records[i] = executeEvent(ctx, started, events[i], ops, mocks)
			}
		}(indices)
	}
	wg.Wait()
	return records
}

func planEvents(scenario Scenario, cfg *config.File) ([]plannedEvent, map[string]string, error) {
	nodeHosts := map[string]string{}
	for _, node := range scenario.Topology.MockML.Nodes {
		nodeHosts[node.Name] = node.Host
	}
	hosts := map[string]config.HostCfg{}
	for _, host := range cfg.Hosts {
		hosts[host.ID] = host
	}
	workload := scenario.Duration()
	var planned []plannedEvent
	for _, event := range scenario.TimelineEvents() {
		spec := eventActions[event.Action]
		window, err := parseEventWindow(event, spec, workload)
		if err != nil {
			return nil, nil, fmt.Errorf("event %s: %w", event.ID, err)
		}
		p := plannedEvent{Event: event, window: window}
		if spec.target == targetHost {
			host, ok := hosts[event.Target]
			if !ok {
				return nil, nil, fmt.Errorf("event %s target %s absent from generated topology", event.ID, event.Target)
			}
			p.address = participantAddress(cfg, host)
			dir := host.Env["DEVSHARD_TESTENV_EXECUTOR_FAULT_DIR"]
			if dir == "" {
				dir = "/tmp"
			}
			if file, _ := event.Params["file"].(string); file != "" {
				p.tripPath = dir + "/" + file
			}
		}
		planned = append(planned, p)
	}
	return planned, nodeHosts, nil
}

// participantAddress resolves an HA replica to the on-chain identity it serves.
func participantAddress(cfg *config.File, host config.HostCfg) string {
	for _, candidate := range cfg.Hosts {
		if host.KeyName != "" && candidate.ID == host.KeyName {
			return candidate.Address
		}
	}
	return host.Address
}

type composeEventOps struct {
	dir, project, compose string
	// docker runs one Docker CLI command and returns its trimmed stdout; tests replace it.
	docker func(ctx context.Context, args ...string) (string, error)
}

func (o composeEventOps) Compose(ctx context.Context, args ...string) error {
	// Same env as `up`, so compose resolves the project exactly as it was started.
	return runCommandWithEnv(ctx, o.dir, loadTestComposeEnv(), "docker", append([]string{"compose", "-p", o.project, "-f", o.compose}, args...)...)
}

// CPULimit reads the container's CPU limit in CPUs; 0 means it has none.
func (o composeEventOps) CPULimit(ctx context.Context, host string) (float64, error) {
	container, err := o.container(ctx, host)
	if err != nil {
		return 0, err
	}
	output, err := o.docker(ctx, "inspect", "-f", "{{.HostConfig.NanoCpus}}", container)
	if err != nil {
		return 0, err
	}
	nanoCPUs, err := strconv.ParseInt(output, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("read CPU limit of %s: %w", host, err)
	}
	return float64(nanoCPUs) / 1e9, nil
}

// Throttle sets the container CPU limit. docker update leaves the limit as it is for --cpus 0, so 0 lifts it to the host's CPU count, the highest Docker accepts.
func (o composeEventOps) Throttle(ctx context.Context, host string, cpus float64) error {
	container, err := o.container(ctx, host)
	if err != nil {
		return err
	}
	if cpus == 0 {
		output, err := o.docker(ctx, "info", "--format", "{{.NCPU}}")
		if err != nil {
			return err
		}
		hostCPUs, err := strconv.Atoi(output)
		if err != nil || hostCPUs < 1 {
			return fmt.Errorf("read Docker host CPU count from %q", output)
		}
		cpus = float64(hostCPUs)
	}
	_, err = o.docker(ctx, "update", "--cpus", strconv.FormatFloat(cpus, 'f', -1, 64), container)
	return err
}

func (o composeEventOps) container(ctx context.Context, host string) (string, error) {
	container, err := o.docker(ctx, "compose", "-p", o.project, "-f", o.compose, "ps", "-q", host)
	if err != nil {
		return "", fmt.Errorf("resolve container for %s: %w", host, err)
	}
	if container == "" {
		return "", fmt.Errorf("no running container for %s", host)
	}
	return container, nil
}

func dockerOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("docker %s: %w\n%s", strings.Join(args, " "), err, stderr.Bytes())
	}
	return strings.TrimSpace(string(output)), nil
}

func (o composeEventOps) Tripfile(ctx context.Context, host, path string, present bool) error {
	if present {
		return o.Compose(ctx, "exec", "-T", host, "touch", path)
	}
	return o.Compose(ctx, "exec", "-T", host, "rm", "-f", path)
}

type httpMockControl struct {
	client   *http.Client
	chainURL string
	dapiURL  string
	mlURLs   map[string]string
}

func newHTTPMockControl(cfg *config.File) httpMockControl {
	control := httpMockControl{
		client:   &http.Client{Timeout: eventCommandTimeout},
		chainURL: fmt.Sprintf("http://127.0.0.1:%d", cfg.MockChain.TestenvPort),
		dapiURL:  fmt.Sprintf("http://127.0.0.1:%d", cfg.MockDapi.HTTPPort),
		mlURLs:   map[string]string{},
	}
	port := cfg.MockOpenAI.HTTPPort
	if port == 0 {
		port = config.DefaultMockOpenAIHTTPPort
	}
	for i, node := range cfg.MockOpenAI.Nodes {
		control.mlURLs[node.Name] = fmt.Sprintf("http://127.0.0.1:%d", port+i)
	}
	return control
}

// MLFault posts a FaultPatch; lifting pause_stream also releases held streams.
func (c httpMockControl) MLFault(ctx context.Context, node string, patch map[string]any) error {
	base, ok := c.mlURLs[node]
	if !ok {
		return fmt.Errorf("unknown mock_ml node %s", node)
	}
	if err := c.post(ctx, base+"/testenv/fault", patch); err != nil {
		return err
	}
	if paused, set := patch["pause_stream"].(bool); set && !paused {
		return c.post(ctx, base+"/testenv/stream/release", map[string]any{})
	}
	return nil
}

func (c httpMockControl) ChainParticipant(ctx context.Context, address, mode string) error {
	return c.post(ctx, c.chainURL+"/testenv/participant", map[string]any{"address": address, "mode": mode})
}

func (c httpMockControl) EpochAdvance(ctx context.Context) error {
	return c.post(ctx, c.chainURL+"/testenv/epoch", map[string]any{"advance": true})
}

func (c httpMockControl) DAPIChallenge(ctx context.Context, host string, challenged bool) error {
	return c.post(ctx, c.dapiURL+"/testenv/challenge", map[string]any{"host": host, "on": challenged})
}

// GatewayPhase replaces mock-dapi's phase timeline; nil returns to Inference.
func (c httpMockControl) GatewayPhase(ctx context.Context, steps []gatewayphase.StepJSON) error {
	if steps == nil {
		steps = []gatewayphase.StepJSON{}
	}
	return c.post(ctx, c.dapiURL+"/testenv/phase", map[string]any{"steps": steps})
}

func (c httpMockControl) post(ctx context.Context, url string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		reply, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("POST %s returned %s: %s", url, response.Status, strings.TrimSpace(string(reply)))
	}
	return nil
}

type eventRunner struct {
	cancel    context.CancelFunc
	done      chan struct{}
	records   []EventRecord
	outputDir string
	once      sync.Once
	err       error
}

func startEvents(ctx context.Context, opts RunnerConfig, cfg *config.File, project, compose string, scenario Scenario) (*eventRunner, error) {
	planned, nodeHosts, err := planEvents(scenario, cfg)
	if err != nil || len(planned) == 0 {
		return nil, err
	}
	ops := composeEventOps{dir: opts.TestenvDir, project: project, compose: compose, docker: func(ctx context.Context, args ...string) (string, error) {
		return dockerOutput(ctx, opts.TestenvDir, args...)
	}}
	mocks := newHTTPMockControl(cfg)
	child, cancel := context.WithCancel(ctx)
	runner := &eventRunner{cancel: cancel, done: make(chan struct{}), outputDir: opts.OutputDir}
	started := time.Now()
	go func() {
		defer close(runner.done)
		runner.records = runEventLanes(child, started, planned, nodeHosts, ops, mocks)
	}()
	return runner, nil
}

func (r *eventRunner) finish(cancel bool) ([]EventRecord, error) {
	if r == nil {
		return nil, nil
	}
	r.once.Do(func() {
		if cancel {
			r.cancel()
		}
		<-r.done
		r.cancel()
		fired := 0
		for _, record := range r.records {
			if record.StartedAt != nil {
				fired++
			} else {
				log.Printf("loadtest: event=%s action=%s target=%s stage=not_fired error=%q", record.ID, record.Action, record.Target, record.Error)
			}
			switch {
			case record.Error != "":
				r.err = errors.Join(r.err, fmt.Errorf("event %s (%s %s): %s", record.ID, record.Action, record.Target, record.Error))
			case record.StartedAt == nil:
				r.err = errors.Join(r.err, fmt.Errorf("event %s (%s %s): never applied", record.ID, record.Action, record.Target))
			}
		}
		log.Printf("loadtest: stage=events fired=%d/%d", fired, len(r.records))
		r.err = errors.Join(r.err, writeEventRecords(r.outputDir, r.records))
	})
	return r.records, r.err
}

func writeEventRecords(outputDir string, records []EventRecord) error {
	if records == nil {
		records = []EventRecord{}
	}
	body, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outputDir, "events.json"), append(body, '\n'), 0o644)
}
