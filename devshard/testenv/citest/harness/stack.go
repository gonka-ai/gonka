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

const defaultStackTimeout = 12 * time.Minute

// Stack is a generated compose workdir for Docker citest.
type Stack struct {
	WorkDir       string
	TestenvDir    string
	ConfigPath    string
	ComposePath   string
	Timeout       time.Duration
	Observability bool
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
		WorkDir:     workDir,
		TestenvDir:  testenvDir,
		ConfigPath:  filepath.Join(workDir, "config.yaml"),
		ComposePath: filepath.Join(workDir, "docker-compose.yml"),
		Timeout:     defaultStackTimeout,
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
		args = append(args, "-f", filepath.Join(s.TestenvDir, "docker-compose.observability.yml"))
		ipOverride := filepath.Join(s.WorkDir, "docker-compose.observability.ip.yml")
		if _, err := os.Stat(ipOverride); err == nil {
			args = append(args, "-f", ipOverride)
		}
	}
	return args
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
	up.Env = append(os.Environ(), "COMPOSE_HTTP_TIMEOUT=300")
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
	args = append(args, "down", "-v")
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = s.WorkDir
	_, _ = cmd.CombinedOutput()
}

// StopService stops a compose service without removing volumes (fault injection).
func (s *Stack) StopService(t *testing.T, service string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", append(append([]string{"compose"}, s.composeFileArgs()...), "stop", service)...)
	cmd.Dir = s.WorkDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker compose stop %s: %v\n%s", service, err, out)
	}
}

// StartService starts a previously stopped compose service.
func (s *Stack) StartService(t *testing.T, service string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", append(append([]string{"compose"}, s.composeFileArgs()...), "start", service)...)
	cmd.Dir = s.WorkDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker compose start %s: %v\n%s", service, err, out)
	}
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
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// ComposeLogsSince returns Docker Compose logs emitted at or after the given time.
func (s *Stack) ComposeLogsSince(since time.Time, services ...string) (string, error) {
	args := append(append([]string{"compose"}, s.composeFileArgs()...), "logs", "--no-color", "--since", since.UTC().Format(time.RFC3339Nano))
	args = append(args, services...)
	cmd := exec.Command("docker", args...)
	cmd.Dir = s.WorkDir
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

func (s *Stack) runningServices() (map[string]struct{}, error) {
	cmd := exec.Command("docker", append(append([]string{"compose"}, s.composeFileArgs()...), "ps", "--status", "running", "--format", "{{.Service}}")...)
	cmd.Dir = s.WorkDir
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
