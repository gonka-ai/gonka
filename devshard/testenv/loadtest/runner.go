package loadtest

import (
	"context"
	"debug/elf"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"devshard/testenv/config"
	"devshard/testenv/mockchain/ledgerface"
	"devshard/testenv/replay"
)

type RunnerConfig struct {
	ScenarioPath    string
	ProfilesDir     string
	TestenvDir      string
	OutputDir       string
	KeepStack       bool
	LoadDataset     string
	MetricsInterval time.Duration
	// GatewayDriver overrides the scenario's topology.gateway when set.
	GatewayDriver config.GatewayDriver
	// StrictXFail fails the run when a known_bug case passes (XPASS).
	StrictXFail bool
	// CheckHistory is the file of outcomes that span runs (see recordCheckHistory); empty means check-history.json beside OutputDir.
	CheckHistory string
}

type RunResult struct {
	Summary            Summary
	Terminal           TerminalSummary
	Assertions         []AssertionResult
	OutputDir          string
	WorkDir            string
	GatewayURL         string
	Allocations        map[string]uint64
	MLStats            map[string]MLNodeStats
	GatewayState       GatewayStateSizes
	Metrics            MetricsSummary
	Events             []EventRecord
	CPUProfiles        []CPUProfileResult
	ContentionProfiles []ContentionProfileResult
	ProfileSnapshots   []ProfileSnapshotResult
	Accounting         *AccountingReport
}

type MLNodeStats struct {
	Allocations         uint64 `json:"allocations"`
	RequestsReceived    uint64 `json:"requests_received"`
	SuccessfulResponses uint64 `json:"successful_responses"`
	FailedResponses     uint64 `json:"failed_responses"`
	Timeouts            uint64 `json:"timeouts"`
	ReplayHits          uint64 `json:"replay_hits"`
	ReplayMisses        uint64 `json:"replay_misses"`
	Error               string `json:"error,omitempty"`
}

type GatewayStateSizes struct {
	Diffs           int     `json:"diffs"`
	DiffsBytes      int64   `json:"diffs_bytes"`
	DiffsMB         float64 `json:"diffs_mb"`
	SignatureNonces int     `json:"signature_nonces"`
	NonceStates     int     `json:"nonce_states"`
	PendingTxs      int     `json:"pending_txs"`
	AppliedTxKeys   int     `json:"applied_tx_keys"`
}

// AssertionResult is one human-readable load-test assertion outcome.
type AssertionResult struct {
	Name     string `json:"name"`
	Passed   bool   `json:"passed"`
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
	Details  string `json:"details,omitempty"`
	// Case links the check to a scenario case; Status is XFAIL or XPASS for
	// known_bug cases, or INVALID when a harness stall covers the check.
	Case   string `json:"case,omitempty"`
	Status string `json:"status,omitempty"`
	// spans are the windows the check judged; see markInvalid.
	spans []timeSpan
	// fixOnRepeat marks a failure that is a FIX only when it repeats; see recordCheckHistory.
	fixOnRepeat bool
}

type assertionFailures struct {
	Checks []AssertionResult
}

func (e *assertionFailures) Error() string {
	messages := make([]string, 0, len(e.Checks))
	for _, check := range e.Checks {
		messages = append(messages, formatAssertionFailure(check))
	}
	return fmt.Sprintf("%d assertions failed: %s", len(messages), strings.Join(messages, "; "))
}

func formatAssertionFailure(check AssertionResult) string {
	verdict := "failed"
	if check.Status == statusInvalid {
		verdict = "INVALID (harness stall)"
	}
	message := fmt.Sprintf("assertion %s %s: expected %s; actual %s", check.Name, verdict, check.Expected, check.Actual)
	if check.Details != "" {
		message += "; details: " + check.Details
	}
	return message
}

// TerminalSummary describes all DevShard inference records observed during the
// orphan check. It can differ from the number of client requests because the
// gateway may serve cache hits or create speculative attempts.
// Sealed counts unfinished records of deactivated escrows, whose settlement
// escrow-accounting-reconciles judges, except challenged ones under a pending-age bound on an escrow not yet sealed;
// InFlight counts unfinished records still within the pending-age bound.
type TerminalSummary struct {
	Finished int `json:"finished"`
	Ghost    int `json:"ghost"`
	// GhostExcluded counts burns left out of Ghost and Total: burns a fault caused on a host inside its fault window.
	GhostExcluded int `json:"ghost_excluded"`
	Total         int `json:"total"`
	Orphaned      int `json:"orphaned"`
	Sealed        int `json:"sealed"`
	InFlight      int `json:"in_flight"`
	// ChallengedAtSeal counts the sealed records still challenged on an escrow settled on chain or parked with settlement off.
	ChallengedAtSeal         int            `json:"challenged_at_seal"`
	ChallengedAtSealByEscrow map[string]int `json:"challenged_at_seal_by_escrow,omitempty"`
	GhostRate                float64        `json:"ghost_rate"`
	Statuses                 map[string]int `json:"statuses,omitempty"`
}

