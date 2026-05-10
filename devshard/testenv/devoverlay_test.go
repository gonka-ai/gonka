package testenv_test

// Phase 12 — pin the live-reload + dlv overlay contract without
// booting docker. Docker behaviour is out of reach for `go test`,
// but every mechanical knob the overlay relies on (air configs,
// compose services, dlv ports, air → compose service alignment) is
// static text and therefore checkable here.
//
// Failure modes this test catches and which used to cost real time:
//
//   - An air config referring to a Go package path that no longer
//     exists: air loops rebuilding with "package not found" until
//     someone reads the container logs carefully.
//   - docker-compose.dev.yml starting an `.air.X.debug.toml` that
//     opens dlv on one port while the compose `ports:` block
//     publishes a different port. The service never attaches in the
//     IDE and the mismatch is invisible from `docker inspect`.
//   - Renaming a host ID in gencompose (e.g. dropping -testenv-)
//     silently breaks the static overlay entries.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// airConfigSpec ties an .air.<svc>.toml on disk to the behaviour it
// encodes so the regex-based parser below has a typed target.
type airConfigSpec struct {
	file          string
	wantRoot      string
	wantPackage   string // path the build cmd must pass to `go build`
	wantBinary    string // /tmp/air/<svc>/<basename>
	expectDlv     bool   // debug variants wrap the binary in `dlv exec`
	wantDlvPort   string // literal `:2345` or `:${DLV_PORT:-2347}` fallback
	packageDir    string // on-disk location relative to devshard/
	debounceDelay int    // ms; all configs pin 500
}

var airConfigSpecs = []airConfigSpec{
	{
		file:          ".air.mock-chain.toml",
		wantRoot:      "/workspace/devshard",
		wantPackage:   "./testenv/cmd/mockchain",
		wantBinary:    "/tmp/air/mock-chain/mockchain",
		packageDir:    "testenv/cmd/mockchain",
		debounceDelay: 500,
	},
	{
		file:          ".air.mock-chain.debug.toml",
		wantRoot:      "/workspace/devshard",
		wantPackage:   "./testenv/cmd/mockchain",
		wantBinary:    "/tmp/air/mock-chain/mockchain",
		expectDlv:     true,
		wantDlvPort:   ":2345",
		packageDir:    "testenv/cmd/mockchain",
		debounceDelay: 500,
	},
	{
		file:          ".air.height-sync.toml",
		wantRoot:      "/workspace/devshard",
		wantPackage:   "./testenv/cmd/heightsyncd",
		wantBinary:    "/tmp/air/height-sync/heightsyncd",
		packageDir:    "testenv/cmd/heightsyncd",
		debounceDelay: 500,
	},
	{
		file:          ".air.height-sync.debug.toml",
		wantRoot:      "/workspace/devshard",
		wantPackage:   "./testenv/cmd/heightsyncd",
		wantBinary:    "/tmp/air/height-sync/heightsyncd",
		expectDlv:     true,
		wantDlvPort:   ":2346",
		packageDir:    "testenv/cmd/heightsyncd",
		debounceDelay: 500,
	},
	{
		file:          ".air.devshardd.toml",
		wantRoot:      "/workspace/devshard",
		wantPackage:   "./testenv/cmd/devshardd-testenv",
		wantBinary:    "/tmp/air/devshardd/devshardd-testenv",
		packageDir:    "testenv/cmd/devshardd-testenv",
		debounceDelay: 500,
	},
	{
		file:          ".air.devshardd.debug.toml",
		wantRoot:      "/workspace/devshard",
		wantPackage:   "./testenv/cmd/devshardd-testenv",
		wantBinary:    "/tmp/air/devshardd/devshardd-testenv",
		expectDlv:     true,
		// The devshardd debug config parametrises the port so extra
		// hosts can opt into dlv by setting DLV_PORT=2348, 2349, …
		// The compose overlay publishes 2347 for host-0 by default.
		wantDlvPort:   ":${DLV_PORT:-2347}",
		packageDir:    "testenv/cmd/devshardd-testenv",
		debounceDelay: 500,
	},
	{
		file:          ".air.devshardctl.toml",
		wantRoot:      "/workspace/devshard",
		wantPackage:   "./cmd/devshardctl",
		wantBinary:    "/tmp/air/devshardctl/devshardctl",
		packageDir:    "cmd/devshardctl",
		debounceDelay: 500,
	},
}

