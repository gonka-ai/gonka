package harness

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"devshard/testenv/config"

	"github.com/stretchr/testify/require"
)

const (
	defaultStackTimeout       = 12 * time.Minute
	composeCleanupStopTimeout = 5 * time.Second
)

// Stack is a generated compose workdir for Docker citest.
type Stack struct {
	WorkDir       string
	TestenvDir    string
	ConfigPath    string
	ComposePath   string
	Timeout       time.Duration
	Observability bool
	// ComposeProject is the docker compose project label. Empty uses the
	// workdir basename (Compose default). Observability citest sets this so
	// Promtail only ships this stack's containers.
	ComposeProject string
}

// Endpoints are host-published URLs for health probes.
type Endpoints struct {
	MockChainRPC   string
	MockChainGRPC  string
	MockChainAdmin string
	MockDapiHTTP   string
	MockDapiGRPC   string
	MockOpenAIHTTP string
	RouterHTTP     string
	GatewayHTTP    string
}

// NewStack creates a temp workdir under testenv and registers cleanup.
func NewStack(t *testing.T, prefix string) *Stack {
	t.Helper()
	RequireDocker(t)

	testenvDir, err := filepath.Abs("..")
	require.NoError(t, err)

	workDir, err := os.MkdirTemp(testenvDir, prefix)
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(workDir) })

	return &Stack{
		WorkDir:        workDir,
		TestenvDir:     testenvDir,
		ConfigPath:     filepath.Join(workDir, "config.yaml"),
		ComposePath:    filepath.Join(workDir, "docker-compose.yml"),
		Timeout:        defaultStackTimeout,
		ComposeProject: strings.ToLower(filepath.Base(workDir)),
	}
}

// RequireDocker skips when docker is unavailable.
func RequireDocker(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not on PATH")
	}
}

// RequireLinuxDevshardd skips when the container-mounted devshardd binary is missing.
func RequireLinuxDevshardd(t *testing.T, testenvDir string) {
	t.Helper()
	devsharddPath := filepath.Join(testenvDir, "..", "..", "build", "devshardd")
	if _, err := os.Stat(devsharddPath); err != nil {
		t.Skipf("linux devshardd binary missing at %s (run: make -C testenv build-devshardd)", devsharddPath)
	}
}

// RunGencompose renders compose + keyrings into the workdir.
func (s *Stack) RunGencompose(t *testing.T) {
	t.Helper()
	gen := exec.Command("go", "run", "./cmd/gencompose",
		"-config", s.ConfigPath,
		"-out", s.ComposePath,
	)
	gen.Dir = s.TestenvDir
	out, err := gen.CombinedOutput()
	if err != nil {
		t.Fatalf("gencompose: %v\n%s", err, out)
	}
	fixComposePaths(t, s.ComposePath, s.TestenvDir)
	PatchComposeUseRandomHostPorts(t, s.ComposePath)
}

// LoadConfig reads the generated config.yaml after gencompose.
func (s *Stack) LoadConfig(t *testing.T) *config.File {
	t.Helper()
	cfg, err := config.Load(s.ConfigPath)
	require.NoError(t, err)
	return cfg
}

// Endpoints reads Docker-assigned localhost ports from a running compose stack.
func (s *Stack) Endpoints(t *testing.T, cfg *config.File) Endpoints {
	t.Helper()
	eps := s.MockChainEndpoints(t, cfg)
	eps.MockDapiHTTP = "http://" + s.composePublishedAddr(t, "mock-dapi", cfg.MockDapi.HTTPPort)
	eps.MockDapiGRPC = s.composePublishedAddr(t, "mock-dapi", cfg.MockDapi.GRPCPort)
	eps.MockOpenAIHTTP = "http://" + s.composePublishedAddr(t, "mock-openai", cfg.MockOpenAI.HTTPPort)
	eps.RouterHTTP = "http://" + s.composePublishedAddr(t, "versiond-router", 8080)
	eps.GatewayHTTP = "http://" + s.composePublishedAddr(t, "devshardctl", cfg.Devshardctl.Port)
	return eps
}

// RouterHTTP is the host-published versiond-router URL. Use this when the
// gateway is stopped (`Endpoints` also queries devshardctl).
func (s *Stack) RouterHTTP(t *testing.T) string {
	t.Helper()
	return "http://" + s.composePublishedAddr(t, "versiond-router", 8080)
}

