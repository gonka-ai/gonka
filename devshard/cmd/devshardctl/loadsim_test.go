//go:build loadsim

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"runtime/metrics"
	"runtime/pprof"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	devshardpkg "devshard"
	"devshard/host"
	"devshard/internal/statetest"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/user"
)

const (
	loadSimulationModel   = "loadsim-model"
	loadSimulationBalance = 1 << 50
)

// loadSimulationSettings are the knobs of one run, read from LOADSIM_* variables. See devshard/docs/gateway-load-simulation.md.
type loadSimulationSettings struct {
	escrows           int
	groupSize         int
	requestsPerSecond int
	warmup            time.Duration
	duration          time.Duration
	inferenceLatency  time.Duration
	maxInFlight       int
	reportEvery       time.Duration
	profileDir        string
}

// readPositiveIntEnv reads a simulation knob, falling back when it is unset and failing the test when it is not a positive integer.
func readPositiveIntEnv(t *testing.T, name string, fallback int) int {
	t.Helper()
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		t.Fatalf("%s=%q must be a positive integer", name, raw)
	}
	return value
}

func loadSimulationSettingsFromEnv(t *testing.T) loadSimulationSettings {
	t.Helper()
	readDuration := func(name string, fallback time.Duration) time.Duration {
		raw := os.Getenv(name)
		if raw == "" {
			return fallback
		}
		value, err := time.ParseDuration(raw)
		if err != nil || value < 0 {
			t.Fatalf("%s=%q must be a non-negative duration", name, raw)
		}
		return value
	}
	settings := loadSimulationSettings{
		escrows:           readPositiveIntEnv(t, "LOADSIM_ESCROWS", 4),
		groupSize:         readPositiveIntEnv(t, "LOADSIM_GROUP_SIZE", 4),
		requestsPerSecond: readPositiveIntEnv(t, "LOADSIM_RPS", 30),
		warmup:            readDuration("LOADSIM_WARMUP", 5*time.Second),
		duration:          readDuration("LOADSIM_DURATION", 30*time.Second),
		inferenceLatency:  readDuration("LOADSIM_INFERENCE_LATENCY", 2*time.Second),
		maxInFlight:       readPositiveIntEnv(t, "LOADSIM_MAX_IN_FLIGHT", 512),
		reportEvery:       readDuration("LOADSIM_REPORT_EVERY", 30*time.Second),
		profileDir:        os.Getenv("LOADSIM_PROFILE_DIR"),
	}
	if settings.reportEvery <= 0 {
		t.Fatalf("LOADSIM_REPORT_EVERY=%s must be positive", settings.reportEvery)
	}
	return settings
}

// delayedEngine answers like the stub after a fixed latency, so in-flight windows fill the way real inferences fill them.
type delayedEngine struct {
	answer  *stub.InferenceEngine
	latency time.Duration
}

func (e delayedEngine) Execute(ctx context.Context, request devshardpkg.ExecuteRequest) (*devshardpkg.ExecuteResult, error) {
	timer := time.NewTimer(e.latency)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return e.answer.Execute(ctx, request)
}

// inProcessFleet is every simulated escrow: a gateway runtime over a SQLite-backed session whose hosts live in this process.
type inProcessFleet struct {
	runtimes []*devshardRuntime
	sessions []*user.Session
	machines []*state.StateMachine
	stores   []*storage.SQLite
}

// meanLiveInferences is the unsealed window a state root is recomputed over, averaged across the fleet's escrows.
func (f *inProcessFleet) meanLiveInferences() int {
	total := 0
	for _, machine := range f.machines {
		total += len(machine.SnapshotInferences())
	}
	return total / max(len(f.machines), 1)
}

// meanNonce is how far the escrows have advanced, which is what the in-memory diff history grows with.
func (f *inProcessFleet) meanNonce() uint64 {
	var total uint64
	for _, session := range f.sessions {
		total += session.Nonce()
	}
	return total / uint64(max(len(f.sessions), 1))
}

func (f *inProcessFleet) close() {
	for _, escrowRuntime := range f.runtimes {
		escrowRuntime.stopRedundancy()
		_ = escrowRuntime.close()
	}
	for _, store := range f.stores {
		_ = store.Close()
	}
}