// airCfg is the minimal subset of .air.<svc>.toml the test cares
// about. Kept line-oriented because the module has no TOML parser
// and every field is a single literal on its own line.
type airCfg struct {
	root         string
	tmpDir       string
	buildCmd     string
	buildBin     string
	delay        int
	killDelay    string
	stopOnError  string
	includeExts  []string
	exclDirCount int
}

var (
	reRoot        = regexp.MustCompile(`(?m)^root\s*=\s*"([^"]+)"`)
	reTmp         = regexp.MustCompile(`(?m)^tmp_dir\s*=\s*"([^"]+)"`)
	reCmd         = regexp.MustCompile(`(?m)^\s*cmd\s*=\s*"([^"]+)"`)
	reBin         = regexp.MustCompile(`(?m)^\s*bin\s*=\s*"([^"]+)"`)
	reFullBin     = regexp.MustCompile(`(?m)^\s*full_bin\s*=\s*"([^"]*)"`)
	reIncludeExt  = regexp.MustCompile(`(?m)^\s*include_ext\s*=\s*\[([^\]]+)\]`)
	reExcludeDir  = regexp.MustCompile(`(?ms)^\s*exclude_dir\s*=\s*\[([^\]]+)\]`)
	reDelay       = regexp.MustCompile(`(?m)^\s*delay\s*=\s*(\d+)`)
	reKillDelay   = regexp.MustCompile(`(?m)^\s*kill_delay\s*=\s*"([^"]+)"`)
	reStopOnError = regexp.MustCompile(`(?m)^\s*stop_on_error\s*=\s*(\S+)`)
)

func parseAirConfig(t *testing.T, body string) airCfg {
	t.Helper()
	var c airCfg
	if m := reRoot.FindStringSubmatch(body); m != nil {
		c.root = m[1]
	}
	if m := reTmp.FindStringSubmatch(body); m != nil {
		c.tmpDir = m[1]
	}
	if m := reCmd.FindStringSubmatch(body); m != nil {
		c.buildCmd = m[1]
	}
	if m := reBin.FindStringSubmatch(body); m != nil {
		c.buildBin = m[1]
	}
	// Debug air configs use full_bin (dlv exec …) and omit bin; the static
	// contract still applies to that launch command line.
	if c.buildBin == "" {
		if m := reFullBin.FindStringSubmatch(body); m != nil {
			c.buildBin = m[1]
		}
	}
	if m := reIncludeExt.FindStringSubmatch(body); m != nil {
		for _, tok := range strings.Split(m[1], ",") {
			tok = strings.Trim(strings.TrimSpace(tok), `"`)
			if tok != "" {
				c.includeExts = append(c.includeExts, tok)
			}
		}
	}
	if m := reExcludeDir.FindStringSubmatch(body); m != nil {
		for _, tok := range strings.Split(m[1], ",") {
			tok = strings.Trim(strings.TrimSpace(tok), `"`)
			if tok != "" {
				c.exclDirCount++
			}
		}
	}
	if m := reDelay.FindStringSubmatch(body); m != nil {
		// atoi without error: the fixture is checked in and the
		// regex already guarantees digits.
		for _, r := range m[1] {
			c.delay = c.delay*10 + int(r-'0')
		}
	}
	if m := reKillDelay.FindStringSubmatch(body); m != nil {
		c.killDelay = m[1]
	}
	if m := reStopOnError.FindStringSubmatch(body); m != nil {
		c.stopOnError = m[1]
	}
	return c
}

func testenvPath(t *testing.T, name string) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Join(wd, name)
}

func devshardRootFromTestenv(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Dir(wd)
}

