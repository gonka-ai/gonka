package testenv_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type airConfigSpec struct {
	file          string
	wantRoot      string
	wantPackage   string
	wantBinary    string
	expectDlv     bool
	wantDlvPort   string
	packageDir    string
	debounceDelay int
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
		file:          ".air.mock-dapi.toml",
		wantRoot:      "/workspace/devshard",
		wantPackage:   "./testenv/cmd/mockdapi",
		wantBinary:    "/tmp/air/mock-dapi/mockdapi",
		packageDir:    "testenv/cmd/mockdapi",
		debounceDelay: 500,
	},
	{
		file:          ".air.mock-dapi.debug.toml",
		wantRoot:      "/workspace/devshard",
		wantPackage:   "./testenv/cmd/mockdapi",
		wantBinary:    "/tmp/air/mock-dapi/mockdapi",
		expectDlv:     true,
		wantDlvPort:   ":2346",
		packageDir:    "testenv/cmd/mockdapi",
		debounceDelay: 500,
	},
	{
		file:          ".air.mock-openai.toml",
		wantRoot:      "/workspace/devshard",
		wantPackage:   "./testenv/cmd/mockopenai",
		wantBinary:    "/tmp/air/mock-openai/mockopenai",
		packageDir:    "testenv/cmd/mockopenai",
		debounceDelay: 500,
	},
	{
		file:          ".air.mock-openai.debug.toml",
		wantRoot:      "/workspace/devshard",
		wantPackage:   "./testenv/cmd/mockopenai",
		wantBinary:    "/tmp/air/mock-openai/mockopenai",
		expectDlv:     true,
		wantDlvPort:   ":2347",
		packageDir:    "testenv/cmd/mockopenai",
		debounceDelay: 500,
	},
}

type airCfg struct {
	root        string
	tmpDir      string
	buildCmd    string
	buildBin    string
	delay       int
	killDelay   string
	stopOnError string
	includeExts []string
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

func TestDevOverlay_AirConfigsReferenceRealPackages(t *testing.T) {
	root := devshardRootFromTestenv(t)
	for _, spec := range airConfigSpecs {
		spec := spec
		t.Run(spec.file, func(t *testing.T) {
			mainGo := filepath.Join(root, spec.packageDir, "main.go")
			if _, err := os.Stat(mainGo); err != nil {
				t.Fatalf("%s: missing %s: %v", spec.file, mainGo, err)
			}
		})
	}
}

func TestDevOverlay_AirConfigsStaticContract(t *testing.T) {
	for _, spec := range airConfigSpecs {
		spec := spec
		t.Run(spec.file, func(t *testing.T) {
			cfg := parseAirConfig(t, readAirConfig(t, spec.file))
			if cfg.root != spec.wantRoot {
				t.Errorf("root = %q, want %q", cfg.root, spec.wantRoot)
			}
			if !strings.Contains(cfg.buildCmd, spec.wantPackage) {
				t.Errorf("build cmd %q missing package %q", cfg.buildCmd, spec.wantPackage)
			}
			if spec.expectDlv {
				if !strings.Contains(cfg.buildCmd, `-gcflags 'all=-N -l'`) {
					t.Errorf("debug build missing gcflags")
				}
				if !strings.Contains(cfg.buildBin, "dlv exec") {
					t.Errorf("debug bin missing dlv exec")
				}
				if !strings.Contains(cfg.buildBin, "--listen="+spec.wantDlvPort) {
					t.Errorf("debug bin missing listen %s", spec.wantDlvPort)
				}
			} else if cfg.buildBin != spec.wantBinary {
				t.Errorf("bin = %q, want %q", cfg.buildBin, spec.wantBinary)
			}
		})
	}
}

type composeOverlay struct {
	Services map[string]struct {
		Image       string   `yaml:"image"`
		Command     []string `yaml:"command"`
		WorkingDir  string   `yaml:"working_dir"`
		Ports       []string `yaml:"ports"`
		CapAdd      []string `yaml:"cap_add"`
		SecurityOpt []string `yaml:"security_opt"`
		Volumes     []string `yaml:"volumes"`
		Build       struct {
			Context    string `yaml:"context"`
			Dockerfile string `yaml:"dockerfile"`
		} `yaml:"build"`
	} `yaml:"services"`
	Volumes map[string]struct{} `yaml:"volumes"`
}

func TestDevOverlay_ComposeDevMockChain(t *testing.T) {
	body, err := os.ReadFile(testenvPath(t, "docker-compose.dev.yml"))
	if err != nil {
		t.Fatalf("read overlay: %v", err)
	}
	var overlay composeOverlay
	if err := yaml.Unmarshal(body, &overlay); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	svc, ok := overlay.Services["mock-chain"]
	if !ok {
		t.Fatal("mock-chain service missing from dev overlay")
	}
	if svc.Build.Dockerfile != "devshard/testenv/Dockerfile.dev" {
		t.Errorf("dockerfile = %q", svc.Build.Dockerfile)
	}
	if svc.WorkingDir != "/workspace/devshard" {
		t.Errorf("working_dir = %q", svc.WorkingDir)
	}
	if len(svc.Command) != 2 || !strings.HasSuffix(svc.Command[1], ".air.mock-chain.debug.toml") {
		t.Errorf("command = %v", svc.Command)
	}
	found2345 := false
	for _, p := range svc.Ports {
		if p == "2345:2345" {
			found2345 = true
		}
	}
	if !found2345 {
		t.Errorf("ports = %v, want 2345:2345", svc.Ports)
	}
	if _, ok := overlay.Volumes["gomodcache"]; !ok {
		t.Error("missing gomodcache volume")
	}
	if _, ok := overlay.Volumes["gobuildcache"]; !ok {
		t.Error("missing gobuildcache volume")
	}

	svcDapi, ok := overlay.Services["mock-dapi"]
	if !ok {
		t.Fatal("mock-dapi service missing from dev overlay")
	}
	if !strings.HasSuffix(svcDapi.Command[1], ".air.mock-dapi.debug.toml") {
		t.Errorf("mock-dapi command = %v", svcDapi.Command)
	}
	found2346 := false
	for _, p := range svcDapi.Ports {
		if p == "2346:2346" {
			found2346 = true
		}
	}
	if !found2346 {
		t.Errorf("mock-dapi ports = %v, want 2346:2346", svcDapi.Ports)
	}

	svcOpenAI, ok := overlay.Services["mock-openai"]
	if !ok {
		t.Fatal("mock-openai service missing from dev overlay")
	}
	if !strings.HasSuffix(svcOpenAI.Command[1], ".air.mock-openai.debug.toml") {
		t.Errorf("mock-openai command = %v", svcOpenAI.Command)
	}
	found2347 := false
	for _, p := range svcOpenAI.Ports {
		if p == "2347:2347" {
			found2347 = true
		}
	}
	if !found2347 {
		t.Errorf("mock-openai ports = %v, want 2347:2347", svcOpenAI.Ports)
	}
}

func TestDevOverlay_VSCodeLaunchJSON_MatchesDlvPorts(t *testing.T) {
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
	got, ok := byName["Attach: mock-chain"]
	if !ok {
		t.Fatal("launch.json missing configuration \"Attach: mock-chain\"")
	}
	if got != 2345 {
		t.Errorf("Attach: mock-chain port = %d, want 2345", got)
	}
	gotDapi, ok := byName["Attach: mock-dapi"]
	if !ok {
		t.Fatal("launch.json missing configuration \"Attach: mock-dapi\"")
	}
	if gotDapi != 2346 {
		t.Errorf("Attach: mock-dapi port = %d, want 2346", gotDapi)
	}
}