func RunScenario(ctx context.Context, opts RunnerConfig) (result RunResult, err error) {
	if opts.ScenarioPath == "" || opts.TestenvDir == "" || opts.OutputDir == "" {
		return RunResult{}, fmt.Errorf("scenario path, testenv directory, and output directory are required")
	}
	result.OutputDir = opts.OutputDir
	if opts.MetricsInterval != 0 && opts.MetricsInterval < 100*time.Millisecond {
		return result, fmt.Errorf("metrics interval must be at least 100ms")
	}
	scenario, err := LoadScenario(opts.ScenarioPath)
	if err != nil {
		return RunResult{}, err
	}
	if err := opts.GatewayDriver.Validate(); err != nil {
		return RunResult{}, err
	}
	if opts.GatewayDriver != "" {
		scenario.Topology.Gateway = opts.GatewayDriver
	}
	driver := scenario.Topology.Gateway
	var replaySelector *replay.Selector
	var replayModels []string
	if opts.LoadDataset != "" {
		dataset, err := replay.LoadFile(opts.LoadDataset)
		if err != nil {
			return RunResult{}, err
		}
		replaySelector = replay.NewSelector(dataset, scenario.Seed)
		replayModels = dataset.Models()
	}
	if err := os.MkdirAll(opts.OutputDir, 0o755); err != nil {
		return RunResult{}, fmt.Errorf("create result directory: %w", err)
	}
	defer func() {
		if err != nil {
			_ = writeAssertions(opts.OutputDir, result.Assertions, err)
		}
	}()
	if err := copyFile(opts.ScenarioPath, filepath.Join(opts.OutputDir, "run.yaml")); err != nil {
		return RunResult{}, err
	}
	profilesDir := opts.ProfilesDir
	if profilesDir == "" {
		profilesDir = filepath.Join(opts.TestenvDir, "loadtest", "profiles")
	}
	profiles := make(map[string]Profile, len(scenario.Topology.MockML.Nodes))
	for _, node := range scenario.Topology.MockML.Nodes {
		if _, exists := profiles[node.Profile]; exists {
			continue
		}
		profile, err := LoadProfile(profilesDir, node.Profile)
		if err != nil {
			return RunResult{}, err
		}
		profiles[node.Profile] = profile
	}
	workDir, err := os.MkdirTemp(opts.TestenvDir, "loadtest-")
	if err != nil {
		return RunResult{}, fmt.Errorf("create testenv work directory: %w", err)
	}
	result.WorkDir = workDir
	if !opts.KeepStack {
		defer func() { _ = os.RemoveAll(workDir) }()
	}
	if opts.LoadDataset != "" {
		if err := copyFile(opts.LoadDataset, filepath.Join(workDir, "replay.jsonl")); err != nil {
			return RunResult{}, fmt.Errorf("copy load dataset: %w", err)
		}
	}

	composePath := filepath.Join(workDir, "docker-compose.yml")
	project := ""
	composeEnv := loadTestComposeEnv()
	defer func() {
		if opts.KeepStack || project == "" {
			return
		}
		_ = runCommandWithEnv(context.Background(), opts.TestenvDir, composeEnv, "docker", "compose", "-p", project, "-f", composePath, "down", "--volumes", "--remove-orphans")
	}()

	if err := refuseConcurrentStacks(ctx, listComposeProjects); err != nil {
		return RunResult{}, err
	}
	dockerArch, err := dockerServerArch(ctx)
	if err != nil {
		return RunResult{}, err
	}
	const maxStartAttempts = 3
	log.Printf("loadtest: stage=stack_start scenario=%s gateway=%s", scenario.Scenario, gatewayDriverName(driver))
	log.Printf("loadtest: peer_rpc_transport=h2 endpoint=versiond-router:8080 grpc=true")
	for attempt := 1; attempt <= maxStartAttempts; attempt++ {
		project = "loadtest-" + strconv.FormatInt(time.Now().UnixNano(), 36)
		log.Printf("loadtest: starting isolated Docker stack (attempt %d/%d, project %s)", attempt, maxStartAttempts, project)
		if err := writeRunnerConfig(opts.TestenvDir, workDir, scenario, profiles, replaySelector != nil, replayModels); err != nil {
			return RunResult{}, err
		}
		if err := runCommand(ctx, opts.TestenvDir, "go", "run", "./cmd/gencompose", "-config", filepath.Join(workDir, "config.yaml"), "-out", composePath); err != nil {
			return RunResult{}, err
		}
		if err := fixComposePaths(composePath, opts.TestenvDir); err != nil {
			return RunResult{}, err
		}
		if err := checkChildBinaryArch(filepath.Join(opts.TestenvDir, "..", "..", "build", "devshardd"), dockerArch); err != nil {
			return RunResult{}, err
		}
		err := runCommandWithEnv(ctx, opts.TestenvDir, composeEnv, "docker", "compose", "-p", project, "-f", composePath, "up", "--build", "--wait", "--detach")
		if err == nil {
			break
		}
		log.Printf("loadtest: Docker stack start failed: %v", err)
		_ = writeComposeLogs(opts.OutputDir, opts.TestenvDir, project, composePath, scenario.Diagnostics.LogTail())
		_ = runCommandWithEnv(context.Background(), opts.TestenvDir, composeEnv, "docker", "compose", "-p", project, "-f", composePath, "down", "--volumes", "--remove-orphans")
		if !isAddressInUse(err) || attempt == maxStartAttempts {
			return RunResult{}, err
		}
	}
	if project == "" {
		return RunResult{}, fmt.Errorf("start load-test Compose project")
	}

	cfg, err := config.Load(filepath.Join(workDir, "config.yaml"))
	if err != nil {
		return RunResult{}, err
	}
	initialEscrowIDs := configuredEscrowIDs(cfg)
	apiKey, err := envValue(filepath.Join(workDir, ".env"), "TESTENV_ADMIN_API_KEY")
	if err != nil {
		return RunResult{}, err
	}
	result.GatewayURL = fmt.Sprintf("http://127.0.0.1:%d", cfg.Devshardctl.Port)
	accountingURL := fmt.Sprintf("http://127.0.0.1:%d", cfg.Devshardctl.StatsPort)
	stateGatewayURL := result.GatewayURL
	if replaySelector != nil || len(scenario.RequestModels()) > 1 {
		// Replay and multi-model stacks configure Gateway through DEVSHARDS_JSON, so even a
		// single dataset model uses the multi-DevShard debug route.
		stateGatewayURL = scopedGatewayURL(result.GatewayURL, config.PrimaryEscrowID(cfg))
	}
	debugGatewayURL := stateGatewayURL
	aggregateDebug := scenario.Gateway.EscrowRotation.Enabled || driver.IsGateway()
	if aggregateDebug {
		// Rotation adds runtimes while the test is running. The orphan check
		// discovers and inspects all of them through the Gateway root.
		debugGatewayURL = result.GatewayURL
	}
	log.Printf("loadtest: waiting for gateway at %s", result.GatewayURL)
	if err := waitGateway(ctx, result.GatewayURL); err != nil {
		_ = writeComposeLogs(opts.OutputDir, opts.TestenvDir, project, composePath, scenario.Diagnostics.LogTail())
		return RunResult{}, err
	}
	var accountingInitial accountingAPIResponse
	if accountingEnabled(scenario.Assertions.Accounting) {
		log.Printf("loadtest: stage=accounting_baseline")
		accountingInitial, err = waitForAccountingSnapshot(ctx, accountingURL, "baseline")
		if err != nil {
			_ = writeComposeLogs(opts.OutputDir, opts.TestenvDir, project, composePath, scenario.Diagnostics.LogTail())
			return result, fmt.Errorf("collect accounting baseline: %w", err)
		}
	}
	rotationModels := rotationModelIDs(cfg, replayModels, scenario)
	applySettings := applyGatewayScenarioSettings
	if driver.IsGateway() {
		applySettings = applyCmdGatewayScenarioSettings
	}
	if err := applySettings(ctx, result.GatewayURL, apiKey, scenario, rotationModels); err != nil {
		_ = writeComposeLogs(opts.OutputDir, opts.TestenvDir, project, composePath, scenario.Diagnostics.LogTail())
		return RunResult{}, err
	}
	log.Printf("loadtest: stage=gateway_ready secondary_wait_after_winner=%s", scenario.SecondaryWaitAfterWinner())
	collector, err := startMetricsCollector(ctx, opts, cfg, project, composePath, result.GatewayURL, apiKey)
	if err != nil {
		return result, fmt.Errorf("start metrics collector: %w", err)
	}
	defer func() { result.Metrics = collector.stop() }()
	collector.phase.Store("workload")
	profiler, err := startCPUProfiler(ctx, opts, cfg, project, composePath, result.GatewayURL, apiKey, scenario.Diagnostics.CPUProfiles, scenario.Diagnostics.ContentionProfiles)
	if err != nil {
		return result, fmt.Errorf("start CPU profiler: %w", err)
	}
	defer func() { result.CPUProfiles, result.ContentionProfiles = profiler.stop() }()
	snapshots, err := startSnapshotProfiler(ctx, opts, cfg, project, composePath, result.GatewayURL, apiKey, scenario.Diagnostics)
	if err != nil {
		return result, fmt.Errorf("start snapshot profiler: %w", err)
	}
	defer func() { result.ProfileSnapshots = snapshots.stop() }()

	var invariants *invariantRun
	if scenario.Assertions.Invariants.Enabled {
		invariants, err = startInvariantRun(ctx, opts, cfg, project, composePath, result.GatewayURL, accountingURL, apiKey, scenario, time.Now())
		if err != nil {
			return result, fmt.Errorf("start invariant collection: %w", err)
		}
		defer invariants.stop()
	}
	faultRunner, err := startEvents(ctx, opts, cfg, project, composePath, scenario)
	if err != nil {
		return result, err
	}
	defer func() {
		results, faultErr := faultRunner.finish(true)
		result.Events = results
		if err != nil {
			err = errors.Join(err, faultErr)
		}
	}()

	log.Printf("loadtest: stage=workload_start scenario=%s", scenario.Scenario)
	summary, err := RunGenerator(ctx, GeneratorConfig{
		GatewayURL: result.GatewayURL,
		APIKey:     apiKey,
		Scenario:   scenario,
		OutputDir:  opts.OutputDir,
		Replay:     replaySelector,
	})
	if err != nil {
		_ = writeComposeLogs(opts.OutputDir, opts.TestenvDir, project, composePath, scenario.Diagnostics.LogTail())
		return RunResult{}, err
	}
	result.Summary = summary
	result.Events, err = faultRunner.finish(false)
	faultErr := err
	collector.phase.Store("drain")
	allocations, err := fetchAllocations(ctx, cfg.MockDapi.HTTPPort)
	if err != nil {
		_ = writeComposeLogs(opts.OutputDir, opts.TestenvDir, project, composePath, scenario.Diagnostics.LogTail())
		return RunResult{}, err
	}
	result.Allocations = allocations
	if err := writeComposeLogsTo(opts.OutputDir, opts.TestenvDir, project, composePath, "compose-pre-drain.log", scenario.Diagnostics.LogTail()); err != nil {
		return RunResult{}, err
	}
	burns, err := readGatewayBurns(filepath.Join(opts.OutputDir, "compose-pre-drain.log"))
	if err != nil {
		return RunResult{}, err
	}
	inputs := RunInputs{Gateway: resolvedDriver(driver), Started: summary.StartedAt, Allocations: allocations, InitialEscrowIDs: sortedKeySet(initialEscrowIDs), Burns: burns}
	for _, host := range cfg.Hosts {
		inputs.Hosts = append(inputs.Hosts, InvariantHost{ID: host.ID, Participant: participantAddress(cfg, host)})
	}
	if invariants != nil {
		inputs.Started = invariants.started
	}
	if scenario.Assertions.EscrowRotation.RequireNewEscrow {
		inputs.CurrentEscrowIDs, err = fetchGatewayDevshardIDs(ctx, debugGatewayURL, apiKey)
		if err != nil {
			inputs.CurrentEscrowError = err.Error()
		}
	}
	if scenario.Assertions.Devshard.NoOrphanedWork {
		judge := orphanJudgeFor(scenario, inputs, result.Events)
		judge.ChainURL = fmt.Sprintf("http://127.0.0.1:%d", cfg.MockChain.TestenvPort)
		poll, terminal, _ := waitForNoOrphanedWork(ctx, debugGatewayURL, apiKey, judge, aggregateDebug)
		inputs.Orphans, result.Terminal = &poll, terminal
	}
	if accountingEnabled(scenario.Assertions.Accounting) {
		log.Printf("loadtest: stage=accounting_collection")
		accountingFinal, accountingErr := waitForAccountingSnapshot(ctx, accountingURL, "final")
		if accountingErr != nil {
			inputs.AccountingError = "final accounting snapshot: " + accountingErr.Error()
		} else if report, reportErr := buildAccountingReport(cfg, scenario.Assertions.Accounting.TargetHost, accountingInitial, accountingFinal); reportErr != nil {
			inputs.AccountingError = "target participant: " + reportErr.Error()
		} else {
			inputs.Accounting, result.Accounting = &report, &report
			if err := writeAccountingReport(opts.OutputDir, report); err != nil {
				log.Printf("loadtest: write accounting.json: %v", err)
			}
		}
	}
	if invariants != nil {
		log.Printf("loadtest: stage=invariants")
		end := invariants.collectEndState(ctx, rotationModels, filepath.Join(opts.OutputDir, "compose-pre-drain.log"))
		inputs.Invariants = &end
	}
	if inputs.Invariants == nil {
		ledger := chainLedger{}
		if ledger.SettleTxs, ledger.Escrows, err = fetchChainLedger(ctx, fmt.Sprintf("http://127.0.0.1:%d", cfg.MockChain.TestenvPort)); err != nil {
			ledger.Error = err.Error()
		}
		inputs.ChainLedger = &ledger
	}
	if err := writeRunInputs(opts.OutputDir, inputs); err != nil {
		return result, fmt.Errorf("write %s: %w", runInputsFile, err)
	}
	assertions, _, err := recheckRun(opts.OutputDir, opts.StrictXFail, opts.CheckHistory)
	if err != nil {
		return result, fmt.Errorf("evaluate checks: %w", err)
	}
	result.Assertions = assertions
	var assertionErr error
	if failures := failedChecks(assertions); len(failures) > 0 {
		assertionErr = &assertionFailures{Checks: failures}
	} else if faultErr != nil {
		assertionErr = faultErr
	}
	log.Printf("loadtest: stage=artifacts_collection")
	if err := writeComposeLogs(opts.OutputDir, opts.TestenvDir, project, composePath, scenario.Diagnostics.LogTail()); err != nil {
		if assertionErr == nil {
			return RunResult{}, err
		}
		log.Printf("loadtest: final Compose log collection failed: %v", err)
	}
	if driver.IsGateway() {
		log.Printf("loadtest: gateway state sizes skipped: cmd/gateway serves no /v1/debug/state-sizes")
	} else {
		gatewayState, stateErr := fetchGatewayStateSizes(ctx, stateGatewayURL, apiKey)
		if stateErr == nil {
			result.GatewayState = gatewayState
			if err := writeGatewayStateSizes(opts.OutputDir, gatewayState); err != nil && assertionErr == nil {
				return RunResult{}, err
			}
		} else if assertionErr == nil {
			return RunResult{}, stateErr
		}
	}
	mlStats, statsErr := fetchMLNodeStats(ctx, cfg.MockDapi.HTTPPort)
	if statsErr == nil {
		result.MLStats = mlStats
		if err := writeMLNodeStats(opts.OutputDir, mlStats); err != nil && assertionErr == nil {
			return RunResult{}, err
		}
	} else if assertionErr == nil {
		return RunResult{}, statsErr
	}
	_ = writeGatewayInferences(ctx, stateGatewayURL, apiKey, opts.OutputDir)
	if assertionErr != nil {
		return result, assertionErr
	}
	if err := writeAssertions(opts.OutputDir, result.Assertions, nil); err != nil {
		return RunResult{}, err
	}
	return result, nil
}

