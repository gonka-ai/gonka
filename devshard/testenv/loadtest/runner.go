package loadtest

import (
	"context"
	"encoding/json"
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
}

type RunResult struct {
	Summary      Summary
	Terminal     TerminalSummary
	Assertions   []AssertionResult
	OutputDir    string
	WorkDir      string
	GatewayURL   string
	Allocations  map[string]uint64
	MLStats      map[string]MLNodeStats
	GatewayState GatewayStateSizes
	Metrics      MetricsSummary
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
	message := fmt.Sprintf("assertion %s failed: expected %s; actual %s", check.Name, check.Expected, check.Actual)
	if check.Details != "" {
		message += "; details: " + check.Details
	}
	return message
}

// TerminalSummary describes all DevShard inference records observed during the
// orphan check. It can differ from the number of client requests because the
// gateway may serve cache hits or create speculative attempts.
type TerminalSummary struct {
	Finished  int            `json:"finished"`
	Ghost     int            `json:"ghost"`
	Total     int            `json:"total"`
	Orphaned  int            `json:"orphaned"`
	GhostRate float64        `json:"ghost_rate"`
	Statuses  map[string]int `json:"statuses,omitempty"`
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
	defer func() {
		if opts.KeepStack || project == "" {
			return
		}
		_ = runCommand(context.Background(), opts.TestenvDir, "docker", "compose", "-p", project, "-f", composePath, "down", "--volumes", "--remove-orphans")
	}()

	const maxStartAttempts = 3
	log.Printf("loadtest: stage=stack_start scenario=%s", scenario.Scenario)
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
		err := runCommand(ctx, opts.TestenvDir, "docker", "compose", "-p", project, "-f", composePath, "up", "--build", "--wait", "--detach")
		if err == nil {
			break
		}
		log.Printf("loadtest: Docker stack start failed: %v", err)
		_ = writeComposeLogs(opts.OutputDir, opts.TestenvDir, project, composePath)
		_ = runCommand(context.Background(), opts.TestenvDir, "docker", "compose", "-p", project, "-f", composePath, "down", "--volumes", "--remove-orphans")
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
	stateGatewayURL := result.GatewayURL
	if replaySelector != nil {
		// Replay stacks configure Gateway through DEVSHARDS_JSON, so even a
		// single dataset model uses the multi-DevShard debug route.
		stateGatewayURL = scopedGatewayURL(result.GatewayURL, config.PrimaryEscrowID(cfg))
	}
	debugGatewayURL := stateGatewayURL
	if scenario.Gateway.EscrowRotation.Enabled {
		// Rotation adds runtimes while the test is running. The orphan check
		// discovers and inspects all of them through the Gateway root.
		debugGatewayURL = result.GatewayURL
	}
	log.Printf("loadtest: waiting for gateway at %s", result.GatewayURL)
	if err := waitGateway(ctx, result.GatewayURL); err != nil {
		_ = writeComposeLogs(opts.OutputDir, opts.TestenvDir, project, composePath)
		return RunResult{}, err
	}
	rotationModels := rotationModelIDs(cfg, replayModels, scenario)
	if err := applyGatewayScenarioSettings(ctx, result.GatewayURL, apiKey, scenario, rotationModels); err != nil {
		_ = writeComposeLogs(opts.OutputDir, opts.TestenvDir, project, composePath)
		return RunResult{}, err
	}
	log.Printf("loadtest: stage=gateway_ready secondary_wait_after_winner=%s", scenario.SecondaryWaitAfterWinner())
	collector, err := startMetricsCollector(ctx, opts, cfg, project, composePath, result.GatewayURL, apiKey)
	if err != nil {
		return result, fmt.Errorf("start metrics collector: %w", err)
	}
	defer func() { result.Metrics = collector.stop() }()
	collector.phase.Store("workload")

	log.Printf("loadtest: stage=workload_start scenario=%s", scenario.Scenario)
	summary, err := RunGenerator(ctx, GeneratorConfig{
		GatewayURL: result.GatewayURL,
		APIKey:     apiKey,
		Scenario:   scenario,
		OutputDir:  opts.OutputDir,
		Replay:     replaySelector,
	})
	if err != nil {
		_ = writeComposeLogs(opts.OutputDir, opts.TestenvDir, project, composePath)
		return RunResult{}, err
	}
	result.Summary = summary
	collector.phase.Store("drain")
	allocations, err := fetchAllocations(ctx, cfg.MockDapi.HTTPPort)
	if err != nil {
		_ = writeComposeLogs(opts.OutputDir, opts.TestenvDir, project, composePath)
		return RunResult{}, err
	}
	result.Allocations = allocations
	if err := writeComposeLogsTo(opts.OutputDir, opts.TestenvDir, project, composePath, "compose-pre-drain.log"); err != nil {
		return RunResult{}, err
	}
	ghostIDs, err := readGhostInferenceIDs(filepath.Join(opts.OutputDir, "compose-pre-drain.log"))
	if err != nil {
		return RunResult{}, err
	}
	terminal, assertions, assertionErr := assertRun(ctx, scenario, summary, allocations, debugGatewayURL, apiKey, initialEscrowIDs, ghostIDs)
	result.Terminal = terminal
	result.Assertions = assertions
	log.Printf("loadtest: stage=artifacts_collection")
	if err := writeComposeLogs(opts.OutputDir, opts.TestenvDir, project, composePath); err != nil {
		if assertionErr == nil {
			return RunResult{}, err
		}
		log.Printf("loadtest: final Compose log collection failed: %v", err)
	}
	gatewayState, stateErr := fetchGatewayStateSizes(ctx, stateGatewayURL, apiKey)
	if stateErr == nil {
		result.GatewayState = gatewayState
		if err := writeGatewayStateSizes(opts.OutputDir, gatewayState); err != nil && assertionErr == nil {
			return RunResult{}, err
		}
	} else if assertionErr == nil {
		return RunResult{}, stateErr
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
	if scenario.Gateway.EscrowRotation.Enabled {
		rotation := scenario.Gateway.EscrowRotation
		models := make([]map[string]any, 0, len(rotationModels))
		for _, modelID := range rotationModels {
			models = append(models, map[string]any{
				"model_id":        modelID,
				"temp_count":      rotation.TempCount,
				"target_count":    rotation.TargetCount,
				"amount":          rotation.Amount,
				"private_key_env": rotation.PrivateKeyEnv,
			})
		}
		if len(models) == 0 {
			return fmt.Errorf("gateway.escrow_rotation is enabled but no model IDs were found")
		}
		settings["escrow_rotation"] = map[string]any{
			"enabled":                  true,
			"settlement_enabled":       rotation.SettlementEnabled,
			"pre_poc_blocks":           rotation.PrePoCBlocks,
			"nonce_deactivation_limit": rotation.NonceDeactivationLimit,
			"models":                   models,
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
	add(scenario.Workload.Request.Model)
	return models
}

func writeRunnerConfig(testenvDir, workDir string, scenario Scenario, profiles map[string]Profile, replayEnabled bool, replayModels []string) error {
	cfg, err := config.Load(filepath.Join(testenvDir, "config", "config.yaml"))
	if err != nil {
		return err
	}
	cfg.Versiond.Mode = scenario.Topology.VersiondMode
	cfg.Postgres.PerParticipant = scenario.Topology.Storage == "per_participant"
	configureParticipants(cfg, scenario.Topology.Participants)
	cfg.Params.MaxNonce = scenario.Topology.Chain.MaxNonce
	if replayEnabled {
		if err := configureReplayModels(cfg, replayModels); err != nil {
			return err
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
	for _, node := range scenario.Topology.MockML.Nodes {
		profile := profiles[node.Profile]
		cfg.MockOpenAI.Nodes = append(cfg.MockOpenAI.Nodes, config.MockOpenAINodeCfg{
			Name:          node.Name,
			TTFT:          profile.TTFT,
			TokenInterval: profile.TokenInterval,
			Workers:       profile.Workers,
			Queue:         profile.Queue,
			Hang:          profile.Hang,
			FailureRate:   profile.FailureRate,
			HTTPStatus:    profile.HTTPStatus,
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
		&cfg.MockDapi.GRPCPort, &cfg.MockDapi.HTTPPort, &cfg.Devshardctl.Port,
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
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, output)
	}
	return nil
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

func assertRun(ctx context.Context, scenario Scenario, summary Summary, allocations map[string]uint64, gatewayURL, apiKey string, initialEscrowIDs, ghostIDs map[string]struct{}) (TerminalSummary, []AssertionResult, error) {
	checks := make([]AssertionResult, 0, 5)
	failures := make([]AssertionResult, 0)
	addCheck := func(name, expected, actual, details string, passed bool) {
		check := AssertionResult{Name: name, Passed: passed, Expected: expected, Actual: actual, Details: details}
		checks = append(checks, check)
		if !passed {
			failures = append(failures, check)
		}
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
	}
	if scenario.Assertions.MockML.RequireEachNodeUsed {
		for _, node := range scenario.Topology.MockML.Nodes {
			addCheck(
				"mock_ml.node_used."+node.Name,
				"allocations > 0",
				fmt.Sprintf("allocations=%d", allocations[node.Name]),
				fmt.Sprintf("configured node profile=%s", node.Profile),
				allocations[node.Name] > 0,
			)
		}
	}
	if scenario.Assertions.EscrowRotation.RequireNewEscrow {
		currentEscrowIDs, escrowErr := fetchGatewayDevshardIDs(ctx, gatewayURL, apiKey)
		newEscrowIDs := difference(currentEscrowIDs, initialEscrowIDs)
		check := AssertionResult{
			Name:     "gateway.escrow_rotation.new_escrow",
			Expected: "new escrow count >= 1",
			Actual:   fmt.Sprintf("new=%d ids=%s current=%s", len(newEscrowIDs), formatEscrowIDs(newEscrowIDs), formatEscrowIDs(currentEscrowIDs)),
			Passed:   escrowErr == nil && len(newEscrowIDs) > 0,
		}
		if escrowErr != nil {
			check.Details = escrowErr.Error()
		} else {
			check.Details = fmt.Sprintf("initial=%s", formatEscrowIDsFromSet(initialEscrowIDs))
		}
		checks = append(checks, check)
		if !check.Passed {
			failures = append(failures, check)
		}
	}
	terminal := TerminalSummary{}
	if scenario.Assertions.Devshard.NoOrphanedWork {
		var orphanErr error
		terminal, orphanErr = waitForNoOrphanedWork(ctx, gatewayURL, apiKey, scenario.Assertions.Devshard.MaxGhostRate, ghostIDs, scenario.DrainDuration(), scenario.Gateway.EscrowRotation.Enabled)
		check := AssertionResult{
			Name:     "devshard.no_orphaned_work",
			Passed:   orphanErr == nil,
			Expected: fmt.Sprintf("orphaned=0; ghost rate <= %.2f%%", scenario.Assertions.Devshard.MaxGhostRate*100),
			Actual:   formatTerminalSummary(terminal),
		}
		if orphanErr != nil {
			check.Details = orphanErr.Error()
		}
		checks = append(checks, check)
		if orphanErr != nil {
			failures = append(failures, check)
		}
	}
	if len(failures) > 0 {
		return terminal, checks, &assertionFailures{Checks: failures}
	}
	return terminal, checks, nil
}

func formatTerminalSummary(summary TerminalSummary) string {
	statusCounts := formatStatusCounts(summary.Statuses)
	if summary.Total == 0 && summary.Statuses != nil {
		statusCounts = "none"
	}
	return fmt.Sprintf("finished=%d orphaned=%d ghost=%d total=%d ghost_rate=%.2f%% statuses=%s", summary.Finished, summary.Orphaned, summary.Ghost, summary.Total, summary.GhostRate*100, statusCounts)
}

type debugInference struct {
	Status string `json:"status"`
}

type debugInferencesResponse struct {
	Inferences map[string]debugInference `json:"inferences"`
}

func waitForNoOrphanedWork(ctx context.Context, gatewayURL, apiKey string, maxGhostRate float64, ghostIDs map[string]struct{}, timeout time.Duration, aggregateRuntimeState ...bool) (TerminalSummary, error) {
	log.Printf("loadtest: stage=devshard_orphan_check_start timeout=%s max_ghost_rate=%.2f%%", timeout, maxGhostRate*100)
	aggregate := len(aggregateRuntimeState) > 0 && aggregateRuntimeState[0]
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	client := &http.Client{Timeout: 5 * time.Second}
	lastState := TerminalSummary{}
	var lastReadErr error
	observedSnapshot := false
	for {
		state, err := fetchDebugInferenceSummary(ctx, client, gatewayURL, apiKey, ghostIDs, aggregate)
		if err == nil {
			observedSnapshot = true
			lastReadErr = nil
			lastState = state
			if state.GhostRate > maxGhostRate {
				return state, fmt.Errorf("ghost inference rate %.4f exceeds max_ghost_rate %.4f (ghost=%d total=%d)", state.GhostRate, maxGhostRate, state.Ghost, state.Total)
			}
			if state.Orphaned == 0 {
				log.Printf("loadtest: orphan check passed finished=%d orphaned=%d ghost=%d total=%d ghost_rate=%.4f", state.Finished, state.Orphaned, state.Ghost, state.Total, state.GhostRate)
				return state, nil
			}
		} else {
			lastReadErr = err
		}
		select {
		case <-ctx.Done():
			return lastState, ctx.Err()
		case <-deadline.C:
			if !observedSnapshot {
				if lastReadErr == nil {
					lastReadErr = fmt.Errorf("no successful response from debug endpoint")
				}
				return lastState, fmt.Errorf("cannot inspect DevShard inference state within %s: %w", timeout, lastReadErr)
			}
			details := fmt.Sprintf("orphaned=%d statuses=%s ghost=%d total=%d ghost_rate=%.4f", lastState.Orphaned, formatStatusCounts(lastState.Statuses), lastState.Ghost, lastState.Total, lastState.GhostRate)
			if lastReadErr != nil {
				details += fmt.Sprintf("; last debug read error: %v", lastReadErr)
			}
			return lastState, fmt.Errorf("DevShard still has orphaned inference work after %s (%s)", timeout, details)
		case <-ticker.C:
		}
	}
}

func fetchDebugInferenceSummary(ctx context.Context, client *http.Client, gatewayURL, apiKey string, ghostIDs map[string]struct{}, aggregate bool) (TerminalSummary, error) {
	debugURLs := []string{gatewayURL}
	if aggregate {
		var err error
		debugURLs, err = fetchRuntimeDebugURLs(ctx, client, gatewayURL, apiKey)
		if err != nil {
			return TerminalSummary{}, err
		}
	}

	total := TerminalSummary{Statuses: make(map[string]int)}
	for _, debugURL := range debugURLs {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, debugURL+"/v1/debug/inferences", nil)
		if err != nil {
			return TerminalSummary{}, err
		}
		if apiKey != "" {
			request.Header.Set("Authorization", "Bearer "+apiKey)
		}
		response, err := client.Do(request)
		if err != nil {
			return TerminalSummary{}, fmt.Errorf("GET /v1/debug/inferences: %w", err)
		}
		var body debugInferencesResponse
		decodeErr := json.NewDecoder(response.Body).Decode(&body)
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			if decodeErr == nil {
				return TerminalSummary{}, fmt.Errorf("GET /v1/debug/inferences returned %s", response.Status)
			}
			return TerminalSummary{}, fmt.Errorf("GET /v1/debug/inferences returned %s (read body: %w)", response.Status, decodeErr)
		}
		if decodeErr != nil {
			return TerminalSummary{}, fmt.Errorf("decode /v1/debug/inferences: %w", decodeErr)
		}
		mergeTerminalSummary(&total, summarizeDebugInferences(body.Inferences, ghostIDs))
	}
	if total.Total > 0 {
		total.GhostRate = float64(total.Ghost) / float64(total.Total)
	}
	return total, nil
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

func fetchRuntimeDebugURLs(ctx context.Context, client *http.Client, gatewayURL, apiKey string) ([]string, error) {
	ids, err := fetchGatewayDevshardIDs(ctx, gatewayURL, apiKey)
	if err != nil {
		return nil, err
	}
	urls := make([]string, 0, len(ids))
	for _, id := range ids {
		urls = append(urls, scopedGatewayURL(gatewayURL, id))
	}
	return urls, nil
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
	dst.Total += src.Total
	dst.Orphaned += src.Orphaned
	for status, count := range src.Statuses {
		dst.Statuses[status] += count
	}
}

func summarizeDebugInferences(inferences map[string]debugInference, ghostIDs map[string]struct{}) TerminalSummary {
	statuses := make(map[string]int)
	finished := 0
	ghosts := 0
	orphaned := 0
	for id, inference := range inferences {
		if _, ghost := ghostIDs[id]; ghost {
			ghosts++
			continue
		}
		statuses[inference.Status]++
		if inference.Status == "finished" {
			finished++
		}
		if !isTerminalInferenceStatus(inference.Status) {
			orphaned++
		}
	}
	total := len(inferences)
	ghostRate := 0.0
	if total > 0 {
		ghostRate = float64(ghosts) / float64(total)
	}
	return TerminalSummary{
		Finished:  finished,
		Ghost:     ghosts,
		Total:     total,
		Orphaned:  orphaned,
		GhostRate: ghostRate,
		Statuses:  statuses,
	}
}

func isTerminalInferenceStatus(status string) bool {
	switch status {
	case "finished", "validated", "invalidated", "timed_out":
		return true
	default:
		return false
	}
}

// readGhostInferenceIDs derives deliberate no-send slots from the gateway
// trace, leaving production accounting and debug endpoints untouched.
func readGhostInferenceIDs(path string) (map[string]struct{}, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read gateway trace %s: %w", path, err)
	}
	ghosts := make(map[string]struct{})
	for _, line := range strings.Split(string(body), "\n") {
		if !strings.Contains(line, "stage=ghost_probe_skipped") {
			continue
		}
		for _, field := range strings.Fields(line) {
			if !strings.HasPrefix(field, "nonce=") {
				continue
			}
			nonce := strings.Trim(strings.TrimPrefix(field, "nonce="), `"`)
			if _, err := strconv.ParseUint(nonce, 10, 64); err != nil {
				return nil, fmt.Errorf("parse ghost nonce %q in %s: %w", nonce, path, err)
			}
			ghosts[nonce] = struct{}{}
			break
		}
	}
	return ghosts, nil
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

func writeComposeLogs(outputDir, testenvDir, project, composePath string) error {
	return writeComposeLogsTo(outputDir, testenvDir, project, composePath, "compose.log")
}

func writeComposeLogsTo(outputDir, testenvDir, project, composePath, filename string) error {
	// Long load tests can produce millions of container log lines. The tail is
	// enough for post-run diagnosis and keeps artifact collection bounded.
	cmd := exec.Command("docker", "compose", "-p", project, "-f", composePath, "logs", "--no-color", "--tail", "5000")
	cmd.Dir = testenvDir
	body, err := cmd.CombinedOutput()
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outputDir, filename), body, 0o644)
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
		if failures, ok := assertionErr.(*assertionFailures); ok {
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