// MockChainEndpoints reads Docker-assigned host ports for a mock-chain-only stack.
func (s *Stack) MockChainEndpoints(t *testing.T, cfg *config.File) Endpoints {
	t.Helper()
	return Endpoints{
		MockChainRPC:   "http://" + s.composePublishedAddr(t, "mock-chain", cfg.MockChain.RPCPort),
		MockChainGRPC:  s.composePublishedAddr(t, "mock-chain", cfg.MockChain.GRPCPort),
		MockChainAdmin: "http://" + s.composePublishedAddr(t, "mock-chain", cfg.MockChain.TestenvPort),
	}
}

func (s *Stack) composePublishedAddr(t *testing.T, service string, targetPort int) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	args := append([]string{"compose"}, s.composeFileArgs()...)
	args = append(args, "port", service, strconv.Itoa(targetPort))
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = s.WorkDir
	cmd.Env = s.composeEnv()
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "docker compose port %s %d\n%s", service, targetPort, out)
	raw := strings.TrimSpace(string(out))
	host, port, err := net.SplitHostPort(raw)
	require.NoError(t, err, "parse docker compose port output %q", raw)
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// Up starts the stack with docker compose up (expects citest-images built; pulls missing hub images).
func (s *Stack) Up(t *testing.T) {
	t.Helper()
	s.composeUp(t, false, nil)
}

// UpBuild starts the stack and rebuilds images first.
func (s *Stack) UpBuild(t *testing.T) {
	t.Helper()
	s.composeUp(t, true, nil)
}

// UpServices starts only the named compose services (optionally rebuilding images).
func (s *Stack) UpServices(t *testing.T, build bool, services ...string) {
	t.Helper()
	s.composeUp(t, build, services)
}

// UpWithObservability starts the stack and observability overlay (see PrepareObservabilityOverlay).
func (s *Stack) UpWithObservability(t *testing.T, cfg *config.File) {
	t.Helper()
	s.PrepareObservabilityOverlay(t, cfg)
	s.composeUp(t, false, nil)
}

func (s *Stack) composeFileArgs() []string {
	args := []string{"-f", s.ComposePath}
	if s.Observability {
		overlay := filepath.Join(s.WorkDir, "docker-compose.observability.yml")
		if _, err := os.Stat(overlay); err != nil {
			overlay = filepath.Join(s.TestenvDir, "docker-compose.observability.yml")
		}
		args = append(args, "-f", overlay)
		ipOverride := filepath.Join(s.WorkDir, "docker-compose.observability.ip.yml")
		if _, err := os.Stat(ipOverride); err == nil {
			args = append(args, "-f", ipOverride)
		}
	}
	return args
}

func (s *Stack) composeEnv() []string {
	env := append(os.Environ(), "COMPOSE_HTTP_TIMEOUT=300")
	if s.ComposeProject != "" {
		env = append(env, "COMPOSE_PROJECT_NAME="+s.ComposeProject)
	}
	return env
}

func (s *Stack) composeUp(t *testing.T, build bool, services []string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), s.Timeout)
	defer cancel()

	t.Cleanup(func() { s.Down(t) })

	args := append([]string{"compose"}, s.composeFileArgs()...)
	args = append(args, "up", "-d", "--wait", "--pull", "missing")
	if build {
		args = append(args, "--build")
	}
	args = append(args, services...)
	up := exec.CommandContext(ctx, "docker", args...)
	up.Dir = s.WorkDir
	up.Env = s.composeEnv()
	out, err := up.CombinedOutput()
	if err != nil {
		DumpComposeLogs(t, s)
		s.Down(t)
		t.Fatalf("docker compose up: %v\n%s", err, out)
	}
}

// Down stops the stack and removes volumes.
func (s *Stack) Down(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	args := append([]string{"compose"}, s.composeFileArgs()...)
	args = append(args,
		"down",
		"--volumes",
		"--remove-orphans",
		"--timeout", strconv.Itoa(int(composeCleanupStopTimeout/time.Second)),
	)
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = s.WorkDir
	cmd.Env = s.composeEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("docker compose cleanup: %v\n%s", err, out)
	}
}

// StopService stops a compose service without removing volumes (fault injection).
func (s *Stack) StopService(t *testing.T, service string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", append(append([]string{"compose"}, s.composeFileArgs()...), "stop", service)...)
	cmd.Dir = s.WorkDir
	cmd.Env = s.composeEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker compose stop %s: %v\n%s", service, err, out)
	}
}

// ServiceStopResult records Docker's terminal state after a compose stop. Exit
// code 137 proves Docker had to consume its SIGKILL backstop even though the
// compose command itself reports success.
type ServiceStopResult struct {
	ContainerID string
	ExitCode    int
}