func applyGatewayScenarioSettings(ctx context.Context, gatewayURL, apiKey string, scenario Scenario, rotationModels []string) error {
	duration := scenario.SecondaryWaitAfterWinner()

	settings := map[string]any{
		"redundancy": map[string]any{
			"secondary_wait_after_winner_ms": duration.Milliseconds(),
		},
	}
	throttle := map[string]any{}
	for name, value := range scenario.Gateway.ParticipantThrottle.settings() {
		if value == "" {
			continue
		}
		quarantine, err := time.ParseDuration(value)
		if err != nil || quarantine < time.Millisecond {
			return fmt.Errorf("invalid gateway.participant_throttle.%s: %q", name, value)
		}
		throttle[name+"_ms"] = quarantine.Milliseconds()
	}
	if len(throttle) > 0 {
		settings["participant_throttle"] = throttle
	}
	if scenario.Gateway.EscrowRotation.Enabled {
		rotation := scenario.Gateway.EscrowRotation
		models, err := rotationModelSettings(rotation, rotationModels)
		if err != nil {
			return err
		}
		settings["escrow_rotation"] = map[string]any{
			"enabled":            true,
			"settlement_enabled": rotation.SettlementEnabled,
			"pre_poc_blocks":     rotation.PrePoCBlocks,
			"models":             models,
		}
	}

	body, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("marshal gateway settings: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, gatewayURL+"/v1/admin/settings", strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("create gateway settings request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+apiKey)
	}
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		return fmt.Errorf("apply gateway settings: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		reply, _ := io.ReadAll(response.Body)
		return fmt.Errorf("apply gateway settings returned %s: %s", response.Status, strings.TrimSpace(string(reply)))
	}
	return nil
}

// rotationModelSettings expands the scenario rotation into one model entry per rotated model, with
// the per-model settlement overrides; both drivers take this shape.
func rotationModelSettings(rotation GatewayEscrowRotation, rotationModels []string) ([]map[string]any, error) {
	overrides := map[string]*bool{}
	for _, model := range rotation.Models {
		overrides[model.Model] = model.SettlementEnabled
	}
	models := make([]map[string]any, 0, len(rotationModels))
	for _, modelID := range rotationModels {
		model := map[string]any{
			"model_id":        modelID,
			"temp_count":      rotation.TempCount,
			"target_count":    rotation.TargetCount,
			"amount":          rotation.Amount,
			"private_key_env": rotation.PrivateKeyEnv,
		}
		if enabled, ok := overrides[modelID]; ok {
			delete(overrides, modelID)
			if enabled != nil {
				model["settlement_enabled"] = *enabled
			}
		}
		models = append(models, model)
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("gateway.escrow_rotation is enabled but no model IDs were found")
	}
	for modelID := range overrides {
		return nil, fmt.Errorf("gateway.escrow_rotation.models: %s is not a rotated model", modelID)
	}
	return models, nil
}

func rotationModelIDs(cfg *config.File, replayModels []string, scenario Scenario) []string {
	if !scenario.Gateway.EscrowRotation.Enabled {
		return nil
	}
	seen := make(map[string]struct{})
	capacity := len(replayModels) + 1
	if cfg != nil {
		capacity += len(cfg.Escrows)
	}
	models := make([]string, 0, capacity)
	add := func(modelID string) {
		modelID = strings.TrimSpace(modelID)
		if modelID == "" {
			return
		}
		if _, ok := seen[modelID]; ok {
			return
		}
		seen[modelID] = struct{}{}
		models = append(models, modelID)
	}
	for _, modelID := range replayModels {
		add(modelID)
	}
	if cfg != nil {
		for _, escrow := range cfg.Escrows {
			add(escrow.ModelID)
		}
	}
	for _, modelID := range scenario.RequestModels() {
		add(modelID)
	}
	return models
}

