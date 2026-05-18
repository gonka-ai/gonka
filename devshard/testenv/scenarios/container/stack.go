package container

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"devshard/testenv/config"
)

// E2EWorkspaceOption configures [PrepareIsolatedE2EWorkspace].
type E2EWorkspaceOption func(*e2eWorkspaceOpts)

type e2eWorkspaceOpts struct {
	mutateConfig func(*testing.T, *config.Config)
	afterConfig  []func(*testing.T, string)
}

// WithConfigMutate loads workspace config.yaml after the canonical copy, applies fn,
// validates, and saves. Runs before [e2eWorkspaceOpts.afterConfig] hooks.
func WithConfigMutate(fn func(*testing.T, *config.Config)) E2EWorkspaceOption {
	return func(o *e2eWorkspaceOpts) {
		o.mutateConfig = fn
	}
}

// TestenvDir returns the absolute path to devshard/testenv (directory containing docker-compose.yml).
func TestenvDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(1)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

// ComposeFilePath returns the base docker-compose.yml under testenv.
func ComposeFilePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(TestenvDir(t), "docker-compose.yml")
}

// ComposeProject returns a compose project name tied to this OS process.
// Prefer ComposeProjectForTest for E2E tests so names stay unique when multiple
// tests run in the same process.
func ComposeProject() string {
	return fmt.Sprintf("containere2e%d", os.Getpid())
}

var composeProjectSeq uint64

// ComposeProjectForTest returns a Docker Compose project name unique within this
// test binary run. Container scenarios publish fixed host ports (8081, 9100, …);
// do not use t.Parallel() for those tests or the second stack will fail to bind.
func ComposeProjectForTest(t *testing.T) string {
	t.Helper()
	n := atomic.AddUint64(&composeProjectSeq, 1)
	return fmt.Sprintf("containere2e%d_%d", os.Getpid(), n)
}

const defaultContainerE2EComposeProject = "heightsynce2e"

// ReuseContainerE2EStack reports whether run-container-heightsync-e2e.sh already
// brought up the shared compose project (TESTENV_REUSE_STACK=1).
func ReuseContainerE2EStack() bool {
	return os.Getenv("TESTENV_REUSE_STACK") == "1"
}

// ContainerE2EComposeProject is the docker compose project for the shared stack
// (env CONTAINER_E2E_PROJECT, default heightsynce2e).
func ContainerE2EComposeProject() string {
	if p := strings.TrimSpace(os.Getenv("CONTAINER_E2E_PROJECT")); p != "" {
		return p
	}
	return defaultContainerE2EComposeProject
}

// PrintReuseStack logs that tests attach to a pre-started stack.
func PrintReuseStack(t *testing.T) {
	t.Helper()
	t.Logf("TESTENV_REUSE_STACK=1 — using compose project %q in %s (no per-test compose up/down)",
		ContainerE2EComposeProject(), TestenvDir(t))
}