// newInProcessFleet builds each runtime the way buildRuntime does, with in-process hosts in place of the HTTP transport.
func newInProcessFleet(t *testing.T, settings loadSimulationSettings, storageDir string) *inProcessFleet {
	t.Helper()
	fleet := &inProcessFleet{}
	perf := NewPerfTracker(nil)
	for index := range settings.escrows {
		escrowID := strconv.Itoa(1000 + index)
		signers := make([]*signing.Secp256k1Signer, settings.groupSize)
		for slot := range signers {
			signers[slot] = testutil.MustGenerateKey(t)
		}
		group := testutil.MakeGroup(signers)
		configuration := testutil.DefaultConfig(len(group))
		creator := testutil.MustGenerateKey(t)
		verifier := signing.NewSecp256k1Verifier()
		clients := make([]user.HostClient, len(signers))
		for slot, signer := range signers {
			hostMachine := statetest.MustStateMachine(t, escrowID, configuration, group, loadSimulationBalance, creator.Address(), verifier)
			engine := delayedEngine{answer: stub.NewInferenceEngine(), latency: settings.inferenceLatency}
			hostNode, err := host.NewHost(hostMachine, signer, engine, escrowID, group, nil, host.WithGrace(100))
			require.NoError(t, err, "building host %d of %s", slot, escrowID)
			clients[slot] = &user.InProcessClient{Host: hostNode}
		}
		store, err := storage.NewSQLite(filepath.Join(storageDir, escrowID))
		require.NoError(t, err, "opening the store of %s", escrowID)
		fleet.stores = append(fleet.stores, store)
		require.NoError(t, store.CreateSession(storage.CreateSessionParams{
			EscrowID: escrowID, Version: "loadsim", CreatorAddr: creator.Address(),
			Config: configuration, Group: group, InitialBalance: loadSimulationBalance,
		}))
		machine, err := state.NewStateMachine(escrowID, configuration, group, loadSimulationBalance, creator.Address(), verifier, store)
		require.NoError(t, err, "building the state machine of %s", escrowID)
		session, err := user.NewSession(machine, creator, escrowID, group, clients, verifier, user.WithStorage(store))
		require.NoError(t, err, "opening the session of %s", escrowID)
		redundancy := NewRedundancy(session, perf, len(clients), loadSimulationModel)
		proxy := &Proxy{session: session, sm: machine, escrowID: escrowID, model: loadSimulationModel, redundancy: redundancy, perf: perf}
		escrowRuntime := &devshardRuntime{
			stopped:               make(chan struct{}),
			id:                    escrowID,
			model:                 loadSimulationModel,
			handler:               newRuntimeMux(proxy),
			proxy:                 proxy,
			session:               session,
			participantKeys:       session.ParticipantKeys(),
			participantSlotCounts: hostSlotCounts(session.HostParticipantKeyList()),
		}
		escrowRuntime.active.Store(true)
		escrowRuntime.activeConfigured = true
		fleet.runtimes = append(fleet.runtimes, escrowRuntime)
		fleet.sessions = append(fleet.sessions, session)
		fleet.machines = append(fleet.machines, machine)
	}
	return fleet
}

// newLoadSimulationGateway wires the fleet into the pooled gateway and its HTTP handler, the way mustBuildGateway and buildGatewayHandler do.
func newLoadSimulationGateway(t *testing.T, fleet *inProcessFleet, storageDir string) http.Handler {
	t.Helper()
	settings := GatewaySettings{
		DefaultModel: loadSimulationModel,
		ModelLimits:  []GatewayModelLimitSettings{{ModelID: loadSimulationModel, AccessMode: string(gatewayAccessModeOpen)}},
	}.WithTuningDefaults()
	gateway := NewGateway(fleet.runtimes, NewGatewayLimiter(0, 0), loadSimulationModel)
	gateway.settings = settings
	gatewayStore, err := NewGatewayStore(filepath.Join(storageDir, "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = gatewayStore.Close() })
	states := make([]GatewayDevshardState, 0, len(fleet.runtimes))
	for _, escrowRuntime := range fleet.runtimes {
		states = append(states, GatewayDevshardState{RuntimeConfig: RuntimeConfig{ID: escrowRuntime.id, Model: escrowRuntime.model}, Active: true})
	}
	require.NoError(t, gatewayStore.Initialize(settings, states))
	gateway.store = gatewayStore
	return buildGatewayHandler(gateway, runtimeOptions{adminAPIKey: "loadsim-admin-api-key"})
}

// trafficOutcome is what the generator saw of one window of traffic.
type trafficOutcome struct {
	mu        sync.Mutex
	latencies []time.Duration
	statuses  map[int]int
	errors    map[string]int
	shed      int
}

func newTrafficOutcome() *trafficOutcome {
	return &trafficOutcome{statuses: map[int]int{}, errors: map[string]int{}}
}

func (o *trafficOutcome) answered() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.latencies)
}

func (o *trafficOutcome) record(latency time.Duration, status int, failure error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if failure != nil {
		o.errors[failure.Error()]++
		return
	}
	o.statuses[status]++
	if status == http.StatusOK {
		o.latencies = append(o.latencies, latency)
	}
}