// baseRunnerConfig loads config/config.yaml. That file is generated and git-ignored,
// so a clean worktree starts from the gencompose defaults instead.
func baseRunnerConfig(testenvDir string) (*config.File, error) {
	cfg, err := config.Load(filepath.Join(testenvDir, "config", "config.yaml"))
	if !errors.Is(err, os.ErrNotExist) {
		return cfg, err
	}
	cfg = &config.File{
		Hosts:   []config.HostCfg{{ID: "versiond-0"}, {ID: "versiond-1"}, {ID: "versiond-2"}},
		Escrows: []config.Escrow{{ID: 1, ModelID: config.DefaultModelID}},
	}
	cfg.ApplyDefaults()
	return cfg, nil
}

func writeRunnerConfig(testenvDir, workDir string, scenario Scenario, profiles map[string]Profile, replayEnabled bool, replayModels []string) error {
	cfg, err := baseRunnerConfig(testenvDir)
	if err != nil {
		return err
	}
	cfg.Versiond.Mode = scenario.Topology.VersiondMode
	cfg.Devshardctl.Driver = scenario.Topology.Gateway
	cfg.Versiond.DisablePeriodicSnapshots = scenario.Diagnostics.DisablePeriodicSnapshots
	cfg.Versiond.EnableContentionProfiles = scenario.Diagnostics.ContentionProfiles
	cfg.Postgres.PerParticipant = scenario.Topology.Storage == "per_participant"
	configureParticipants(cfg, scenario.Topology.Participants)
	cfg.Params.MaxNonce = scenario.Topology.Chain.MaxNonce
	if scenario.Protocol.RefusalTimeout != "" {
		refusal, err := protocolTimeoutSeconds(scenario.Protocol.RefusalTimeout)
		if err != nil {
			return err
		}
		execution, err := protocolTimeoutSeconds(scenario.Protocol.ExecutionTimeout)
		if err != nil {
			return err
		}
		cfg.Params.RefusalTimeout = refusal
		cfg.Params.ExecutionTimeout = execution
	}
	if replayEnabled {
		if err := configureReplayModels(cfg, replayModels); err != nil {
			return err
		}
	} else if models := scenario.RequestModels(); len(models) > 1 {
		// A multi-model mix needs one escrow and epoch group per model, as replay does.
		if err := configureReplayModels(cfg, models); err != nil {
			return err
		}
	}
	for _, host := range scenario.Topology.Hosts {
		for i := range cfg.Hosts {
			if cfg.Hosts[i].ID == host.ID {
				cfg.Hosts[i].Env = host.ResolvedEnv()
			}
		}
	}
	for i := range cfg.Escrows {
		cfg.Escrows[i].Amount = scenario.Topology.Chain.EscrowAmount
	}
	cfg.MockOpenAI.Nodes = make([]config.MockOpenAINodeCfg, 0, len(scenario.Topology.MockML.Nodes))
	cfg.MockOpenAI.ReplayFile = ""
	if replayEnabled {
		cfg.MockOpenAI.ReplayFile = "/fixtures/replay.jsonl"
	}
	for i, node := range scenario.Topology.MockML.Nodes {
		profile := profiles[node.Profile]
		cfg.MockOpenAI.Nodes = append(cfg.MockOpenAI.Nodes, config.MockOpenAINodeCfg{
			Name:             node.Name,
			TTFT:             profile.TTFT,
			TokenInterval:    profile.TokenInterval,
			Workers:          profile.Workers,
			Queue:            profile.Queue,
			Hang:             profile.Hang,
			FailureRate:      profile.FailureRate,
			HTTPStatus:       profile.HTTPStatus,
			TTFTP99:          profile.TTFTP99,
			TokenIntervalP99: profile.TokenIntervalP99,
			// Offset by node index so jittered nodes do not draw in lockstep.
			JitterSeed: uint64(scenario.Seed) + uint64(i),
			Host:       node.Host,
		})
	}
	if err := randomizeTestenv(cfg); err != nil {
		return err
	}
	return cfg.Save(filepath.Join(workDir, "config.yaml"))
}

// configureReplayModels makes the generated chain and Gateway topology match
// the models captured in the replay dataset. Each model needs its own escrow
// because the on-chain escrow model_id is the Gateway runtime identity.
func configureReplayModels(cfg *config.File, models []string) error {
	if cfg == nil {
		return fmt.Errorf("configure replay models: nil config")
	}
	if len(models) == 0 {
		return fmt.Errorf("configure replay models: dataset contains no models")
	}
	if len(cfg.Escrows) == 0 {
		cfg.Escrows = []config.Escrow{{ID: 1}}
	}

	templateEscrow := cfg.Escrows[0]
	if templateEscrow.ID == 0 {
		templateEscrow.ID = 1
	}
	templateGroup := config.EpochGroupBinding{
		EpochIndex:          cfg.Epoch.Index,
		ValidationThreshold: 95,
		ValidationExponent:  -2,
	}
	if len(cfg.EpochGroups) > 0 {
		templateGroup = cfg.EpochGroups[0]
		if templateGroup.EpochIndex == 0 {
			templateGroup.EpochIndex = cfg.Epoch.Index
		}
	}

	escrows := make([]config.Escrow, 0, len(models))
	groups := make([]config.EpochGroupBinding, 0, len(models))
	for index, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			return fmt.Errorf("configure replay models: model %d is empty", index)
		}
		escrow := templateEscrow
		escrow.ID = templateEscrow.ID + uint64(index)
		escrow.ModelID = model
		escrows = append(escrows, escrow)

		group := templateGroup
		group.ModelID = model
		groups = append(groups, group)
	}
	cfg.Escrows = escrows
	cfg.EpochGroups = groups
	return nil
}

// configureParticipants keeps the default HA pair as one participant and
// adds independent solo hosts for every additional participant requested by a
// scenario. One escrow slot per identity makes the local roster explicit.
func configureParticipants(cfg *config.File, participants int) {
	if cfg == nil || participants == 0 {
		return
	}

	hostCount := participants + 1 // versiond-0 and versiond-1 form the HA pair.
	if len(cfg.Hosts) > hostCount {
		cfg.Hosts = cfg.Hosts[:hostCount]
	}
	for len(cfg.Hosts) < hostCount {
		index := len(cfg.Hosts)
		cfg.Hosts = append(cfg.Hosts, config.HostCfg{ID: fmt.Sprintf("versiond-%d", index)})
	}

	for index := range cfg.Hosts {
		cfg.Hosts[index].KeyName = fmt.Sprintf("versiond-%d", index)
	}
	cfg.Hosts[1].KeyName = cfg.Hosts[0].KeyName
	cfg.Escrow.Slots = participants
}

func randomizeTestenv(cfg *config.File) error {
	ports := []*int{
		&cfg.MockChain.GRPCPort, &cfg.MockChain.RPCPort, &cfg.MockChain.TestenvPort,
		&cfg.MockDapi.GRPCPort, &cfg.MockDapi.HTTPPort, &cfg.Devshardctl.Port, &cfg.Devshardctl.StatsPort,
		&cfg.VersiondRouter.Port,
	}
	for _, port := range ports {
		value, err := freePort()
		if err != nil {
			return err
		}
		*port = value
	}
	segment := 20 + int(time.Now().UnixNano()%200)
	base := fmt.Sprintf("172.31.%d", segment)
	cfg.Network.BaseIP = base
	cfg.Network.Subnet = base + ".0/24"
	cfg.VersiondRouter.IP = ""
	cfg.Devshardctl.IP = ""
	cfg.Postgres.IP = ""
	for i := range cfg.Hosts {
		cfg.Hosts[i].IP = ""
	}
	cfg.ApplyDefaults()
	return nil
}

func freePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

func fixComposePaths(composePath, testenvDir string) error {
	repoRoot, err := filepath.Abs(filepath.Join(testenvDir, "..", ".."))
	if err != nil {
		return err
	}
	body, err := os.ReadFile(composePath)
	if err != nil {
		return err
	}
	text := string(body)
	for _, replacement := range [][2]string{
		{"context: ../../versiond-router", "context: " + filepath.Join(repoRoot, "versiond-router")},
		{"context: ../../versioned", "context: " + filepath.Join(repoRoot, "versioned")},
		{"context: ../..", "context: " + repoRoot},
		{"- ../../build/devshardd:", "- " + filepath.Join(repoRoot, "build", "devshardd") + ":"},
	} {
		text = strings.ReplaceAll(text, replacement[0], replacement[1])
	}
	text = stripStaticNetworking(text)
	return os.WriteFile(composePath, []byte(text), 0o644)
}