// StopServiceGracefully sends the container stop signal and gives versiond a
// caller-controlled grace before Docker's SIGKILL backstop. It returns errors
// instead of failing a test so callers can run it concurrently with live work.
func (s *Stack) StopServiceGracefully(service string, grace time.Duration) (ServiceStopResult, error) {
	containerID, err := s.containerID(service)
	if err != nil {
		return ServiceStopResult{}, err
	}
	graceSeconds := int((grace + time.Second - 1) / time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), grace+30*time.Second)
	defer cancel()
	args := append([]string{"compose"}, s.composeFileArgs()...)
	args = append(args, "stop", "--timeout", strconv.Itoa(graceSeconds), service)
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = s.WorkDir
	cmd.Env = s.composeEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ServiceStopResult{}, fmt.Errorf("docker compose stop %s: %w: %s", service, err, out)
	}
	exitCode, err := containerExitCode(containerID)
	if err != nil {
		return ServiceStopResult{}, err
	}
	return ServiceStopResult{ContainerID: containerID, ExitCode: exitCode}, nil
}

// StartService starts a previously stopped compose service.
func (s *Stack) StartService(t *testing.T, service string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", append(append([]string{"compose"}, s.composeFileArgs()...), "start", service)...)
	cmd.Dir = s.WorkDir
	cmd.Env = s.composeEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker compose start %s: %v\n%s", service, err, out)
	}
}

// PauseService freezes a compose service (fault injection; process stays up).
func (s *Stack) PauseService(t *testing.T, service string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", append(append([]string{"compose"}, s.composeFileArgs()...), "pause", service)...)
	cmd.Dir = s.WorkDir
	cmd.Env = s.composeEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker compose pause %s: %v\n%s", service, err, out)
	}
}

// UnpauseService resumes a previously paused compose service.
func (s *Stack) UnpauseService(t *testing.T, service string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", append(append([]string{"compose"}, s.composeFileArgs()...), "unpause", service)...)
	cmd.Dir = s.WorkDir
	cmd.Env = s.composeEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker compose unpause %s: %v\n%s", service, err, out)
	}
}

// WaitComposeLogsContain polls compose logs until needle appears.
func (s *Stack) WaitComposeLogsContain(t *testing.T, timeout time.Duration, needle string, services ...string) string {
	t.Helper()
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	var last string
	ok := AssertEventually(t, timeout, 2*time.Second, func() bool {
		out, err := s.ComposeLogsTail(400, services...)
		if err != nil {
			last = err.Error()
			return false
		}
		last = out
		return strings.Contains(out, needle)
	})
	require.True(t, ok, "compose logs missing %q within %s\n%s", needle, timeout, last)
	return last
}

// ComposeLogs returns tail logs for optional services (all services when empty).
func (s *Stack) ComposeLogs(services ...string) (string, error) {
	return s.ComposeLogsTail(120, services...)
}

// ComposeLogsTail is ComposeLogs with an explicit --tail line count.
func (s *Stack) ComposeLogsTail(tail int, services ...string) (string, error) {
	if tail <= 0 {
		tail = 120
	}
	args := append(append([]string{"compose"}, s.composeFileArgs()...), "logs", "--no-color", "--tail", strconv.Itoa(tail))
	args = append(args, services...)
	cmd := exec.Command("docker", args...)
	cmd.Dir = s.WorkDir
	cmd.Env = s.composeEnv()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// RequireServicesRunning asserts docker compose reports each service as running.
func (s *Stack) RequireServicesRunning(t *testing.T, services ...string) {
	t.Helper()
	running, err := s.runningServices()
	require.NoError(t, err)
	for _, name := range services {
		require.Contains(t, running, name, "service %s not running; running=%v", name, running)
	}
}

func (s *Stack) ServiceRunning(service string) (bool, error) {
	running, err := s.runningServices()
	if err != nil {
		return false, err
	}
	_, ok := running[service]
	return ok, nil
}

func (s *Stack) runningServices() (map[string]struct{}, error) {
	cmd := exec.Command("docker", append(append([]string{"compose"}, s.composeFileArgs()...), "ps", "--status", "running", "--format", "{{.Service}}")...)
	cmd.Dir = s.WorkDir
	cmd.Env = s.composeEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("docker compose ps: %w: %s", err, out)
	}
	set := make(map[string]struct{})
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		set[line] = struct{}{}
	}
	return set, nil
}