// driveTraffic sends requests at a fixed rate whatever the gateway answers, the way clients do, and sheds past maxInFlight.
func driveTraffic(ctx context.Context, client *http.Client, baseURL string, settings loadSimulationSettings, window time.Duration, firstSequence int, outcome *trafficOutcome) int {
	inFlight := make(chan struct{}, settings.maxInFlight)
	ticker := time.NewTicker(time.Second / time.Duration(settings.requestsPerSecond))
	defer ticker.Stop()
	deadline := time.After(window)
	var requests sync.WaitGroup
	defer requests.Wait()
	for sequence := firstSequence; ; sequence++ {
		select {
		case <-ctx.Done():
			return sequence
		case <-deadline:
			return sequence
		case <-ticker.C:
		}
		select {
		case inFlight <- struct{}{}:
		default:
			outcome.mu.Lock()
			outcome.shed++
			outcome.mu.Unlock()
			continue
		}
		body := fmt.Sprintf(`{"model":%q,"stream":true,"max_tokens":64,"messages":[{"role":"user","content":"load simulation %d"}]}`,
			loadSimulationModel, sequence)
		requests.Go(func() {
			defer func() { <-inFlight }()
			started := time.Now()
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v1/chat/completions", strings.NewReader(body))
			if err != nil {
				outcome.record(0, 0, err)
				return
			}
			request.Header.Set("Content-Type", "application/json")
			response, err := client.Do(request)
			if err != nil {
				outcome.record(0, 0, err)
				return
			}
			_, copyErr := io.Copy(io.Discard, response.Body)
			closeErr := response.Body.Close()
			if copyErr != nil || closeErr != nil {
				outcome.record(0, 0, fmt.Errorf("reading the answer: %w", errors.Join(copyErr, closeErr)))
				return
			}
			outcome.record(time.Since(started), response.StatusCode, nil)
		})
	}
}

// reportIntervals logs what each interval cost, so a cost or a heap that grows with the escrows' history shows as a trend.
func reportIntervals(ctx context.Context, t *testing.T, every time.Duration, outcome *trafficOutcome, fleet *inProcessFleet) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	previous, err := readRuntime()
	if err != nil {
		t.Errorf("readRuntime() = %v, want nil", err)
		return
	}
	previousAnswered, started := 0, time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		current, err := readRuntime()
		if err != nil {
			t.Errorf("readRuntime() = %v, want nil", err)
			return
		}
		answered := outcome.answered()
		requests := float64(max(answered-previousAnswered, 1))
		cpuSeconds := (current.processCPU - previous.processCPU).Seconds()
		t.Logf("at %s: nonce %d, %d live inferences per escrow, %d answered, cpu %.1f%% of one core, %.2f ms cpu and %.0f KB allocated per answered request, gc %.2f s, live heap %.1f MB",
			time.Since(started).Round(time.Second), fleet.meanNonce(), fleet.meanLiveInferences(), answered-previousAnswered,
			100*cpuSeconds/every.Seconds(), 1000*cpuSeconds/requests,
			float64(current.allocatedBytes-previous.allocatedBytes)/1e3/requests, current.gcCPUSeconds-previous.gcCPUSeconds,
			float64(current.liveHeapBytes)/1e6)
		previous, previousAnswered = current, answered
	}
}

// runtimeReading is the slice of runtime/metrics a window is judged by.
type runtimeReading struct {
	processCPU     time.Duration
	gcCPUSeconds   float64
	allocatedBytes uint64
	gcCycles       uint64
	goroutines     uint64
	liveHeapBytes  uint64
}

func readRuntime() (runtimeReading, error) {
	samples := []metrics.Sample{
		{Name: "/cpu/classes/gc/total:cpu-seconds"},
		{Name: "/gc/heap/allocs:bytes"},
		{Name: "/gc/cycles/total:gc-cycles"},
		{Name: "/sched/goroutines:goroutines"},
		{Name: "/gc/heap/live:bytes"},
	}
	metrics.Read(samples)
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		return runtimeReading{}, fmt.Errorf("getrusage: %w", err)
	}
	return runtimeReading{
		processCPU:     time.Duration(usage.Utime.Nano() + usage.Stime.Nano()),
		gcCPUSeconds:   samples[0].Value.Float64(),
		allocatedBytes: samples[1].Value.Uint64(),
		gcCycles:       samples[2].Value.Uint64(),
		goroutines:     samples[3].Value.Uint64(),
		liveHeapBytes:  samples[4].Value.Uint64(),
	}, nil
}

func writeProfile(t *testing.T, directory, name string) {
	t.Helper()
	file, err := os.Create(filepath.Join(directory, name+".pprof"))
	require.NoError(t, err, "creating the %s profile", name)
	require.NoError(t, errors.Join(pprof.Lookup(name).WriteTo(file, 0), file.Close()), "writing the %s profile", name)
}