// The runner uses Compose DNS names, not the fixture's fixed container IPs.
// Let Docker choose a network subnet so parallel or interrupted runs cannot
// collide with an existing local route or Compose network.
func stripStaticNetworking(compose string) string {
	lines := strings.Split(compose, "\n")
	filtered := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if line == "    ipam:" && i+2 < len(lines) && lines[i+1] == "      config:" && strings.HasPrefix(lines[i+2], "        - subnet:") {
			i += 2
			continue
		}
		if strings.TrimSpace(line) != "" && strings.HasPrefix(strings.TrimSpace(line), "ipv4_address:") {
			continue
		}
		filtered = append(filtered, line)
	}
	return strings.Join(filtered, "\n")
}

func runCommand(ctx context.Context, dir, name string, args ...string) error {
	return runCommandWithEnv(ctx, dir, nil, name, args...)
}

func runCommandWithEnv(ctx context.Context, dir string, env []string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, output)
	}
	return nil
}

func dockerServerArch(ctx context.Context) (string, error) {
	output, err := exec.CommandContext(ctx, "docker", "version", "--format", "{{.Server.Arch}}").Output()
	if err != nil {
		return "", fmt.Errorf("read Docker server arch: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// loadTestComposeEnv selects the v6 peer-RPC transport for this isolated
// stack: HTTP/2 (the client default) on versiond-router:8080 with native
// gRPC. The normal testenv Compose defaults remain unchanged for non-loadtest
// testenv and citest runs.
func loadTestComposeEnv() []string {
	env := os.Environ()
	env = removeEnvValue(env, "DEVSHARD_RPC_H2_UPGRADE")
	env = setEnvValue(env, "DEVSHARD_RPC_GRPC", "true")
	return env
}

func setEnvValue(env []string, key, value string) []string {
	prefix := key + "="
	filtered := make([]string, 0, len(env)+1)
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			continue
		}
		filtered = append(filtered, item)
	}
	return append(filtered, prefix+value)
}

func removeEnvValue(env []string, key string) []string {
	prefix := key + "="
	filtered := make([]string, 0, len(env))
	for _, item := range env {
		if !strings.HasPrefix(item, prefix) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func waitGateway(ctx context.Context, gatewayURL string) error {
	deadline := time.NewTimer(5 * time.Minute)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	client := &http.Client{Timeout: 2 * time.Second}
	for {
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, gatewayURL+"/v1/status", nil)
		response, err := client.Do(request)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("gateway did not become ready within 5m")
		case <-ticker.C:
		}
	}
}

func fetchAllocations(ctx context.Context, dapiPort int) (map[string]uint64, error) {
	url := fmt.Sprintf("http://127.0.0.1:%d/testenv/ml-allocations", dapiPort)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("allocation endpoint returned %s", response.Status)
	}
	var allocations map[string]uint64
	if err := json.NewDecoder(response.Body).Decode(&allocations); err != nil {
		return nil, err
	}
	return allocations, nil
}

func fetchMLNodeStats(ctx context.Context, dapiPort int) (map[string]MLNodeStats, error) {
	url := fmt.Sprintf("http://127.0.0.1:%d/testenv/ml-stats", dapiPort)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ML stats endpoint returned %s", response.Status)
	}
	var stats map[string]MLNodeStats
	if err := json.NewDecoder(response.Body).Decode(&stats); err != nil {
		return nil, err
	}
	return stats, nil
}

func writeMLNodeStats(outputDir string, stats map[string]MLNodeStats) error {
	body, err := json.MarshalIndent(stats, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outputDir, "ml-stats.json"), append(body, '\n'), 0o644)
}

func fetchGatewayStateSizes(ctx context.Context, gatewayURL, apiKey string) (GatewayStateSizes, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, gatewayURL+"/v1/debug/state-sizes", nil)
	if err != nil {
		return GatewayStateSizes{}, err
	}
	if apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+apiKey)
	}
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		return GatewayStateSizes{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return GatewayStateSizes{}, fmt.Errorf("gateway state sizes endpoint returned %s", response.Status)
	}
	var sizes GatewayStateSizes
	if err := json.NewDecoder(response.Body).Decode(&sizes); err != nil {
		return GatewayStateSizes{}, err
	}
	return sizes, nil
}

// scopedGatewayURL selects a runtime in multi-DevShard mode for diagnostic
// endpoints. Client traffic intentionally keeps using the Gateway root URL,
// which performs model-based routing.
func scopedGatewayURL(gatewayURL, escrowID string) string {
	base := strings.TrimRight(gatewayURL, "/")
	id := strings.Trim(strings.TrimSpace(escrowID), "/")
	if id == "" {
		return base
	}
	return base + "/devshard/" + id
}

func writeGatewayStateSizes(outputDir string, sizes GatewayStateSizes) error {
	body, err := json.MarshalIndent(sizes, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outputDir, "gateway-state.json"), append(body, '\n'), 0o644)
}

// assertRun judges the request, mock ML, escrow rotation and drain checks from the run's summary
// and saved inputs.
func assertRun(scenario Scenario, summary Summary, inputs RunInputs, events []EventRecord) []AssertionResult {
	checks := make([]AssertionResult, 0, 5)
	addCheck := func(name, expected, actual, details string, passed bool) {
		checks = append(checks, AssertionResult{Name: name, Passed: passed, Expected: expected, Actual: actual, Details: details})
	}

	if summary.Requests == 0 {
		addCheck("load.requests_present", "> 0 requests", "0 requests", "the load generator did not produce any request results", false)
	} else {
		attempted := summary.Requests - summary.Dropped
		httpFailures := attempted - summary.Completed
		addCheck(
			"requests.error_rate",
			fmt.Sprintf("<= %.4f (%.2f%%)", scenario.Assertions.Requests.ErrorRate, scenario.Assertions.Requests.ErrorRate*100),
			fmt.Sprintf("%.4f (%.2f%%)", summary.ErrorRate, summary.ErrorRate*100),
			fmt.Sprintf("offered=%d attempted=%d completed=%d http_failures=%d dropped=%d", summary.Requests, attempted, summary.Completed, httpFailures, summary.Dropped),
			summary.ErrorRate <= scenario.Assertions.Requests.ErrorRate,
		)
		droppedBound := scenario.Assertions.Requests.ErrorRate
		if bound := scenario.Assertions.Requests.DroppedRate; bound != nil {
			droppedBound = *bound
		}
		addCheck(
			"requests.dropped_rate",
			fmt.Sprintf("<= %.4f (%.2f%%)", droppedBound, droppedBound*100),
			fmt.Sprintf("%.4f (%.2f%%)", summary.DroppedRate, summary.DroppedRate*100),
			fmt.Sprintf("offered=%d dropped=%d max_in_flight=%d", summary.Requests, summary.Dropped, scenario.Workload.MaxInFlight),
			summary.DroppedRate <= droppedBound,
		)
	}
	if scenario.Assertions.MockML.RequireEachNodeUsed {
		for _, node := range scenario.Topology.MockML.Nodes {
			addCheck(
				"mock_ml.node_used."+node.Name,
				"allocations > 0",
				fmt.Sprintf("allocations=%d", inputs.Allocations[node.Name]),
				fmt.Sprintf("configured node profile=%s", node.Profile),
				inputs.Allocations[node.Name] > 0,
			)
		}
	}
	if scenario.Assertions.EscrowRotation.RequireNewEscrow {
		initial := map[string]struct{}{}
		for _, id := range inputs.InitialEscrowIDs {
			initial[id] = struct{}{}
		}
		newEscrowIDs := difference(inputs.CurrentEscrowIDs, initial)
		check := AssertionResult{
			Name:     "gateway.escrow_rotation.new_escrow",
			Expected: "new escrow count >= 1",
			Actual:   fmt.Sprintf("new=%d ids=%s current=%s", len(newEscrowIDs), formatEscrowIDs(newEscrowIDs), formatEscrowIDs(inputs.CurrentEscrowIDs)),
			Passed:   inputs.CurrentEscrowError == "" && len(newEscrowIDs) > 0,
		}
		if inputs.CurrentEscrowError != "" {
			check.Details = inputs.CurrentEscrowError
		} else {
			check.Details = fmt.Sprintf("initial=%s", formatEscrowIDsFromSet(initial))
		}
		checks = append(checks, check)
	}
	if scenario.Assertions.Devshard.NoOrphanedWork {
		check := AssertionResult{
			Name:     "devshard.no_orphaned_work",
			Expected: fmt.Sprintf("orphaned=0; ghost rate <= %.2f%%, fault-caused burns inside the host's fault windows left out", scenario.Assertions.Devshard.MaxGhostRate*100),
			Actual:   "unavailable",
			Details:  "the drain check's last read " + notSaved,
		}
		if inputs.Orphans != nil {
			terminal, done, verdict := judgeOrphans(*inputs.Orphans, orphanJudgeFor(scenario, inputs, events))
			if !done && verdict == nil {
				verdict = errors.New("the drain check ended before a verdict")
			}
			check.Passed, check.Actual, check.Details = verdict == nil, formatTerminalSummary(terminal), ""
			if verdict != nil {
				check.Details = verdict.Error()
			}
		}
		checks = append(checks, check)
	}
	return checks
}

