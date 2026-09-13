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
)

type RunnerConfig struct {
	ScenarioPath string
	ProfilesDir  string
	TestenvDir   string
	OutputDir    string
	KeepStack    bool
}

type RunResult struct {
	Summary     Summary
	OutputDir   string
	WorkDir     string
	GatewayURL  string
	Allocations map[string]uint64
}

func RunScenario(ctx context.Context, opts RunnerConfig) (result RunResult, err error) {
	if opts.ScenarioPath == "" || opts.TestenvDir == "" || opts.OutputDir == "" {
		return RunResult{}, fmt.Errorf("scenario path, testenv directory, and output directory are required")
	}
	scenario, err := LoadScenario(opts.ScenarioPath)
	if err != nil {
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
	if err := os.MkdirAll(opts.OutputDir, 0o755); err != nil {
		return RunResult{}, fmt.Errorf("create result directory: %w", err)
	}
	defer func() {
		if err != nil {
			_ = writeAssertions(opts.OutputDir, err)
		}
	}()
	if err := copyFile(opts.ScenarioPath, filepath.Join(opts.OutputDir, "run.yaml")); err != nil {
		return RunResult{}, err
	}

	workDir, err := os.MkdirTemp(opts.TestenvDir, "loadtest-")
	if err != nil {
		return RunResult{}, fmt.Errorf("create testenv work directory: %w", err)
	}
	result.WorkDir = workDir
	if !opts.KeepStack {
		defer func() { _ = os.RemoveAll(workDir) }()
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
	for attempt := 1; attempt <= maxStartAttempts; attempt++ {
		project = "loadtest-" + strconv.FormatInt(time.Now().UnixNano(), 36)
		log.Printf("loadtest: starting isolated Docker stack (attempt %d/%d, project %s)", attempt, maxStartAttempts, project)
		if err := writeRunnerConfig(opts.TestenvDir, workDir, scenario, profiles); err != nil {
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
	apiKey, err := envValue(filepath.Join(workDir, ".env"), "TESTENV_ADMIN_API_KEY")
	if err != nil {
		return RunResult{}, err
	}
	result.GatewayURL = fmt.Sprintf("http://127.0.0.1:%d", cfg.Devshardctl.Port)
	log.Printf("loadtest: waiting for gateway at %s", result.GatewayURL)
	if err := waitGateway(ctx, result.GatewayURL); err != nil {
		_ = writeComposeLogs(opts.OutputDir, opts.TestenvDir, project, composePath)
		return RunResult{}, err
	}

	log.Printf("loadtest: gateway is ready; generating %s workload", scenario.Scenario)
	summary, err := RunGenerator(ctx, GeneratorConfig{
		GatewayURL: result.GatewayURL,
		APIKey:     apiKey,
		Scenario:   scenario,
		OutputDir:  opts.OutputDir,
	})
	if err != nil {
		_ = writeComposeLogs(opts.OutputDir, opts.TestenvDir, project, composePath)
		return RunResult{}, err
	}
	result.Summary = summary
	log.Printf("loadtest: generated %d requests; validating terminal state", summary.Requests)
	allocations, err := fetchAllocations(ctx, cfg.MockDapi.HTTPPort)
	if err != nil {
		_ = writeComposeLogs(opts.OutputDir, opts.TestenvDir, project, composePath)
		return RunResult{}, err
	}
	result.Allocations = allocations
	if err := assertRun(ctx, scenario, summary, allocations, result.GatewayURL, apiKey); err != nil {
		_ = writeGatewayInferences(ctx, result.GatewayURL, apiKey, opts.OutputDir)
		_ = writeComposeLogs(opts.OutputDir, opts.TestenvDir, project, composePath)
		return RunResult{}, err
	}
	if err := writeAssertions(opts.OutputDir, nil); err != nil {
		return RunResult{}, err
	}
	return result, nil
}

func writeRunnerConfig(testenvDir, workDir string, scenario Scenario, profiles map[string]Profile) error {
	cfg, err := config.Load(filepath.Join(testenvDir, "config", "config.yaml"))
	if err != nil {
		return err
	}
	cfg.Versiond.Mode = scenario.Topology.VersiondMode
	cfg.Postgres.PerHost = scenario.Topology.Storage == "per_host"
	cfg.Params.MaxNonce = scenario.Topology.Chain.MaxNonce
	for i := range cfg.Escrows {
		cfg.Escrows[i].Amount = scenario.Topology.Chain.EscrowAmount
	}
	cfg.MockOpenAI.Nodes = make([]config.MockOpenAINodeCfg, 0, len(scenario.Topology.MockML.Nodes))
	for _, node := range scenario.Topology.MockML.Nodes {
		profile := profiles[node.Profile]
		cfg.MockOpenAI.Nodes = append(cfg.MockOpenAI.Nodes, config.MockOpenAINodeCfg{
			Name:          node.Name,
			TTFT:          profile.TTFT,
			TokenInterval: profile.TokenInterval,
			Workers:       profile.Workers,
			Queue:         profile.Queue,
		})
	}
	if err := randomizeTestenv(cfg); err != nil {
		return err
	}
	return cfg.Save(filepath.Join(workDir, "config.yaml"))
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

func assertRun(ctx context.Context, scenario Scenario, summary Summary, allocations map[string]uint64, gatewayURL, apiKey string) error {
	if summary.Requests == 0 {
		return fmt.Errorf("load generator produced no requests")
	}
	if summary.ErrorRate > scenario.Thresholds.ErrorRate {
		return fmt.Errorf("error rate %.4f exceeds threshold %.4f", summary.ErrorRate, scenario.Thresholds.ErrorRate)
	}
	if scenario.Assertions.MockML.RequireEachNodeUsed {
		for _, node := range scenario.Topology.MockML.Nodes {
			if allocations[node.Name] == 0 {
				return fmt.Errorf("mock ML node %s received no allocations", node.Name)
			}
		}
	}
	if scenario.Assertions.Devshard.RequireDrain || scenario.Assertions.Devshard.NoOrphanedWork {
		return waitForFinishedInferences(ctx, gatewayURL, apiKey, summary.Completed, scenario.DrainDuration())
	}
	return nil
}

func waitForFinishedInferences(ctx context.Context, gatewayURL, apiKey string, expected int, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	client := &http.Client{Timeout: 5 * time.Second}
	lastStatuses := map[string]int(nil)
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, gatewayURL+"/v1/debug/inferences", nil)
		if err != nil {
			return err
		}
		if apiKey != "" {
			request.Header.Set("Authorization", "Bearer "+apiKey)
		}
		response, err := client.Do(request)
		if err == nil && response.StatusCode == http.StatusOK {
			var body struct {
				Inferences map[string]struct {
					Status string `json:"status"`
				} `json:"inferences"`
			}
			err = json.NewDecoder(response.Body).Decode(&body)
			_ = response.Body.Close()
			if err == nil {
				finished := 0
				allFinished := true
				lastStatuses = make(map[string]int)
				for _, inference := range body.Inferences {
					lastStatuses[inference.Status]++
					if inference.Status == "finished" {
						finished++
					} else {
						allFinished = false
					}
				}
				if finished >= expected && allFinished {
					return nil
				}
			}
		} else if response != nil {
			_ = response.Body.Close()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("Devshard did not drain %d successful inferences within %s (last statuses: %s)", expected, timeout, formatStatusCounts(lastStatuses))
		case <-ticker.C:
		}
	}
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
	cmd := exec.Command("docker", "compose", "-p", project, "-f", composePath, "logs", "--no-color")
	cmd.Dir = testenvDir
	body, err := cmd.CombinedOutput()
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outputDir, "compose.log"), body, 0o644)
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

func writeAssertions(outputDir string, assertionErr error) error {
	report := map[string]any{"passed": assertionErr == nil}
	if assertionErr != nil {
		report["error"] = assertionErr.Error()
	}
	body, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outputDir, "assertions.json"), append(body, '\n'), 0o644)
}
