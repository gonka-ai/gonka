//go:build loadsim

package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
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

	devshardpkg "devshard"
	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/env"
	"devshard/cmd/gateway/registry"
	"devshard/cmd/gateway/store"
	"devshard/host"
	"devshard/internal/statetest"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/transport"
	"devshard/user"
)

const (
	loadSimulationModel = "loadsim-model"
	loadSimulationEpoch = 41
)

// loadSimulationSettings are the knobs of one run, read from LOADSIM_* variables. See ../CONTRIBUTING.md, "Load simulation".
type loadSimulationSettings struct {
	escrows           int
	groupSize         int
	requestsPerSecond int
	warmup            time.Duration
	duration          time.Duration
	inferenceLatency  time.Duration
	streamTokens      int
	maxInFlight       int
	reportEvery       time.Duration
	profileDir        string
}

func loadSimulationSettingsFromEnv(t *testing.T) loadSimulationSettings {
	t.Helper()
	readInt := func(name string, fallback int) int {
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
	readCount := func(name string) int {
		raw := os.Getenv(name)
		if raw == "" {
			return 0
		}
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			t.Fatalf("%s=%q must be a non-negative integer", name, raw)
		}
		return value
	}
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
		escrows:           readInt("LOADSIM_ESCROWS", 4),
		groupSize:         readInt("LOADSIM_GROUP_SIZE", 4),
		requestsPerSecond: readInt("LOADSIM_RPS", 30),
		warmup:            readDuration("LOADSIM_WARMUP", 5*time.Second),
		duration:          readDuration("LOADSIM_DURATION", 30*time.Second),
		inferenceLatency:  readDuration("LOADSIM_INFERENCE_LATENCY", 2*time.Second),
		streamTokens:      readCount("LOADSIM_STREAM_TOKENS"),
		maxInFlight:       readInt("LOADSIM_MAX_IN_FLIGHT", 512),
		reportEvery:       readDuration("LOADSIM_REPORT_EVERY", time.Minute),
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

// streamingEngine streams one delta event per token, spread over the latency, and stores the stream as the envelope
// a real host keeps, so the gateway parses, folds and forwards an answer event by event the way it does on a stand.
type streamingEngine struct {
	latency      time.Duration
	lines        []string
	body         []byte
	responseHash []byte
	servedHash   []byte
	outputTokens uint64
}

const (
	streamedDeltaLine = `data: {"id":"loadsim","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"t"}}]}`
	streamedDoneLine  = "data: [DONE]"
)

func newStreamingEngine(t *testing.T, latency time.Duration, tokens int) streamingEngine {
	t.Helper()
	lines := make([]string, 0, tokens+2)
	for range tokens {
		lines = append(lines, streamedDeltaLine)
	}
	lines = append(lines,
		fmt.Sprintf(`data: {"id":"loadsim","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":80,"completion_tokens":%d}}`, tokens),
		streamedDoneLine)
	body, err := json.Marshal(struct {
		Events []string `json:"events"`
	}{Events: lines})
	if err != nil {
		t.Fatalf("encoding the streamed envelope: %v", err)
	}
	served, err := stub.HashServedView(body)
	if err != nil {
		t.Fatalf("hashing the streamed envelope: %v", err)
	}
	responseHash := sha256.Sum256(body)
	return streamingEngine{latency: latency, lines: lines, body: body, responseHash: responseHash[:], servedHash: served, outputTokens: uint64(tokens)}
}

func (e streamingEngine) Execute(ctx context.Context, request devshardpkg.ExecuteRequest) (*devshardpkg.ExecuteResult, error) {
	if request.Recovery == devshardpkg.RecoveryStoredOnly {
		return nil, devshardpkg.ErrNoStoredResponse
	}
	interval := e.latency / time.Duration(len(e.lines))
	timer := time.NewTimer(interval)
	defer timer.Stop()
	flusher, flushes := request.ResponseWriter.(http.Flusher)
	for _, line := range e.lines {
		select {
		case <-timer.C:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		timer.Reset(interval)
		if request.ResponseWriter != nil {
			_, _ = io.WriteString(request.ResponseWriter, line+"\n\n")
			if flushes {
				flusher.Flush()
			}
		}
	}
	return &devshardpkg.ExecuteResult{
		ResponseHash: e.responseHash,
		ServedHash:   e.servedHash,
		InputTokens:  80,
		OutputTokens: e.outputTokens,
		ResponseBody: e.body,
	}, nil
}

// inProcessFleet serves every escrow from real hosts living in this process, so the gateway dials no network.
type inProcessFleet struct {
	sessions map[string]registry.EscrowSession
	machines []*state.StateMachine

	stepsMu sync.Mutex
	steps   map[string]stepTotal
}

type stepTotal struct {
	count   int
	elapsed time.Duration
	longest time.Duration
}

func (f *inProcessFleet) recordStep(step string, elapsed time.Duration) {
	f.stepsMu.Lock()
	defer f.stepsMu.Unlock()
	total := f.steps[step]
	total.count++
	total.elapsed += elapsed
	total.longest = max(total.longest, elapsed)
	f.steps[step] = total
}

func (f *inProcessFleet) stepTotals() map[string]stepTotal {
	f.stepsMu.Lock()
	defer f.stepsMu.Unlock()
	return maps.Clone(f.steps)
}

func stepMeansSince(previous, current map[string]stepTotal) string {
	means := make([]string, 0, len(current))
	for _, step := range slices.Sorted(maps.Keys(current)) {
		count := current[step].count - previous[step].count
		if count == 0 {
			continue
		}
		mean := (current[step].elapsed - previous[step].elapsed) / time.Duration(count)
		means = append(means, fmt.Sprintf("%s %s", step, mean.Round(time.Microsecond)))
	}
	return strings.Join(means, ", ")
}

func (f *inProcessFleet) reportSteps(t *testing.T) {
	f.stepsMu.Lock()
	defer f.stepsMu.Unlock()
	for _, step := range slices.Sorted(maps.Keys(f.steps)) {
		total := f.steps[step]
		t.Logf("step %s: %d times, mean %s, longest %s, %s in all",
			step, total.count, (total.elapsed / time.Duration(max(total.count, 1))).Round(time.Microsecond), total.longest.Round(time.Microsecond), total.elapsed.Round(time.Millisecond))
	}
}

// meanLiveInferences is the unsealed window a state root is recomputed over, averaged across the fleet's escrows.
func (f *inProcessFleet) meanLiveInferences() int {
	total := 0
	for _, machine := range f.machines {
		total += len(machine.SnapshotInferences())
	}
	return total / max(len(f.machines), 1)
}

// newInProcessFleet builds every session up front, on the test's goroutine, where a failed build may stop the test.
func newInProcessFleet(t *testing.T, settings loadSimulationSettings, escrowIDs []string) *inProcessFleet {
	t.Helper()
	fleet := &inProcessFleet{sessions: make(map[string]registry.EscrowSession, len(escrowIDs)), steps: map[string]stepTotal{}}
	storageRoot := t.TempDir()
	for _, escrowID := range escrowIDs {
		signers := make([]*signing.Secp256k1Signer, settings.groupSize)
		for index := range signers {
			signers[index] = testutil.MustGenerateKey(t)
		}
		group := testutil.MakeGroup(signers)
		configuration := testutil.DefaultConfig(len(group))
		creator := testutil.MustGenerateKey(t)
		verifier := signing.NewSecp256k1Verifier()
		clients := make([]user.HostClient, len(signers))
		for index, signer := range signers {
			hostMachine := statetest.MustStateMachine(t, escrowID, configuration, group, 1<<50, creator.Address(), verifier)
			var engine devshardpkg.InferenceEngine = delayedEngine{answer: stub.NewInferenceEngine(), latency: settings.inferenceLatency}
			if settings.streamTokens > 0 {
				engine = newStreamingEngine(t, settings.inferenceLatency, settings.streamTokens)
			}
			hostNode, err := host.NewHost(hostMachine, signer, engine, escrowID, group, nil, host.WithGrace(100))
			if err != nil {
				t.Fatalf("building host %d of %s: %v", index, escrowID, err)
			}
			clients[index] = &user.InProcessClient{Host: hostNode}
		}
		sessionStore, err := storage.NewSQLite(filepath.Join(storageRoot, escrowID))
		if err != nil {
			t.Fatalf("opening the store of %s: %v", escrowID, err)
		}
		if err := sessionStore.CreateSession(storage.CreateSessionParams{
			EscrowID: escrowID, Version: testutil.RuntimeTestVersion, CreatorAddr: creator.Address(),
			Config: configuration, Group: group, InitialBalance: 1 << 50,
		}); err != nil {
			t.Fatalf("creating the stored session of %s: %v", escrowID, err)
		}
		machine, err := state.NewStateMachine(escrowID, configuration, group, 1<<50, creator.Address(), verifier, sessionStore)
		if err != nil {
			t.Fatalf("building the state machine of %s: %v", escrowID, err)
		}
		session, err := user.NewSession(machine, creator, escrowID, group, clients, verifier,
			user.WithStorage(sessionStore), user.WithStepTimer(fleet.recordStep))
		if err != nil {
			t.Fatalf("opening the session of %s: %v", escrowID, err)
		}
		fleet.sessions[escrowID] = registry.NewSessionHandle(session, machine)
		fleet.machines = append(fleet.machines, machine)
	}
	return fleet
}

func (f *inProcessFleet) serving(_ context.Context, escrowID string) (registry.EscrowSession, error) {
	session, built := f.sessions[escrowID]
	if !built {
		return nil, fmt.Errorf("escrow %s is not part of the simulated fleet", escrowID)
	}
	return session, nil
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
func driveTraffic(ctx context.Context, client *http.Client, baseURL string, settings loadSimulationSettings, window time.Duration, outcome *trafficOutcome) {
	inFlight := make(chan struct{}, settings.maxInFlight)
	ticker := time.NewTicker(time.Second / time.Duration(settings.requestsPerSecond))
	defer ticker.Stop()
	deadline := time.After(window)
	var requests sync.WaitGroup
	defer requests.Wait()
	for sequence := 0; ; sequence++ {
		select {
		case <-ctx.Done():
			return
		case <-deadline:
			return
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

// reportIntervals logs what each interval cost, so a cost that grows with the escrows' live windows shows as a trend.
func reportIntervals(ctx context.Context, t *testing.T, every time.Duration, outcome *trafficOutcome, fleet *inProcessFleet) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	previous, err := readRuntime()
	if err != nil {
		t.Errorf("readRuntime() = %v, want nil", err)
		return
	}
	previousAnswered, started := 0, time.Now()
	previousSteps := fleet.stepTotals()
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
		t.Logf("at %s: %d live inferences per escrow, %d answered, cpu %.1f%% of one core, %.2f ms cpu and %.0f KB allocated per answered request, gc %.2f s",
			time.Since(started).Round(time.Second), fleet.meanLiveInferences(), answered-previousAnswered,
			100*cpuSeconds/every.Seconds(), 1000*cpuSeconds/requests,
			float64(current.allocatedBytes-previous.allocatedBytes)/1e3/requests, current.gcCPUSeconds-previous.gcCPUSeconds)
		currentSteps := fleet.stepTotals()
		t.Logf("at %s: step means over the interval: %s", time.Since(started).Round(time.Second), stepMeansSince(previousSteps, currentSteps))
		previous, previousAnswered, previousSteps = current, answered, currentSteps
	}
}

// runtimeReading is the slice of runtime/metrics a window is judged by.
type runtimeReading struct {
	processCPU     time.Duration
	gcCPUSeconds   float64
	allocatedBytes uint64
	gcCycles       uint64
	goroutines     uint64
}

func readRuntime() (runtimeReading, error) {
	samples := []metrics.Sample{
		{Name: "/cpu/classes/gc/total:cpu-seconds"},
		{Name: "/gc/heap/allocs:bytes"},
		{Name: "/gc/cycles/total:gc-cycles"},
		{Name: "/sched/goroutines:goroutines"},
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
	}, nil
}

func quantile(sorted []time.Duration, fraction float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	return sorted[min(len(sorted)-1, int(fraction*float64(len(sorted))))]
}

func writeProfile(t *testing.T, directory, name string) {
	t.Helper()
	file, err := os.Create(filepath.Join(directory, name+".pprof"))
	if err != nil {
		t.Fatalf("creating the %s profile: %v", name, err)
	}
	if err := errors.Join(pprof.Lookup(name).WriteTo(file, 0), file.Close()); err != nil {
		t.Fatalf("writing the %s profile: %v", name, err)
	}
}

func waitUntilServing(t *testing.T, client *http.Client, baseURL string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get(baseURL + "/metrics")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the gateway did not serve /metrics within 30s")
}

// Test flow:
//  1. Seed LOADSIM_ESCROWS escrows, each served by LOADSIM_GROUP_SIZE real hosts in this process whose inference takes LOADSIM_INFERENCE_LATENCY.
//  2. Compose and serve the gateway exactly as run() does, with the chain replaced by fakes.
//  3. Send streaming chat requests at LOADSIM_RPS for LOADSIM_WARMUP, then for LOADSIM_DURATION under CPU, mutex and block profiling.
//  4. Report throughput, latency quantiles, CPU and allocation per request, and write the profiles to LOADSIM_PROFILE_DIR.
func TestLoadSimulation(t *testing.T) {
	settings := loadSimulationSettingsFromEnv(t)
	gatewayEnvironment(t)
	port := freePort(t)
	t.Setenv("DEVSHARD_PORT", strconv.Itoa(port))
	t.Setenv("DEVSHARD_ADMIN_API_KEY", "loadsim-admin-api-key")
	t.Setenv("DEVSHARD_REQUIRE_HEIGHT_SEED", "false")
	t.Setenv("DEVSHARD_STATS_ENABLED", "true")
	t.Setenv("DEVSHARD_STATS_PORT", strconv.Itoa(freePort(t)))

	values, err := env.Load()
	if err != nil {
		t.Fatalf("env.Load() = %v, want nil", err)
	}
	storageDir, err := resolveStorageDir(values.StorageDir)
	if err != nil {
		t.Fatalf("resolveStorageDir() = %v, want nil", err)
	}
	gatewayStore, err := store.Open(storageDir)
	if err != nil {
		t.Fatalf("store.Open() = %v, want nil", err)
	}
	if err := gatewayStore.SaveOverrides(t.Context(), config.Overrides{ModelAccess: map[string]string{loadSimulationModel: config.ModelAccessOpen}}); err != nil {
		t.Fatalf("SaveOverrides() = %v, want nil", err)
	}
	escrowIDs := make([]string, 0, settings.escrows)
	for index := range settings.escrows {
		escrowID := strconv.Itoa(1000 + index)
		escrowIDs = append(escrowIDs, escrowID)
		if err := gatewayStore.UpsertDevshard(t.Context(), store.DevshardRecord{
			EscrowID: escrowID, Model: loadSimulationModel, Active: true, RotationEpoch: loadSimulationEpoch,
		}); err != nil {
			t.Fatalf("UpsertDevshard() = %v, want nil", err)
		}
	}
	fleet := newInProcessFleet(t, settings, escrowIDs)
	sources := func(config.Chain, config.HeightSync, string, *transport.PeerRPCAdoption, user.StepTimer) (chainSources, error) {
		return chainSources{
			Serving:   fleet.serving,
			ReadOnly:  readOnlySessions(gatewayStore, storageDir),
			Reader:    chainServingModels{models: map[string]chain.ModelParams{loadSimulationModel: {ContextWindow: 32768, MaxModelLen: 32768}}},
			Transport: chainWithoutADial{},
		}, nil
	}
	composed, err := compose(t.Context(), values, storageDir, gatewayStore, sources)
	if err != nil {
		t.Fatalf("compose() = %v, want nil", err)
	}

	serving, stopServing := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- composed.serve(serving) }()
	t.Cleanup(func() {
		stopServing()
		select {
		case err := <-served:
			if err != nil {
				t.Logf("serve() = %v: the timeout votes in-process hosts leave short keep a few races past the shutdown grace; the window above is unaffected", err)
			}
		case <-time.After(30 * time.Second):
			t.Error("serve() did not return within 30s of cancellation")
		}
	})

	baseURL := "http://127.0.0.1:" + strconv.Itoa(port)
	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: settings.maxInFlight}, Timeout: 2 * time.Minute}
	waitUntilServing(t, client, baseURL)

	warmed := newTrafficOutcome()
	driveTraffic(t.Context(), client, baseURL, settings, settings.warmup, warmed)
	t.Logf("warmup: statuses %v, errors %v, shed %d", warmed.statuses, warmed.errors, warmed.shed)

	profileDir := settings.profileDir
	if profileDir == "" {
		profileDir = t.TempDir()
	}
	runtime.SetMutexProfileFraction(5)
	runtime.SetBlockProfileRate(int(10 * time.Microsecond))
	t.Cleanup(func() {
		runtime.SetMutexProfileFraction(0)
		runtime.SetBlockProfileRate(0)
	})
	cpuProfile, err := os.Create(filepath.Join(profileDir, "cpu.pprof"))
	if err != nil {
		t.Fatalf("creating the cpu profile: %v", err)
	}
	if err := pprof.StartCPUProfile(cpuProfile); err != nil {
		t.Fatalf("StartCPUProfile() = %v, want nil", err)
	}
	t.Cleanup(pprof.StopCPUProfile)
	before, err := readRuntime()
	if err != nil {
		t.Fatalf("readRuntime() = %v, want nil", err)
	}
	started := time.Now()
	measured := newTrafficOutcome()
	reporting, stopReporting := context.WithCancel(t.Context())
	var reporter sync.WaitGroup
	reporter.Go(func() { reportIntervals(reporting, t, settings.reportEvery, measured, fleet) })
	driveTraffic(t.Context(), client, baseURL, settings, settings.duration, measured)
	stopReporting()
	reporter.Wait()
	elapsed := time.Since(started)
	after, err := readRuntime()
	if err != nil {
		t.Fatalf("readRuntime() = %v, want nil", err)
	}
	pprof.StopCPUProfile()
	if err := cpuProfile.Close(); err != nil {
		t.Fatalf("closing the cpu profile: %v", err)
	}
	writeProfile(t, profileDir, "mutex")
	writeProfile(t, profileDir, "block")
	writeProfile(t, profileDir, "heap")

	slices.Sort(measured.latencies)
	answered := len(measured.latencies)
	cpuSeconds := (after.processCPU - before.processCPU).Seconds()
	perRequest := func(total float64) float64 {
		if answered == 0 {
			return 0
		}
		return total / float64(answered)
	}
	t.Logf("window %s at %d rps: %d answered 200, statuses %v, errors %v, shed %d",
		elapsed.Round(time.Millisecond), settings.requestsPerSecond, answered, measured.statuses, measured.errors, measured.shed)
	t.Logf("latency p50 %s, p90 %s, p99 %s, max %s",
		quantile(measured.latencies, 0.50), quantile(measured.latencies, 0.90),
		quantile(measured.latencies, 0.99), quantile(measured.latencies, 1))
	t.Logf("cpu %.2f s (%.1f%% of one core), gc %.2f s, %.2f ms cpu per answered request",
		cpuSeconds, 100*cpuSeconds/elapsed.Seconds(), after.gcCPUSeconds-before.gcCPUSeconds, 1000*perRequest(cpuSeconds))
	t.Logf("allocated %.1f MB (%.1f KB per answered request), %d gc cycles, %d goroutines at the end",
		float64(after.allocatedBytes-before.allocatedBytes)/1e6, perRequest(float64(after.allocatedBytes-before.allocatedBytes))/1e3,
		after.gcCycles-before.gcCycles, after.goroutines)
	fleet.reportSteps(t)
	t.Logf("profiles in %s: go tool pprof -top -nodecount=40 %s", profileDir, filepath.Join(profileDir, "cpu.pprof"))
	if answered == 0 {
		t.Fatal("no request was answered 200: the simulation measured nothing")
	}
}