// orphanJudgeFor builds the drain check's judge; the live poll and the saved-run evaluation share it.
func orphanJudgeFor(scenario Scenario, inputs RunInputs, events []EventRecord) orphanJudge {
	settings := resolveInvariantSettings(scenario)
	judge := orphanJudge{MaxGhostRate: scenario.Assertions.Devshard.MaxGhostRate, Timeout: scenario.DrainDuration()}
	if scenario.Assertions.Invariants.Enabled {
		judge.MaxPendingAge = settings.MaxPendingAge
	}
	nodeHosts := map[string]string{}
	for _, node := range scenario.Topology.MockML.Nodes {
		nodeHosts[node.Name] = node.Host
	}
	judge.Ghosts = newGhostSet(inputs.Burns, burnExcuse(InvariantInputs{Started: inputs.Started, Workload: scenario.Duration(), Settings: settings, Hosts: inputs.Hosts, NodeHosts: nodeHosts, Events: events}))
	judge.SettlementOff = func(model string) bool { return settlementDisabledModels(scenario, []string{model})[model] }
	if ledger, _, _ := spendSources(inputs); ledger != nil {
		judge.Escrows = escrowsByID(ledger.Escrows)
	}
	return judge
}

func escrowsByID(escrows []ledgerface.EscrowView) map[string]ledgerface.EscrowView {
	byID := make(map[string]ledgerface.EscrowView, len(escrows))
	for _, escrow := range escrows {
		byID[strconv.FormatUint(escrow.ID, 10)] = escrow
	}
	return byID
}

func formatTerminalSummary(summary TerminalSummary) string {
	statusCounts := formatStatusCounts(summary.Statuses)
	if summary.Total == 0 && summary.Statuses != nil {
		statusCounts = "none"
	}
	atSeal := strconv.Itoa(summary.ChallengedAtSeal)
	if len(summary.ChallengedAtSealByEscrow) > 0 {
		atSeal += " (escrow " + formatEscrowCounts(summary.ChallengedAtSealByEscrow) + ")"
	}
	return fmt.Sprintf("finished=%d orphaned=%d sealed=%d challenged_at_seal=%s in_flight=%d ghost=%d ghost_excluded=%d total=%d ghost_rate=%.2f%% statuses=%s", summary.Finished, summary.Orphaned, summary.Sealed, atSeal, summary.InFlight, summary.Ghost, summary.GhostExcluded, summary.Total, summary.GhostRate*100, statusCounts)
}

type debugInference struct {
	Status    string `json:"status"`
	StartedAt int64  `json:"started_at"`
}

type debugInferencesResponse struct {
	Inferences json.RawMessage `json:"inferences"`
}

// decodeDebugInferences reads the drain check's view of either driver's debug inference records.
func decodeDebugInferences(raw json.RawMessage) (map[string]debugInference, error) {
	records, err := decodeInferenceRecords(raw)
	if err != nil {
		return nil, err
	}
	inferences := make(map[string]debugInference, len(records))
	for nonce, record := range records {
		inferences[nonce] = debugInference{Status: record.Status, StartedAt: record.StartedAt}
	}
	return inferences, nil
}

// orphanJudge is what devshard.no_orphaned_work judges a debug read by.
type orphanJudge struct {
	MaxGhostRate  float64
	MaxPendingAge time.Duration // above zero, unfinished records younger than it stay in flight
	Timeout       time.Duration
	Ghosts        ghostSet
	// Escrows is the mock chain's view of each escrow by ID; SettlementOff reports a model whose escrows park on deactivation.
	Escrows       map[string]ledgerface.EscrowView
	SettlementOff func(model string) bool
	// ChainURL, when set, is the mock chain the live poll reads Escrows from before each judgement.
	ChainURL string
}

// escrowState is how far a runtime's escrow is from final for the orphan check.
type escrowState int

const (
	escrowActive escrowState = iota
	// escrowDeactivated is due a settlement the chain has not recorded, or its chain state is unknown.
	escrowDeactivated
	// escrowSealed is settled on chain or parked with settlement off: no diff reaches it again.
	escrowSealed
)

// stateOf places a runtime's escrow for the orphan check. An escrow the chain read does not show stays
// escrowDeactivated, so its challenged records keep being judged by age.
func (j orphanJudge) stateOf(runtime orphanRuntime) escrowState {
	if runtime.Active {
		return escrowActive
	}
	escrow, known := j.Escrows[runtime.EscrowID]
	if known && (escrow.Settled || j.SettlementOff != nil && j.SettlementOff(escrow.ModelID)) {
		return escrowSealed
	}
	return escrowDeactivated
}

// orphanRuntime is one runtime's /v1/debug/inferences; EscrowID is empty for a runtime read
// without its escrow.
type orphanRuntime struct {
	EscrowID   string                    `json:"escrow_id,omitempty"`
	Active     bool                      `json:"active"`
	Inferences map[string]debugInference `json:"inferences"`
}

// orphanSnapshot is one successful read of every runtime by the drain check.
type orphanSnapshot struct {
	At       time.Time       `json:"at"`
	Runtimes []orphanRuntime `json:"runtimes"`
}

// orphanPoll is how the drain check ended: its last successful read, the error of a later failed
// read, and whether the deadline ended it. judgeOrphans gives the same verdict on a saved poll.
type orphanPoll struct {
	Last      *orphanSnapshot `json:"last,omitempty"`
	ReadError string          `json:"read_error,omitempty"`
	TimedOut  bool            `json:"timed_out"`
	Cancelled string          `json:"cancelled,omitempty"`
}