// waitComposeServiceLog polls docker compose logs until service output contains all substrings.
func waitComposeServiceLog(t *testing.T, ws, project, service string, timeout time.Duration, contains ...string) {
	t.Helper()
	if len(contains) == 0 {
		return
	}
	composeFile := filepath.Join(ws, "docker-compose.yml")
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		cmd := exec.CommandContext(ctx, "docker", "compose", "-f", composeFile, "-p", project,
			"logs", "--tail=300", service)
		out, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			t.Logf("compose logs %s: %v", service, err)
		} else {
			text := string(out)
			ok := true
			for _, sub := range contains {
				if !strings.Contains(text, sub) {
					ok = false
					break
				}
			}
			if ok {
				t.Logf("compose logs %s: matched %v", service, contains)
				return
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s logs to contain %v", service, contains)
}

// resetSharedStackHostDB clears per-host SQLite dirs, devshardctl session DB, and restarts
// devshardd + devshardctl (used when TESTENV_RESET_STACK_DB=1).
func resetSharedStackHostDB(t *testing.T, ws, project string) {
	t.Helper()
	ctlDBDir := filepath.Join(ws, "db", "devshardctl")
	if err := os.RemoveAll(ctlDBDir); err != nil {
		t.Fatalf("remove %s: %v", ctlDBDir, err)
	}
	if err := os.MkdirAll(ctlDBDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", ctlDBDir, err)
	}
	for i := 0; i < 4; i++ {
		dbDir := filepath.Join(ws, "db", fmt.Sprintf("devshardd-testenv-%d", i))
		entries, err := os.ReadDir(dbDir)
		if err != nil {
			if os.IsNotExist(err) {
				if mkErr := os.MkdirAll(dbDir, 0o755); mkErr != nil {
					t.Fatalf("mkdir %s: %v", dbDir, mkErr)
				}
				continue
			}
			t.Fatalf("read %s: %v", dbDir, err)
		}
		for _, ent := range entries {
			if err := os.RemoveAll(filepath.Join(dbDir, ent.Name())); err != nil {
				t.Fatalf("remove %s: %v", filepath.Join(dbDir, ent.Name()), err)
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	services := []string{
		"devshardctl",
		"devshardd-testenv-0", "devshardd-testenv-1", "devshardd-testenv-2", "devshardd-testenv-3",
	}
	args := append([]string{"restart"}, services...)
	if err := DockerCompose(ctx, ws, project, nil, nil, args...).Run(); err != nil {
		t.Fatalf("compose restart after db reset: %v", err)
	}
	WaitCoreStackServicesRunningOrFail(t, ctx, ws, project, time.Now().Add(4*time.Minute))
}

// ModuleRoot is the devshard Go module root (parent directory of testenv/).
func ModuleRoot(t *testing.T) string {
	t.Helper()
	return filepath.Clean(filepath.Join(TestenvDir(t), ".."))
}

// PrepareIsolatedE2EWorkspace materializes a fresh directory (under t.TempDir)
// with docker-compose.yml, config.yaml, observability config, and empty per-host
// db/ trees so each test does not share repo-local SQLite or config files.
//
// Options run after the canonical tree is copied: [WithConfigMutate] persists an
// edited config.yaml; [e2eWorkspaceOpts.afterConfig] hooks may patch sibling artifacts.
// Note: canonical docker-compose.yml is produced by gencompose (see container TestMain)
// and already lists HEIGHT_SYNC_ANCHOR_PERIOD_NONCES / HEIGHT_SYNC_SYNC_TURN_SLOTS per
// service — do not append duplicate YAML keys.
func PrepareIsolatedE2EWorkspace(t *testing.T, opts ...E2EWorkspaceOption) string {
	t.Helper()
	var wo e2eWorkspaceOpts
	for _, opt := range opts {
		opt(&wo)
	}

	canonical := TestenvDir(t)
	modRoot := filepath.Clean(filepath.Join(canonical, ".."))
	modRootDocker := filepath.ToSlash(modRoot)

	composeSrc, err := os.ReadFile(filepath.Join(canonical, "docker-compose.yml"))
	if err != nil {
		t.Fatalf("read canonical docker-compose.yml: %v", err)
	}
	// Compose file lives outside the module root; rewrite build contexts.
	patched := strings.ReplaceAll(string(composeSrc), "      context: ..", "      context: "+modRootDocker)

	ws := filepath.Join(t.TempDir(), "e2e-stack")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws, "docker-compose.yml"), []byte(patched), 0o644); err != nil {
		t.Fatalf("write workspace docker-compose.yml: %v", err)
	}

	cfgSrc, err := os.ReadFile(filepath.Join(canonical, "config.yaml"))
	if err != nil {
		t.Fatalf("read canonical config.yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws, "config.yaml"), cfgSrc, 0o644); err != nil {
		t.Fatalf("write workspace config.yaml: %v", err)
	}

	if wo.mutateConfig != nil {
		cfgPath := filepath.Join(ws, "config.yaml")
		cfg, err := config.Load(cfgPath)
		if err != nil {
			t.Fatalf("load workspace config for mutate: %v", err)
		}
		wo.mutateConfig(t, cfg)
		if err := cfg.Validate(); err != nil {
			t.Fatalf("workspace config after mutate: %v", err)
		}
		if err := cfg.Save(cfgPath); err != nil {
			t.Fatalf("save workspace config: %v", err)
		}
	}
	for _, fn := range wo.afterConfig {
		fn(t, ws)
	}

	obsSrc := filepath.Join(canonical, "observability")
	if st, err := os.Stat(obsSrc); err != nil || !st.IsDir() {
		t.Fatalf("observability directory: %v", err)
	}
	obsDst := filepath.Join(ws, "observability")
	if err := copyDirRecursive(obsSrc, obsDst); err != nil {
		t.Fatalf("copy observability: %v", err)
	}

	for i := range 4 {
		dbDir := filepath.Join(ws, "db", fmt.Sprintf("devshardd-testenv-%d", i))
		if err := os.MkdirAll(dbDir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dbDir, err)
		}
	}

	t.Logf("isolated e2e workspace: %s", ws)
	return ws
}

func copyDirRecursive(srcRoot, dstRoot string) error {
	return filepath.WalkDir(srcRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcRoot, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dstRoot, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}

// PruneStaleContainerE2EDockerStacks tears down compose projects whose bridge network
// still exists as *_testenv. Every gencompose stack pins 172.30.0.0/24; Docker rejects a
// second network on that pool ("Pool overlaps with other one on this address space").
// Leftovers come from crashed container E2E tests, manual `docker compose up` in testenv/,
// or run-stack-citest.sh — not only containere2e* project names.
func PruneStaleContainerE2EDockerStacks(t *testing.T) {
	t.Helper()
	canonical := TestenvDir(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	ls := exec.CommandContext(ctx, "docker", "network", "ls", "--format", "{{.Name}}")
	out, err := ls.Output()
	if err != nil {
		t.Logf("prune e2e stacks: docker network ls: %v", err)
		return
	}
	seenProj := make(map[string]struct{})
	for _, name := range strings.Split(string(out), "\n") {
		name = strings.TrimSpace(name)
		if name == "" || !strings.HasSuffix(name, "_testenv") {
			continue
		}
		proj := strings.TrimSuffix(name, "_testenv")
		if proj == "" {
			continue
		}
		if _, ok := seenProj[proj]; ok {
			continue
		}
		seenProj[proj] = struct{}{}
		dctx, dcancel := context.WithTimeout(context.Background(), 2*time.Minute)
		errDown := DockerCompose(dctx, canonical, proj, nil, nil, "down", "--remove-orphans", "--timeout", "60").Run()
		dcancel()
		if errDown != nil {
			t.Logf("stale compose project %q: down failed: %v (try: docker network rm %q)", proj, errDown, name)
			rmCtx, rmCancel := context.WithTimeout(context.Background(), 30*time.Second)
			_ = exec.CommandContext(rmCtx, "docker", "network", "rm", name).Run()
			rmCancel()
			continue
		}
		t.Logf("tore down stale compose project %q (freed %s)", proj, name)
	}
}

// DockerCompose runs docker compose with the given args; working directory is testenvRoot.
func DockerCompose(ctx context.Context, testenvRoot, project string, stdout, stderr io.Writer, args ...string) *exec.Cmd {
	composeFile := filepath.Join(testenvRoot, "docker-compose.yml")
	full := append([]string{"compose", "-f", composeFile, "-p", project}, args...)
	cmd := exec.CommandContext(ctx, "docker", full...)
	cmd.Dir = testenvRoot
	if stdout != nil {
		cmd.Stdout = stdout
	}
	if stderr != nil {
		cmd.Stderr = stderr
	}
	return cmd
}

// ttyPalette carries ANSI SGR fragments for compose debug hints.
type ttyPalette struct {
	bold, dim, title, comment, cmd, rule, reset string
}

func ansiComposeHintsPalette() ttyPalette {
	return ttyPalette{
		bold:    "\033[1m",
		dim:     "\033[2m",
		title:   "\033[1;35m",
		comment: "\033[33m",
		cmd:     "\033[1;36m",
		rule:    "\033[90m",
		reset:   "\033[0m",
	}
}

// composeHintsColorPalette decides whether to emit ANSI for [PrintComposeDebugHints].
//
// `go test` almost always attaches the test binary's stderr to a pipe (not a tty), so an
// isatty-only check would strip color even in Cursor/iTerm where output is ultimately a terminal.
// We enable color when TERM looks capable (and not "dumb"), while respecting NO_COLOR, CI, and
// FORCE_COLOR (see https://no-color.org/ and common FORCE_COLOR conventions).
func composeHintsColorPalette() ttyPalette {
	if os.Getenv("NO_COLOR") != "" {
		return ttyPalette{}
	}
	force := strings.TrimSpace(os.Getenv("FORCE_COLOR"))
	if force != "" && force != "0" {
		return ansiComposeHintsPalette()
	}
	if os.Getenv("CI") != "" {
		return ttyPalette{}
	}
	term := strings.TrimSpace(os.Getenv("TERM"))
	if term != "" && term != "dumb" {
		return ansiComposeHintsPalette()
	}
	fi, err := os.Stderr.Stat()
	if err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		return ansiComposeHintsPalette()
	}
	return ttyPalette{}
}

// PrintComposeDebugHints writes human-readable, copy-paste docker compose commands to os.Stderr
// (not via testing.T.Log), so `go test -v` does not prefix each line with file:line. Comments use
// shell `#` lines; commands are highlighted per [composeHintsColorPalette] (set FORCE_COLOR=1 in
// CI if you want ANSI there; NO_COLOR=1 disables color everywhere).
func PrintComposeDebugHints(workspaceRoot, project string) {
	p := composeHintsColorPalette()
	c := filepath.Join(workspaceRoot, "docker-compose.yml")

	fprintf := func(format string, args ...any) {
		_, _ = fmt.Fprintf(os.Stderr, format, args...)
	}

	fprintf("\n")
	fprintf("%s══════════════════════════════════════════════════════════════════%s\n", p.rule, p.reset)
	fprintf("%s E2E Docker stack — manual log / status commands%s\n", p.title, p.reset)
	fprintf("%s══════════════════════════════════════════════════════════════════%s\n", p.rule, p.reset)
	fprintf("\n")
	fprintf("%sCompose project:%s  %s%s%s\n", p.bold, p.reset, p.cmd, project, p.reset)
	fprintf("%sWorkspace:%s       %s%s%s\n", p.bold, p.reset, p.dim, workspaceRoot, p.reset)
	fprintf("%sCompose file:%s    %s%s%s\n", p.bold, p.reset, p.dim, c, p.reset)
	fprintf("\n")
	fprintf("%s# Follow devshardctl (operator / OpenAI-compatible proxy) — live tail%s\n", p.comment, p.reset)
	fprintf("%sdocker compose -p %q -f %q logs -f devshardctl%s\n\n", p.cmd, project, c, p.reset)

	fprintf("%s# Follow all four devshardd-testenv replicas together — live tail%s\n", p.comment, p.reset)
	fprintf("%sdocker compose -p %q -f %q logs -f devshardd-testenv-0 devshardd-testenv-1 devshardd-testenv-2 devshardd-testenv-3%s\n\n",
		p.cmd, project, c, p.reset)

	fprintf("%s# Last 400 lines from devshardctl only (no follow) — good after a failure%s\n", p.comment, p.reset)
	fprintf("%sdocker compose -p %q -f %q logs --tail=400 devshardctl%s\n\n", p.cmd, project, c, p.reset)

	fprintf("%s# Last 400 lines from host replica 0 only (no follow)%s\n", p.comment, p.reset)
	fprintf("%sdocker compose -p %q -f %q logs --tail=400 devshardd-testenv-0%s\n\n", p.cmd, project, c, p.reset)

	fprintf("%s# Container status table for this project (running / exited / restarting)%s\n", p.comment, p.reset)
	fprintf("%sdocker compose -p %q -f %q ps -a%s\n", p.cmd, project, c, p.reset)
	fprintf("\n%s──────────────────────────────────────────────────────────────────%s\n\n", p.rule, p.reset)
}

// LogComposeDebugHints is a thin wrapper kept for callers that already pass *testing.T; it only
// marks the helper frame for stack traces and prints to stderr via [PrintComposeDebugHints].
func LogComposeDebugHints(t *testing.T, workspaceRoot, project string) {
	t.Helper()
	PrintComposeDebugHints(workspaceRoot, project)
}

func composeServiceStates(ctx context.Context, workspaceRoot, project string) (map[string]string, error) {
	composeFile := filepath.Join(workspaceRoot, "docker-compose.yml")
	cmd := exec.CommandContext(ctx, "docker", "compose", "-f", composeFile, "-p", project, "ps", "-a", "--format", "{{.Service}}\t{{.State}}")
	cmd.Dir = workspaceRoot
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	st := make(map[string]string)
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		st[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
	}
	return st, nil
}

func composeLogsTail(ctx context.Context, workspaceRoot, project, service string, tailLines int) string {
	composeFile := filepath.Join(workspaceRoot, "docker-compose.yml")
	cmd := exec.CommandContext(ctx, "docker", "compose", "-f", composeFile, "-p", project,
		"logs", "--no-color", "--tail", strconv.Itoa(tailLines), service)
	cmd.Dir = workspaceRoot
	out, err := cmd.Output()
	if err != nil {
		return fmt.Sprintf("(logs unavailable: %v)", err)
	}
	return string(out)
}

// WaitCoreStackServicesRunningOrFail polls docker compose until devshardd-testenv-{0..3} and
// devshardctl are running. If any of those services is exited or dead, the test fails immediately
// with a recent log tail (see [LogComposeDebugHints] for manual follow-up).
func WaitCoreStackServicesRunningOrFail(t *testing.T, ctx context.Context, workspaceRoot, project string, deadline time.Time) {
	t.Helper()
	want := []string{
		"devshardd-testenv-0", "devshardd-testenv-1", "devshardd-testenv-2", "devshardd-testenv-3",
		"devshardctl",
	}
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			t.Fatalf("wait core stack services: %v", ctx.Err())
		}
		states, err := composeServiceStates(ctx, workspaceRoot, project)
		if err != nil {
			t.Logf("compose ps (retry): %v", err)
			time.Sleep(2 * time.Second)
			continue
		}
		blocked := false
		for _, svc := range want {
			raw, ok := states[svc]
			state := strings.ToLower(strings.TrimSpace(raw))
			if strings.HasPrefix(state, "exited") {
				state = "exited"
			}
			if !ok {
				blocked = true
				continue
			}
			switch state {
			case "exited", "dead":
				tail := composeLogsTail(ctx, workspaceRoot, project, svc, 200)
				t.Fatalf("compose service %q is %q (stack unhealthy). Last logs:\n%s", svc, raw, tail)
			case "running":
				// ok
			default:
				blocked = true
			}
		}
		if !blocked {
			t.Logf("compose core services running: %v", want)
			return
		}
		time.Sleep(2 * time.Second)
	}
	psCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	states, err := composeServiceStates(psCtx, workspaceRoot, project)
	if err != nil {
		t.Fatalf("deadline waiting for core compose services to run; final ps failed: %v", err)
	}
	t.Fatalf("deadline waiting for core compose services to run (got states=%v)", states)
}

// WaitHTTP_OK polls url until HTTP 200 or deadline.
func WaitHTTP_OK(t *testing.T, c *http.Client, url string, deadline time.Time, note string) {
	t.Helper()
	for time.Now().Before(deadline) {
		resp, err := c.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("deadline waiting for %s: %s", note, url)
}

// WaitHeightSyncPositive waits for /block/latest to report height > 0.
func WaitHeightSyncPositive(t *testing.T, c *http.Client, deadline time.Time) {
	t.Helper()
	base := time.Now()
	for time.Now().Before(deadline) {
		resp, err := c.Get("http://127.0.0.1:9100/block/latest")
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK && len(b) > 0 {
				// Loose check: any digit height in JSON avoids importing blockoracle here.
				if containsPositiveHeight(b) {
					t.Logf("height-sync ready after %s", time.Since(base).Round(time.Second))
					return
				}
			}
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatal("deadline: height-sync /block/latest never reported a positive height")
}

func containsPositiveHeight(b []byte) bool {
	// Matches "height":N or "Height":N with N > 0 (simple scan).
	s := string(b)
	for _, key := range []string{`"height":`, `"Height":`} {
		i := indexOf(s, key)
		if i < 0 {
			continue
		}
		j := i + len(key)
		for j < len(s) && (s[j] == ' ' || s[j] == '\t') {
			j++
		}
		if j < len(s) && s[j] >= '1' && s[j] <= '9' {
			return true
		}
	}
	return false
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// fetchHeightSyncLatestHeight reads height-sync GET /block/latest (0 if unavailable).
func fetchHeightSyncLatestHeight(t *testing.T, c *http.Client) int64 {
	t.Helper()
	resp, err := c.Get("http://127.0.0.1:9100/block/latest")
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		return 0
	}
	return parseJSONHeightField(b)
}

func parseJSONHeightField(b []byte) int64 {
	s := string(b)
	for _, key := range []string{`"height":`, `"Height":`} {
		i := indexOf(s, key)
		if i < 0 {
			continue
		}
		j := i + len(key)
		for j < len(s) && (s[j] == ' ' || s[j] == '\t') {
			j++
		}
		var n int64
		_, err := fmt.Sscanf(s[j:], "%d", &n)
		if err == nil {
			return n
		}
	}
	return 0
}

// waitHeightSyncFeedFreshAfterRestart waits until mock-chain produces a new header after
// height-sync is back (height increases), then StaleAfter so mockdapi consumers see it.
func waitHeightSyncFeedFreshAfterRestart(t *testing.T, c *http.Client) {
	t.Helper()
	h0 := fetchHeightSyncLatestHeight(t, c)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(400 * time.Millisecond)
		if h1 := fetchHeightSyncLatestHeight(t, c); h1 > h0 {
			t.Logf("height-sync feed fresh: mainnet height %d → %d", h0, h1)
			time.Sleep(mockdapiStaleAfter + time.Second)
			return
		}
	}
	t.Logf("height-sync height stuck at %d for 30s; sleeping MOCKDAPI_STALE_AFTER anyway", h0)
	time.Sleep(mockdapiStaleAfter + 3*time.Second)
}

// restartDevshardctlForOracleReconnect restarts only devshardctl so its mockdapi client
// re-subscribes to height-sync SSE. Session SQLite is preserved; host executors stay up.
func restartDevshardctlForOracleReconnect(t *testing.T, ws, project string, ctx context.Context, httpClient *http.Client) {
	t.Helper()
	since := time.Now()
	rctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := DockerCompose(rctx, ws, project, nil, nil, "restart", "devshardctl").Run(); err != nil {
		t.Fatalf("compose restart devshardctl: %v", err)
	}
	WaitHTTP_OK(t, httpClient, "http://127.0.0.1:8081/v1/status", time.Now().Add(2*time.Minute), "devshardctl /v1/status after restart")
	waitDevshardctlOracleLive(t, ws, project, since)
}

// waitDevshardctlOracleLive polls devshardctl logs until mockdapi reports a live aligned height.
func waitDevshardctlOracleLive(t *testing.T, ws, project string, since time.Time) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		for _, ln := range composeLogsSince(t, ws, project, "devshardctl", since, 400) {
			kv := parseLogPayloadFromLine(ln)
			if kv["msg"] != "heightsync: emit" || kv["direction"] != "request" {
				continue
			}
			la := strings.TrimSpace(kv["local_aligned"])
			if la != "" && la != "0" {
				t.Logf("devshardctl oracle live: local_aligned=%s mode=%s nonce=%s", la, kv["mode"], kv["nonce"])
				return
			}
			if strings.EqualFold(kv["mode"], "anchor") {
				h := strings.TrimSpace(kv["height"])
				if h != "" && h != "0" {
					t.Logf("devshardctl oracle live: anchor height=%s nonce=%s", h, kv["nonce"])
					return
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("timeout: devshardctl height-sync oracle not live after restart (see heightsync: emit / local_aligned)")
}
