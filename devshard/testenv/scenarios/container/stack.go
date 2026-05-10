//go:build testenvci

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
// validates, and saves. Runs before hooks such as [WithDevsharddSchedulerFromCopiedConfig].
func WithConfigMutate(fn func(*testing.T, *config.Config)) E2EWorkspaceOption {
	return func(o *e2eWorkspaceOpts) {
		o.mutateConfig = fn
	}
}

// WithDevsharddSchedulerFromCopiedConfig injects HEIGHT_SYNC_ANCHOR_PERIOD_NONCES and
// HEIGHT_SYNC_SYNC_TURN_SLOTS into each devshardd-testenv service in the workspace
// compose file, from height_sync.anchor_period_nonces and height_sync.sync_turn_slots
// in config.yaml (including edits from [WithConfigMutate]).
//
// devshardd-testenv applies these environment variables; it does not derive anchor
// cadence from the mounted config path by itself.
func WithDevsharddSchedulerFromCopiedConfig() E2EWorkspaceOption {
	return func(o *e2eWorkspaceOpts) {
		o.afterConfig = append(o.afterConfig, injectDevsharddSchedulerEnvFromWorkspaceConfig)
	}
}

func injectDevsharddSchedulerEnvFromWorkspaceConfig(t *testing.T, ws string) {
	t.Helper()
	cfgPath := filepath.Join(ws, "config.yaml")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load workspace config for scheduler env injection: %v", err)
	}
	k := cfg.HeightSync.AnchorPeriodNonces
	turn := cfg.HeightSync.SyncTurnSlots
	if k < 1 || turn < 1 {
		t.Fatalf("height_sync.anchor_period_nonces and sync_turn_slots must be positive after defaults (got k=%d turn=%d)", k, turn)
	}
	composePath := filepath.Join(ws, "docker-compose.yml")
	data, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatalf("read workspace docker-compose.yml: %v", err)
	}
	const needle = "      METRICS_PORT: \"9600\""
	if !strings.Contains(string(data), needle) {
		t.Fatalf("workspace compose missing %q (cannot inject scheduler env)", needle)
	}
	replacement := fmt.Sprintf(
		"      METRICS_PORT: \"9600\"\n      HEIGHT_SYNC_ANCHOR_PERIOD_NONCES: \"%d\"\n      HEIGHT_SYNC_SYNC_TURN_SLOTS: \"%d\"",
		k, turn,
	)
	out := strings.ReplaceAll(string(data), needle, replacement)
	if err := os.WriteFile(composePath, []byte(out), 0o644); err != nil {
		t.Fatalf("write workspace docker-compose.yml: %v", err)
	}
	t.Logf("injected devshardd HEIGHT_SYNC_ANCHOR_PERIOD_NONCES=%d HEIGHT_SYNC_SYNC_TURN_SLOTS=%d", k, turn)
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
// edited config.yaml; further options (for example [WithDevsharddSchedulerFromCopiedConfig])
// read that file and patch sibling artifacts such as docker-compose.yml.
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

// PruneStaleContainerE2EDockerStacks runs `docker compose down` for every project whose user-defined
// network still exists as containere2e*_testenv. All stacks share subnet 172.30.0.0/24 in compose; Docker
// rejects a second network with the same pool, so leftovers (crashed test, `logs -f` in another terminal)
// must be torn down before the next up.
func PruneStaleContainerE2EDockerStacks(t *testing.T, testenvRoot string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	ls := exec.CommandContext(ctx, "docker", "network", "ls", "--format", "{{.Name}}")
	out, err := ls.Output()
	if err != nil {
		t.Logf("prune e2e stacks: docker network ls: %v", err)
		return
	}
	for _, name := range strings.Split(string(out), "\n") {
		name = strings.TrimSpace(name)
		if name == "" || !strings.HasPrefix(name, "containere2e") || !strings.HasSuffix(name, "_testenv") {
			continue
		}
		proj := strings.TrimSuffix(name, "_testenv")
		dctx, dcancel := context.WithTimeout(context.Background(), 2*time.Minute)
		errDown := DockerCompose(dctx, testenvRoot, proj, nil, nil, "down", "--remove-orphans", "--timeout", "60").Run()
		dcancel()
		if errDown != nil {
			t.Logf("stale compose project %q: down failed: %v", proj, errDown)
			continue
		}
		t.Logf("tore down stale compose project %q", proj)
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