// waitForNoOrphanedWork polls until judgeOrphans reaches a verdict or the deadline passes.
func waitForNoOrphanedWork(ctx context.Context, gatewayURL, apiKey string, judge orphanJudge, aggregateRuntimeState ...bool) (orphanPoll, TerminalSummary, error) {
	log.Printf("loadtest: stage=devshard_orphan_check_start timeout=%s max_ghost_rate=%.2f%%", judge.Timeout, judge.MaxGhostRate*100)
	aggregate := len(aggregateRuntimeState) > 0 && aggregateRuntimeState[0]
	deadline := time.NewTimer(judge.Timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	client := &http.Client{Timeout: 5 * time.Second}
	var poll orphanPoll
	for {
		if judge.ChainURL != "" {
			var escrows struct {
				Escrows []ledgerface.EscrowView `json:"escrows"`
			}
			if err := getJSON(ctx, judge.ChainURL+"/testenv/escrows", "", &escrows); err == nil {
				judge.Escrows = escrowsByID(escrows.Escrows)
			}
		}
		snapshot, err := fetchOrphanSnapshot(ctx, client, gatewayURL, apiKey, aggregate)
		if err == nil {
			poll.Last, poll.ReadError = &snapshot, ""
		} else {
			poll.ReadError = err.Error()
		}
		if state, done, verdict := judgeOrphans(poll, judge); done {
			if verdict == nil {
				log.Printf("loadtest: orphan check passed %s", formatTerminalSummary(state))
			}
			return poll, state, verdict
		}
		select {
		case <-ctx.Done():
			poll.Cancelled = ctx.Err().Error()
			state, _, verdict := judgeOrphans(poll, judge)
			return poll, state, verdict
		case <-deadline.C:
			poll.TimedOut = true
			state, _, verdict := judgeOrphans(poll, judge)
			if verdict == nil {
				log.Printf("loadtest: orphan check passed with records still within %s: %s", judge.MaxPendingAge, formatTerminalSummary(state))
			}
			return poll, state, verdict
		case <-ticker.C:
		}
	}
}

// judgeOrphans returns the summary of the poll's last read and, once done, the verdict: a ghost
// rate above the limit fails, no orphaned and no in-flight record passes, and at the deadline no
// orphaned record passes.
func judgeOrphans(poll orphanPoll, judge orphanJudge) (TerminalSummary, bool, error) {
	if poll.Last == nil {
		switch {
		case poll.Cancelled != "":
			return TerminalSummary{}, true, errors.New(poll.Cancelled)
		case poll.TimedOut:
			readErr := poll.ReadError
			if readErr == "" {
				readErr = "no successful response from debug endpoint"
			}
			return TerminalSummary{}, true, fmt.Errorf("cannot inspect DevShard inference state within %s: %s", judge.Timeout, readErr)
		}
		return TerminalSummary{}, false, nil
	}
	state := TerminalSummary{Statuses: make(map[string]int)}
	for _, runtime := range poll.Last.Runtimes {
		summary := summarizeDebugInferences(runtime.Inferences, judge.Ghosts.escrow(runtime.EscrowID), judge.stateOf(runtime), poll.Last.At, judge.MaxPendingAge)
		if summary.ChallengedAtSeal > 0 {
			if state.ChallengedAtSealByEscrow == nil {
				state.ChallengedAtSealByEscrow = map[string]int{}
			}
			state.ChallengedAtSealByEscrow[runtime.EscrowID] += summary.ChallengedAtSeal
		}
		mergeTerminalSummary(&state, summary)
	}
	if state.Total > 0 {
		state.GhostRate = float64(state.Ghost) / float64(state.Total)
	}
	if poll.ReadError == "" && state.GhostRate > judge.MaxGhostRate {
		return state, true, fmt.Errorf("ghost inference rate %.4f exceeds max_ghost_rate %.4f (ghost=%d total=%d ghost_excluded=%d)", state.GhostRate, judge.MaxGhostRate, state.Ghost, state.Total, state.GhostExcluded)
	}
	if poll.ReadError == "" && state.Orphaned == 0 && (state.InFlight == 0 || poll.TimedOut) {
		return state, true, nil
	}
	if poll.Cancelled != "" {
		return state, true, errors.New(poll.Cancelled)
	}
	if !poll.TimedOut {
		return state, false, nil
	}
	details := fmt.Sprintf("orphaned=%d statuses=%s ghost=%d total=%d ghost_rate=%.4f", state.Orphaned, formatStatusCounts(state.Statuses), state.Ghost, state.Total, state.GhostRate)
	if poll.ReadError != "" {
		details += "; last debug read error: " + poll.ReadError
	}
	return state, true, fmt.Errorf("DevShard still has orphaned inference work after %s (%s)", judge.Timeout, details)
}

func fetchOrphanSnapshot(ctx context.Context, client *http.Client, gatewayURL, apiKey string, aggregate bool) (orphanSnapshot, error) {
	runtimes := []debugRuntime{{url: gatewayURL, active: true}}
	if aggregate {
		var err error
		runtimes, err = fetchDebugRuntimes(ctx, gatewayURL, apiKey)
		if err != nil {
			return orphanSnapshot{}, err
		}
	}
	snapshot := orphanSnapshot{At: time.Now().UTC()}
	for _, runtime := range runtimes {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, runtime.url+"/v1/debug/inferences", nil)
		if err != nil {
			return orphanSnapshot{}, err
		}
		if apiKey != "" {
			request.Header.Set("Authorization", "Bearer "+apiKey)
		}
		response, err := client.Do(request)
		if err != nil {
			return orphanSnapshot{}, fmt.Errorf("GET /v1/debug/inferences: %w", err)
		}
		var body debugInferencesResponse
		decodeErr := json.NewDecoder(response.Body).Decode(&body)
		response.Body.Close()
		var inferences map[string]debugInference
		if decodeErr == nil {
			inferences, decodeErr = decodeDebugInferences(body.Inferences)
		}
		if response.StatusCode != http.StatusOK {
			if decodeErr == nil {
				return orphanSnapshot{}, fmt.Errorf("GET /v1/debug/inferences returned %s", response.Status)
			}
			return orphanSnapshot{}, fmt.Errorf("GET /v1/debug/inferences returned %s (read body: %w)", response.Status, decodeErr)
		}
		if decodeErr != nil {
			return orphanSnapshot{}, fmt.Errorf("decode /v1/debug/inferences: %w", decodeErr)
		}
		snapshot.Runtimes = append(snapshot.Runtimes, orphanRuntime{EscrowID: runtime.escrowID, Active: runtime.active, Inferences: inferences})
	}
	return snapshot, nil
}

type adminDevshardsResponse struct {
	Devshards []struct {
		ID string `json:"id"`
	} `json:"devshards"`
}

func configuredEscrowIDs(cfg *config.File) map[string]struct{} {
	ids := make(map[string]struct{})
	if cfg == nil {
		return ids
	}
	for _, escrow := range cfg.Escrows {
		if escrow.ID == 0 {
			continue
		}
		ids[strconv.FormatUint(escrow.ID, 10)] = struct{}{}
	}
	return ids
}

func fetchGatewayDevshardIDs(ctx context.Context, gatewayURL, apiKey string) ([]string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, gatewayURL+"/v1/admin/devshards", nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+apiKey)
	}
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		return nil, fmt.Errorf("GET /v1/admin/devshards: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4<<10))
		return nil, fmt.Errorf("GET /v1/admin/devshards returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	var body adminDevshardsResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode /v1/admin/devshards: %w", err)
	}
	ids := make([]string, 0, len(body.Devshards))
	seen := make(map[string]struct{}, len(body.Devshards))
	for _, devshard := range body.Devshards {
		id := strings.TrimSpace(devshard.ID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		return nil, fmt.Errorf("GET /v1/admin/devshards returned no escrow IDs")
	}
	return ids, nil
}

type debugRuntime struct {
	url      string
	escrowID string
	active   bool
}

func fetchDebugRuntimes(ctx context.Context, gatewayURL, apiKey string) ([]debugRuntime, error) {
	devshards, err := fetchGatewayDevshards(ctx, gatewayURL, apiKey)
	if err != nil {
		return nil, fmt.Errorf("GET /v1/admin/devshards: %w", err)
	}
	if len(devshards) == 0 {
		return nil, fmt.Errorf("GET /v1/admin/devshards returned no escrow IDs")
	}
	runtimes := make([]debugRuntime, 0, len(devshards))
	for _, devshard := range devshards {
		runtimes = append(runtimes, debugRuntime{url: scopedGatewayURL(gatewayURL, devshard.ID), escrowID: devshard.ID, active: devshard.Active})
	}
	return runtimes, nil
}

func difference(values []string, excluded map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := excluded[value]; !ok {
			result = append(result, value)
		}
	}
	return result
}

func formatEscrowIDs(ids []string) string {
	if len(ids) == 0 {
		return "none"
	}
	return strings.Join(ids, ",")
}

func sortedKeySet(ids map[string]struct{}) []string {
	values := make([]string, 0, len(ids))
	for id := range ids {
		values = append(values, id)
	}
	sort.Strings(values)
	return values
}

func formatEscrowIDsFromSet(ids map[string]struct{}) string {
	values := make([]string, 0, len(ids))
	for id := range ids {
		values = append(values, id)
	}
	sort.Strings(values)
	return formatEscrowIDs(values)
}

func mergeTerminalSummary(dst *TerminalSummary, src TerminalSummary) {
	dst.Finished += src.Finished
	dst.Ghost += src.Ghost
	dst.GhostExcluded += src.GhostExcluded
	dst.Total += src.Total
	dst.Orphaned += src.Orphaned
	dst.Sealed += src.Sealed
	dst.InFlight += src.InFlight
	dst.ChallengedAtSeal += src.ChallengedAtSeal
	for status, count := range src.Statuses {
		dst.Statuses[status] += count
	}
}