func readAirConfig(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(testenvPath(t, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(body)
}

func TestAirConfigs_ReferenceRealPackages(t *testing.T) {
	root := devshardRootFromTestenv(t)
	for _, spec := range airConfigSpecs {
		spec := spec
		t.Run(spec.file, func(t *testing.T) {
			mainGo := filepath.Join(root, spec.packageDir, "main.go")
			if _, err := os.Stat(mainGo); err != nil {
				t.Fatalf("%s: references %s but %s is missing: %v",
					spec.file, spec.wantPackage, mainGo, err)
			}
		})
	}
}

func TestAirConfigs_StaticContract(t *testing.T) {
	for _, spec := range airConfigSpecs {
		spec := spec
		t.Run(spec.file, func(t *testing.T) {
			cfg := parseAirConfig(t, readAirConfig(t, spec.file))

			if cfg.root != spec.wantRoot {
				t.Errorf("root = %q, want %q", cfg.root, spec.wantRoot)
			}
			if !strings.HasPrefix(cfg.tmpDir, "/tmp/air/") {
				t.Errorf("tmp_dir = %q, want /tmp/air/ prefix", cfg.tmpDir)
			}
			if cfg.delay != spec.debounceDelay {
				t.Errorf("delay = %d, want %d (matches subnet-testenv tuning)", cfg.delay, spec.debounceDelay)
			}
			if cfg.killDelay != "1s" {
				t.Errorf(`kill_delay = %q, want "1s"`, cfg.killDelay)
			}
			if cfg.stopOnError != "true" {
				t.Errorf("stop_on_error = %q, want true (dev loop must not silently run stale binary)", cfg.stopOnError)
			}

			// Include-ext pinning keeps YAML / proto edits from
			// being silently ignored — historically the easiest way
			// to "lose" a change in live-reload.
			haveGo, haveYaml := false, false
			for _, ext := range cfg.includeExts {
				if ext == "go" {
					haveGo = true
				}
				if ext == "yaml" {
					haveYaml = true
				}
			}
			if !haveGo || !haveYaml {
				t.Errorf("include_ext = %v, want at least [go yaml]", cfg.includeExts)
			}

			// Exclude list is non-empty — catches a regression
			// where we watch db/observability and triggered rebuild
			// storms on log writes.
			if cfg.exclDirCount == 0 {
				t.Errorf("exclude_dir empty; expected at least docs + observability")
			}

			// Build target must be the exact Go package we pin.
			if !strings.Contains(cfg.buildCmd, spec.wantPackage) {
				t.Errorf("build cmd %q does not reference package %q", cfg.buildCmd, spec.wantPackage)
			}

			// Debug variants add -gcflags='all=-N -l' so dlv can
			// set breakpoints. Forgetting this is the #1 source
			// of "my breakpoints aren't being hit" reports.
			if spec.expectDlv {
				if !strings.Contains(cfg.buildCmd, `-gcflags 'all=-N -l'`) {
					t.Errorf("debug build cmd %q missing -gcflags 'all=-N -l'", cfg.buildCmd)
				}
				if !strings.Contains(cfg.buildBin, "dlv exec") {
					t.Errorf("debug bin %q should wrap binary in `dlv exec`", cfg.buildBin)
				}
				if !strings.Contains(cfg.buildBin, "--accept-multiclient") {
					t.Errorf("debug bin %q missing --accept-multiclient (IDE reconnect breaks without it)", cfg.buildBin)
				}
				if !strings.Contains(cfg.buildBin, "--listen="+spec.wantDlvPort) {
					t.Errorf("debug bin %q missing --listen=%s", cfg.buildBin, spec.wantDlvPort)
				}
				if !strings.Contains(cfg.buildBin, spec.wantBinary) {
					t.Errorf("debug bin %q doesn't exec %q", cfg.buildBin, spec.wantBinary)
				}
			} else {
				if cfg.buildBin != spec.wantBinary {
					t.Errorf("bin = %q, want %q", cfg.buildBin, spec.wantBinary)
				}
				if strings.Contains(cfg.buildBin, "dlv") {
					t.Errorf("non-debug bin %q should not wrap in dlv", cfg.buildBin)
				}
			}
		})
	}
}

// composeOverlay pulls the minimum shape out of docker-compose.dev.yml
// required to verify dlv port alignment and air-config selection.
type composeOverlay struct {
	Services map[string]struct {
		Image      string   `yaml:"image"`
		Command    []string `yaml:"command"`
		WorkingDir string   `yaml:"working_dir"`
		Environment map[string]string `yaml:"environment"`
		Ports      []string `yaml:"ports"`
		CapAdd     []string `yaml:"cap_add"`
		SecurityOpt []string `yaml:"security_opt"`
		Volumes    []string `yaml:"volumes"`
		Build struct {
			Context    string `yaml:"context"`
			Dockerfile string `yaml:"dockerfile"`
		} `yaml:"build"`
	} `yaml:"services"`
}

func loadComposeOverlay(t *testing.T) composeOverlay {
	t.Helper()
	body, err := os.ReadFile(testenvPath(t, "docker-compose.dev.yml"))
	if err != nil {
		t.Fatalf("read docker-compose.dev.yml: %v", err)
	}
	var out composeOverlay
	if err := yaml.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal overlay: %v", err)
	}
	if len(out.Services) == 0 {
		t.Fatalf("docker-compose.dev.yml has no services — overlay is empty")
	}
	return out
}

// Compose overlay must use the shared dev image, bind-mount the repo,
// and hand every service an explicit air config. The dev loop has too
// many moving parts to let any of these drift quietly.
func TestDockerComposeDev_SharedBuild(t *testing.T) {
	overlay := loadComposeOverlay(t)

	for name, svc := range overlay.Services {
		svc := svc
		t.Run(name, func(t *testing.T) {
			if svc.Build.Dockerfile != "devshard/testenv/Dockerfile.dev" {
				t.Errorf("build.dockerfile = %q, want devshard/testenv/Dockerfile.dev",
					svc.Build.Dockerfile)
			}
			if svc.Build.Context != "../.." {
				t.Errorf("build.context = %q, want ../.. (repo root for Dockerfile.dev COPYs)",
					svc.Build.Context)
			}
			if svc.Image != "devshard-dev:latest" {
				t.Errorf("image = %q, want devshard-dev:latest", svc.Image)
			}
			// Must match `root` in every .air.*.toml (/workspace/devshard).
			// If cwd were .../testenv, `go build ./testenv/cmd/...` would look for .../testenv/testenv/cmd.
			if svc.WorkingDir != "/workspace/devshard" {
				t.Errorf("working_dir = %q, want /workspace/devshard", svc.WorkingDir)
			}
			if len(svc.Command) != 2 || svc.Command[0] != "-c" {
				t.Errorf("command = %v, want [\"-c\", \"<air-config>\"]", svc.Command)
			}
			if !strings.HasSuffix(svc.Command[1], ".toml") ||
				!strings.HasPrefix(svc.Command[1], "/workspace/devshard/testenv/.air.") {
				t.Errorf("command[1] = %q, want /workspace/devshard/testenv/.air.*.toml", svc.Command[1])
			}

			// Verify air config referenced by compose exists on disk.
			airName := strings.TrimPrefix(svc.Command[1], "/workspace/devshard/testenv/")
			if _, err := os.Stat(testenvPath(t, airName)); err != nil {
				t.Errorf("compose points at %q but %s is missing: %v",
					svc.Command[1], airName, err)
			}

			// Every service bind-mounts the repo root so edits land
			// instantly; the named caches keep cold-start reasonable.
			var sawWorkspace, sawGoMod, sawGoBuild bool
			for _, v := range svc.Volumes {
				switch {
				case strings.HasSuffix(v, ":/workspace"):
					sawWorkspace = true
				case strings.HasSuffix(v, ":/go/pkg/mod"):
					sawGoMod = true
				case strings.HasSuffix(v, ":/root/.cache/go-build"):
					sawGoBuild = true
				}
			}
			if !sawWorkspace {
				t.Errorf("service missing repo bind-mount to /workspace (volumes=%v)", svc.Volumes)
			}
			if !sawGoMod || !sawGoBuild {
				t.Errorf("service missing cache volumes gomodcache+gobuildcache (volumes=%v)", svc.Volumes)
			}
		})
	}
}

// Debug services open a dlv port and publish it — keep the port
// published in compose aligned with the port the air config told dlv
// to listen on. A silent mismatch was the #2 source of "my debugger
// won't attach" reports.
func TestDockerComposeDev_DlvPortsMatchAirConfigs(t *testing.T) {
	overlay := loadComposeOverlay(t)

	type debugExpectation struct {
		service string
		dlvPort string
		airName string
	}
	want := []debugExpectation{
		{"mock-chain", "2345", ".air.mock-chain.debug.toml"},
		{"height-sync", "2346", ".air.height-sync.debug.toml"},
		{"devshardd-testenv-0", "2347", ".air.devshardd.debug.toml"},
	}

	for _, exp := range want {
		exp := exp
		t.Run(exp.service, func(t *testing.T) {
			svc, ok := overlay.Services[exp.service]
			if !ok {
				t.Fatalf("service %q not declared in overlay", exp.service)
			}

			// 1. Compose publishes the dlv port.
			wantPort := exp.dlvPort + ":" + exp.dlvPort
			found := false
			for _, p := range svc.Ports {
				if p == wantPort {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("service %s ports = %v, want %s", exp.service, svc.Ports, wantPort)
			}

			// 2. SYS_PTRACE + seccomp:unconfined — dlv refuses to
			//    attach otherwise on Docker Desktop.
			foundPtrace := false
			for _, c := range svc.CapAdd {
				if c == "SYS_PTRACE" {
					foundPtrace = true
					break
				}
			}
			if !foundPtrace {
				t.Errorf("service %s missing cap_add SYS_PTRACE", exp.service)
			}
			foundSeccomp := false
			for _, s := range svc.SecurityOpt {
				if s == "seccomp:unconfined" {
					foundSeccomp = true
					break
				}
			}
			if !foundSeccomp {
				t.Errorf("service %s missing security_opt seccomp:unconfined", exp.service)
			}

			// 3. Compose starts the debug air config.
			if len(svc.Command) != 2 || !strings.HasSuffix(svc.Command[1], exp.airName) {
				t.Errorf("service %s command[1] = %q, want suffix %s", exp.service, svc.Command[1], exp.airName)
			}

			// 4. For devshardd, compose also injects DLV_PORT env
			//    var — the air config reads it via ${DLV_PORT:-2347}.
			if exp.service == "devshardd-testenv-0" {
				if svc.Environment["DLV_PORT"] != exp.dlvPort {
					t.Errorf("devshardd-testenv-0 DLV_PORT = %q, want %s",
						svc.Environment["DLV_PORT"], exp.dlvPort)
				}
			}
		})
	}
}

// vscode-launch.json ships the attach configurations operators paste
// into .vscode/launch.json. Pin the host/port mapping so a silent
// rename of a dlv port in docker-compose.dev.yml no longer breaks the
// IDE attach without loud CI signal.
func TestVSCodeLaunchJSON_MatchesDlvPorts(t *testing.T) {
	body, err := os.ReadFile(testenvPath(t, "vscode-launch.json"))
	if err != nil {
		t.Fatalf("read vscode-launch.json: %v", err)
	}
	var launch struct {
		Configurations []struct {
			Name string `json:"name"`
			Port int    `json:"port"`
		} `json:"configurations"`
	}
	if err := json.Unmarshal(body, &launch); err != nil {
		t.Fatalf("unmarshal launch.json: %v", err)
	}
	byName := map[string]int{}
	for _, c := range launch.Configurations {
		byName[c.Name] = c.Port
	}
	for name, port := range map[string]int{
		"Attach: mock-chain":            2345,
		"Attach: height-sync":           2346,
		"Attach: devshardd-testenv-0":   2347,
	} {
		if got, ok := byName[name]; !ok {
			t.Errorf("launch.json missing configuration %q", name)
		} else if got != port {
			t.Errorf("launch.json %q port = %d, want %d", name, got, port)
		}
	}
}