// Test flow:
//  1. Build LOADSIM_ESCROWS runtimes, each a SQLite-backed session over LOADSIM_GROUP_SIZE real hosts in this process whose inference takes LOADSIM_INFERENCE_LATENCY.
//  2. Serve them through the pooled gateway handler over a real HTTP listener.
//  3. Send streaming chat requests at LOADSIM_RPS for LOADSIM_WARMUP, then for LOADSIM_DURATION under CPU, mutex and block profiling.
//  4. Report throughput, latency quantiles, CPU, allocation and live heap, and write the profiles to LOADSIM_PROFILE_DIR.
func TestLoadSimulation(t *testing.T) {
	settings := loadSimulationSettingsFromEnv(t)
	storageDir := t.TempDir()
	fleet := newInProcessFleet(t, settings, storageDir)
	handler := newLoadSimulationGateway(t, fleet, storageDir)
	server := httptest.NewServer(handler)
	t.Cleanup(func() {
		server.Close()
		fleet.close()
	})
	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: settings.maxInFlight}, Timeout: 2 * time.Minute}

	warmed := newTrafficOutcome()
	nextSequence := driveTraffic(t.Context(), client, server.URL, settings, settings.warmup, 0, warmed)
	t.Logf("warmup: statuses %v, errors %v, shed %d", warmed.statuses, warmed.errors, warmed.shed)

	profileDir := settings.profileDir
	if profileDir == "" {
		profileDir = t.TempDir()
	}
	require.NoError(t, os.MkdirAll(profileDir, 0o755))
	runtime.SetMutexProfileFraction(5)
	runtime.SetBlockProfileRate(int(10 * time.Microsecond))
	t.Cleanup(func() {
		runtime.SetMutexProfileFraction(0)
		runtime.SetBlockProfileRate(0)
	})
	cpuProfile, err := os.Create(filepath.Join(profileDir, "cpu.pprof"))
	require.NoError(t, err)
	require.NoError(t, pprof.StartCPUProfile(cpuProfile))
	t.Cleanup(pprof.StopCPUProfile)
	before, err := readRuntime()
	require.NoError(t, err)
	started := time.Now()
	measured := newTrafficOutcome()
	reporting, stopReporting := context.WithCancel(t.Context())
	var reporter sync.WaitGroup
	reporter.Go(func() { reportIntervals(reporting, t, settings.reportEvery, measured, fleet) })
	driveTraffic(t.Context(), client, server.URL, settings, settings.duration, nextSequence, measured)
	stopReporting()
	reporter.Wait()
	elapsed := time.Since(started)
	after, err := readRuntime()
	require.NoError(t, err)
	pprof.StopCPUProfile()
	require.NoError(t, cpuProfile.Close())
	writeProfile(t, profileDir, "mutex")
	writeProfile(t, profileDir, "block")
	runtime.GC()
	writeProfile(t, profileDir, "heap")
	writeProfile(t, profileDir, "allocs")

	slices.Sort(measured.latencies)
	answered := len(measured.latencies)
	cpuSeconds := (after.processCPU - before.processCPU).Seconds()
	perRequest := func(total float64) float64 {
		if answered == 0 {
			return 0
		}
		return total / float64(answered)
	}
	t.Logf("window %s at %d rps: %d answered 200, statuses %v, errors %v, shed %d, mean nonce %d",
		elapsed.Round(time.Millisecond), settings.requestsPerSecond, answered, measured.statuses, measured.errors, measured.shed, fleet.meanNonce())
	t.Logf("latency p50 %s, p90 %s, p99 %s, max %s",
		percentileDuration(measured.latencies, 0.50), percentileDuration(measured.latencies, 0.90),
		percentileDuration(measured.latencies, 0.99), percentileDuration(measured.latencies, 1))
	t.Logf("cpu %.2f s (%.1f%% of one core), gc %.2f s, %.2f ms cpu per answered request",
		cpuSeconds, 100*cpuSeconds/elapsed.Seconds(), after.gcCPUSeconds-before.gcCPUSeconds, 1000*perRequest(cpuSeconds))
	t.Logf("allocated %.1f MB (%.1f KB per answered request), %d gc cycles, live heap %.1f MB, %d goroutines at the end",
		float64(after.allocatedBytes-before.allocatedBytes)/1e6, perRequest(float64(after.allocatedBytes-before.allocatedBytes))/1e3,
		after.gcCycles-before.gcCycles, float64(after.liveHeapBytes)/1e6, after.goroutines)
	t.Logf("profiles in %s: go tool pprof -top -nodecount=40 %s", profileDir, filepath.Join(profileDir, "cpu.pprof"))
	if answered == 0 {
		t.Fatal("no request was answered 200: the simulation measured nothing")
	}
}