func summarizeDebugInferences(inferences map[string]debugInference, ghosts map[string]bool, state escrowState, now time.Time, maxPendingAge time.Duration) TerminalSummary {
	summary := TerminalSummary{Statuses: make(map[string]int)}
	for id, inference := range inferences {
		if excluded, ghost := ghosts[id]; ghost {
			if excluded {
				summary.GhostExcluded++
			} else {
				summary.Ghost++
				summary.Total++
			}
			continue
		}
		summary.Total++
		summary.Statuses[inference.Status]++
		if inference.Status == "finished" {
			summary.Finished++
		}
		switch {
		case isTerminalInferenceStatus(inference.Status):
		// Settlement seals a challenged record as it stands and pays its executor; a parked escrow gets no further diff.
		case state == escrowSealed:
			summary.Sealed++
			if inference.Status == "challenged" {
				summary.ChallengedAtSeal++
			}
		// Until the escrow settles, Finalize still folds the hosts' challenge votes in, so a challenged record is judged by age.
		case state == escrowDeactivated && (inference.Status != "challenged" || maxPendingAge == 0):
			summary.Sealed++
		case maxPendingAge > 0 && inference.StartedAt > 0 && now.Sub(time.Unix(inference.StartedAt, 0)) <= maxPendingAge:
			summary.InFlight++
		default:
			summary.Orphaned++
		}
	}
	if summary.Total > 0 {
		summary.GhostRate = float64(summary.Ghost) / float64(summary.Total)
	}
	return summary
}

func isTerminalInferenceStatus(status string) bool {
	switch status {
	case "finished", "validated", "invalidated", "timed_out":
		return true
	default:
		return false
	}
}

// formatEscrowCounts lists per-escrow counts as "id:count" in escrow ID order.
func formatEscrowCounts(counts map[string]int) string {
	ids := make([]string, 0, len(counts))
	for id := range counts {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		x, _ := strconv.ParseUint(ids[i], 10, 64)
		y, _ := strconv.ParseUint(ids[j], 10, 64)
		return x < y || x == y && ids[i] < ids[j]
	})
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, fmt.Sprintf("%s:%d", id, counts[id]))
	}
	return strings.Join(parts, " ")
}

func formatStatusCounts(counts map[string]int) string {
	if len(counts) == 0 {
		return "unavailable"
	}
	statuses := make([]string, 0, len(counts))
	for status := range counts {
		statuses = append(statuses, status)
	}
	sort.Strings(statuses)
	parts := make([]string, 0, len(statuses))
	for _, status := range statuses {
		parts = append(parts, fmt.Sprintf("%s=%d", status, counts[status]))
	}
	return strings.Join(parts, ", ")
}

func writeGatewayInferences(ctx context.Context, gatewayURL, apiKey, outputDir string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, gatewayURL+"/v1/debug/inferences", nil)
	if err != nil {
		return err
	}
	if apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+apiKey)
	}
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("debug inferences endpoint returned %s", response.Status)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outputDir, "gateway-inferences.json"), append(body, '\n'), 0o644)
}

func writeComposeLogs(outputDir, testenvDir, project, composePath, tail string) error {
	return writeComposeLogsTo(outputDir, testenvDir, project, composePath, "compose.log", tail)
}

func writeComposeLogsTo(outputDir, testenvDir, project, composePath, filename, tail string) error {
	file, err := os.Create(filepath.Join(outputDir, filename))
	if err != nil {
		return err
	}
	// Stream complete logs to disk instead of buffering them in memory.
	cmd := exec.Command("docker", "compose", "-p", project, "-f", composePath, "logs", "--no-color", "--tail", tail)
	cmd.Dir = testenvDir
	cmd.Stdout = file
	cmd.Stderr = file
	runErr := cmd.Run()
	closeErr := file.Close()
	if runErr != nil {
		return runErr
	}
	return closeErr
}

func envValue(path, key string) (string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	prefix := key + "="
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix), nil
		}
	}
	return "", fmt.Errorf("%s missing from %s", key, path)
}

func copyFile(source, destination string) error {
	body, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return os.WriteFile(destination, body, 0o644)
}

func isAddressInUse(err error) bool {
	return err != nil && strings.Contains(err.Error(), "Address already in use")
}

func writeAssertions(outputDir string, checks []AssertionResult, assertionErr error) error {
	report := map[string]any{"passed": assertionErr == nil, "checks": checks}
	if assertionErr != nil {
		report["error"] = assertionErr.Error()
		var failures *assertionFailures
		if errors.As(assertionErr, &failures) {
			names := make([]string, 0, len(failures.Checks))
			for _, check := range failures.Checks {
				names = append(names, check.Name)
			}
			report["failed_assertions"] = names
		}
	}
	body, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outputDir, "assertions.json"), append(body, '\n'), 0o644)
}

func failedChecks(checks []AssertionResult) []AssertionResult {
	failures := []AssertionResult{}
	for _, check := range checks {
		if !check.Passed {
			failures = append(failures, check)
		}
	}
	return failures
}

func eventAssertions(records []EventRecord) []AssertionResult {
	checks := make([]AssertionResult, 0, len(records))
	for _, record := range records {
		actual := "started=<none> ended=<none>"
		if record.StartedAt != nil {
			actual = "started=" + record.StartedAt.Format(time.RFC3339Nano) + " ended=<none>"
			if record.EndedAt != nil {
				actual = "started=" + record.StartedAt.Format(time.RFC3339Nano) + " ended=" + record.EndedAt.Format(time.RFC3339Nano)
			}
		}
		checks = append(checks, AssertionResult{
			Name:     fmt.Sprintf("events.%s.%s.%s.applied", record.ID, record.Action, record.Target),
			Passed:   record.Error == "" && record.StartedAt != nil && record.EndedAt != nil,
			Expected: "event applied and reverted",
			Actual:   actual,
			Details:  record.Error,
		})
	}
	return checks
}

// markKnownBugs turns failures of known_bug cases into passing XFAIL checks.
// A passing known_bug check is XPASS: logged loudly, and a failure if strict.
func markKnownBugs(checks []AssertionResult, cases []Case, strict bool) []AssertionResult {
	knownBugs := map[string]string{}
	for _, c := range cases {
		if c.KnownBug != "" {
			knownBugs[c.ID] = c.KnownBug
		}
	}
	marked := make([]AssertionResult, len(checks))
	for i, check := range checks {
		bug, known := knownBugs[check.Case]
		if known && check.Status == "" {
			if check.Passed {
				check.Status = "XPASS"
				check.Passed = !strict
				check.Details = strings.TrimSuffix("known_bug "+bug+"; "+check.Details, "; ")
				log.Printf("loadtest: XPASS %s: case %s passed although known_bug %s is set: the bug did not reproduce in this run", check.Name, check.Case, bug)
			} else {
				check.Status = "XFAIL"
				check.Passed = true
				check.Details = strings.TrimSpace("known_bug " + bug + "; " + check.Details)
			}
		}
		marked[i] = check
	}
	return marked
}

// refuseConcurrentStacks stops a run while another load-test stack is up: two
// stacks do not fit the Docker VM memory and skew each other's measurements.
func refuseConcurrentStacks(ctx context.Context, list func(context.Context) ([]string, error)) error {
	projects, err := list(ctx)
	if err != nil {
		return fmt.Errorf("list running Compose projects: %w", err)
	}
	var running []string
	for _, project := range projects {
		if strings.HasPrefix(project, "loadtest-") {
			running = append(running, project)
		}
	}
	if len(running) > 0 {
		return fmt.Errorf("another load-test stack is running (%s); stop it with docker compose -p <project> down --volumes", strings.Join(running, ", "))
	}
	return nil
}

func listComposeProjects(ctx context.Context) ([]string, error) {
	output, err := exec.CommandContext(ctx, "docker", "compose", "ls", "--format", "json").Output()
	if err != nil {
		return nil, err
	}
	return parseComposeProjects(output)
}

func parseComposeProjects(output []byte) ([]string, error) {
	var projects []struct {
		Name string `json:"Name"`
	}
	if err := json.Unmarshal(output, &projects); err != nil {
		return nil, fmt.Errorf("decode docker compose ls: %w", err)
	}
	names := make([]string, 0, len(projects))
	for _, project := range projects {
		names = append(names, project.Name)
	}
	return names, nil
}

// checkChildBinaryArch fails when the mounted devshardd cannot run in the
// natively built versiond image: versiond then never starts a child.
func checkChildBinaryArch(path, dockerArch string) error {
	file, err := elf.Open(path)
	if err != nil {
		return fmt.Errorf("read devshardd %s: %w", path, err)
	}
	defer file.Close()
	arch := map[elf.Machine]string{elf.EM_X86_64: "amd64", elf.EM_AARCH64: "arm64"}[file.Machine]
	if arch != dockerArch {
		return fmt.Errorf("devshardd %s is %s (%s) but Docker runs %s; rebuild with make build-devshardd DOCKER_PLATFORM=linux/%s", path, arch, file.Machine, dockerArch, dockerArch)
	}
	return nil
}
