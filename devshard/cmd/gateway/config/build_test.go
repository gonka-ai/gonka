package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"devshard/cmd/gateway/env"
)

func int64Pointer(value int64) *int64       { return &value }
func float64Pointer(value float64) *float64 { return &value }
func stringPointer(value string) *string    { return &value }
func boolPointer(value bool) *bool          { return &value }

// Test flow:
//  1. Build a config from environment values (port, capture sample rate, capture max bytes) and admin overrides (max tokens, disabled flag, per-weight concurrency) that mix env-only, override-only, and both-set fields.
//  2. Assert each built field takes the right source: an override beats the same env value, an env value beats an untouched default, and an override-only field takes the override.
func TestBuildAppliesPrecedenceDefaultsEnvOverrides(t *testing.T) {
	values := env.Values{
		Port:             int64Pointer(9000),
		DefaultMaxTokens: int64Pointer(2000),

		CaptureSampleRate: float64Pointer(0.1),
		CaptureMaxBytes:   int64Pointer(4096),
	}
	overrides := Overrides{
		DefaultMaxTokens:                    int64Pointer(1500),
		Disabled:                            boolPointer(true),
		MaxConcurrentRequestsPer10000Weight: float64Pointer(2.5),
	}

	configuration, err := Build(values, overrides)
	if err != nil {
		t.Fatalf("Build(): %v", err)
	}
	if configuration.Server.Port != 9000 {
		t.Errorf("Server.Port = %d, want env value 9000", configuration.Server.Port)
	}
	if configuration.Limits.DefaultMaxTokens != 1500 {
		t.Errorf("Limits.DefaultMaxTokens = %d, want override value 1500", configuration.Limits.DefaultMaxTokens)
	}
	if !configuration.Modes.Disabled {
		t.Error("Modes.Disabled = false, want override value true")
	}
	if configuration.Limits.MaxTokensCap != 4096 {
		t.Errorf("Limits.MaxTokensCap = %d, want untouched default 4096", configuration.Limits.MaxTokensCap)
	}
	if configuration.Limits.Concurrency.RequestsPer10000Weight != 2.5 {
		t.Errorf("Limits.Concurrency.RequestsPer10000Weight = %v, want override value 2.5", configuration.Limits.Concurrency.RequestsPer10000Weight)
	}
	if configuration.Capture.SampleRate != 0.1 {
		t.Errorf("Capture.SampleRate = %v, want env value 0.1", configuration.Capture.SampleRate)
	}
	if configuration.Capture.MaxBytes != 4096 {
		t.Errorf("Capture.MaxBytes = %d, want env value 4096", configuration.Capture.MaxBytes)
	}
}

// Test flow:
//  1. Build a config from an `APIKeys` environment value listing keys separated by commas with extra whitespace and an empty entry.
//  2. Assert the built server's API keys are the three keys, trimmed and with the blank entry dropped.
func TestBuildSplitsAPIKeys(t *testing.T) {
	values := env.Values{APIKeys: stringPointer("key-one, key-two ,,key-three")}
	configuration, err := Build(values, Overrides{})
	if err != nil {
		t.Fatalf("Build(): %v", err)
	}
	keys := configuration.Server.APIKeys
	if len(keys) != 3 || keys[0] != "key-one" || keys[1] != "key-two" || keys[2] != "key-three" {
		t.Fatalf("Server.APIKeys = %v, want three trimmed keys", keys)
	}
}

// Test flow:
//  1. Build a config with `MaxTokensCap` set to 0 in the environment.
//  2. Assert `Build` returns an error naming `max_tokens_cap`.
func TestBuildRejectsInvalidMergedConfig(t *testing.T) {
	values := env.Values{MaxTokensCap: int64Pointer(0)}
	_, err := Build(values, Overrides{})
	if err == nil || !strings.Contains(err.Error(), "max_tokens_cap") {
		t.Fatalf("Build() with an unset cap: want max_tokens_cap validation error, got %v", err)
	}
}

// Test flow:
//  1. Build a config from an override whose `ModelLimits` map has one entry.
//  2. Mutate the source map's existing entry and add a new one.
//  3. Assert the built config's model limits are unaffected by either mutation, since `Build` clones the map rather than aliasing it.
func TestBuildClonesOverridesModelLimits(t *testing.T) {
	sourceModelLimits := map[string]ModelLimits{
		"model-a": {DefaultMaxTokens: 100, MaxTokensCap: 200},
	}
	overrides := Overrides{ModelLimits: sourceModelLimits}

	configuration, err := Build(env.Values{}, overrides)
	if err != nil {
		t.Fatalf("Build(): %v", err)
	}

	sourceModelLimits["model-a"] = ModelLimits{DefaultMaxTokens: 999, MaxTokensCap: 999}
	sourceModelLimits["model-b"] = ModelLimits{DefaultMaxTokens: 1, MaxTokensCap: 2}

	if got := configuration.Limits.ModelLimits["model-a"].DefaultMaxTokens; got != 100 {
		t.Fatalf("Limits.ModelLimits[model-a].DefaultMaxTokens = %d, want untouched 100 after mutating the source map (map was aliased, not cloned)", got)
	}
	if _, present := configuration.Limits.ModelLimits["model-b"]; present {
		t.Fatal("Limits.ModelLimits gained a key added to the source map after Build — map was aliased, not cloned")
	}
}

// deployedEnvTemplates are the operator-facing deploy files Build must accept without failing.
var deployedEnvTemplates = []string{
	"../../../../deploy/join/config.devshard.env.template",
	"../deploy/config.devshard-gateway.env.template",
}

// Test flow:
//  1. For each deployed env template file (`deployedEnvTemplates`), export every variable it sets the way a shell would, substituting placeholder secrets, then load and build a config from the real environment via `buildFromTemplate`.
//  2. Assert the gateway builds without error from every deployed env template file.
func TestBuildAcceptsTheShippedEnvTemplate(t *testing.T) {
	for _, path := range deployedEnvTemplates {
		t.Run(filepath.Base(filepath.Dir(path)), func(t *testing.T) {
			buildFromTemplate(t, path)
		})
	}
}

// templateSecrets substitutes the templates' placeholder keys so parsing them is not judged as a real secret.
var templateSecrets = map[string]string{
	"DEVSHARD_API_KEYS":      "sk-template-placeholder",
	"DEVSHARD_ADMIN_API_KEY": "sk-admin-template-placeholder",
}

func buildFromTemplate(t *testing.T, deployedEnvTemplate string) {
	t.Helper()
	template, err := os.ReadFile(deployedEnvTemplate)
	if err != nil {
		t.Fatalf("reading %s: %v", deployedEnvTemplate, err)
	}
	exported := 0
	for line := range strings.SplitSeq(string(template), "\n") {
		assignment, isExport := strings.CutPrefix(strings.TrimSpace(line), "export ")
		if !isExport {
			continue
		}
		name, value, hasValue := strings.Cut(assignment, "=")
		if !hasValue {
			t.Fatalf("malformed export line: %q", line)
		}
		exported++
		if placeholder, isSecret := templateSecrets[name]; isSecret {
			value = placeholder
		}
		t.Setenv(name, unquoteShellValue(value))
	}
	if exported == 0 {
		t.Fatalf("%s exports no variable, so this test asserts nothing", deployedEnvTemplate)
	}

	values, err := env.Load()
	if err != nil {
		t.Fatalf("env.Load() = %v, want nil", err)
	}
	if _, err := Build(values, Overrides{}); err != nil {
		t.Fatalf("Build() on %s = %v; the gateway refuses to boot on its own deploy file", deployedEnvTemplate, err)
	}
}

// Test flow:
//  1. Build a config with `ChainRPC` set to a custom CometBFT endpoint.
//  2. Assert the built chain config's RPC endpoint matches it, rather than the derived default.
func TestTheChainRPCFallbackIsConfigurable(t *testing.T) {
	configuration, err := Build(env.Values{ChainRPC: stringPointer("http://cometbft.internal:26657")}, Overrides{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if configuration.Chain.RPCEndpoint != "http://cometbft.internal:26657" {
		t.Fatalf("Chain.RPCEndpoint = %q, want the configured endpoint", configuration.Chain.RPCEndpoint)
	}
}

// Test flow:
//  1. Table-driven: each case configures forced upstream streaming from nothing, from the environment, from a runtime override, or from a runtime override overriding an environment value.
//  2. For each case, build a config.
//  3. Assert the forced-streaming flag matches the case's expectation, proving the default-true flag can be turned off, and back on, without a redeploy.
func TestForcedStreamingIsOnByDefaultAndTurnedOffWithoutARedeploy(t *testing.T) {
	tests := []struct {
		name      string
		values    env.Values
		overrides Overrides
		want      bool
	}{
		{name: "nothing configured", want: true},
		{name: "turned off by env", values: env.Values{ForceUpstreamStreaming: boolPointer(false)}, want: false},
		{name: "turned off at runtime", overrides: Overrides{ForceUpstreamStreaming: boolPointer(false)}, want: false},
		{
			name:      "turned back on at runtime over an env that turned it off",
			values:    env.Values{ForceUpstreamStreaming: boolPointer(false)},
			overrides: Overrides{ForceUpstreamStreaming: boolPointer(true)},
			want:      true,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			configuration, err := Build(testCase.values, testCase.overrides)
			if err != nil {
				t.Fatalf("Build(): %v", err)
			}
			if got := configuration.Limits.ForceUpstreamStreaming; got != testCase.want {
				t.Errorf("force_upstream_streaming = %v, want %v", got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Table-driven: each case configures the max-consecutive-burns run length from nothing, from the environment, from a runtime override, from a runtime override winning over the environment, or turned off entirely.
//  2. For each case, build a config.
//  3. Assert the scheduler's burn-run length matches the case's expectation.
func TestTheBurnRunIsReachableWithoutARedeploy(t *testing.T) {
	tests := []struct {
		name      string
		values    env.Values
		overrides Overrides
		want      int64
	}{
		{name: "nothing configured", want: 2},
		{name: "set by env", values: env.Values{MaxConsecutiveBurns: int64Pointer(3)}, want: 3},
		{name: "set at runtime", overrides: Overrides{MaxConsecutiveBurns: int64Pointer(3)}, want: 3},
		{
			name:      "runtime wins over env",
			values:    env.Values{MaxConsecutiveBurns: int64Pointer(3)},
			overrides: Overrides{MaxConsecutiveBurns: int64Pointer(12)},
			want:      12,
		},
		{name: "turned off at runtime", overrides: Overrides{MaxConsecutiveBurns: int64Pointer(0)}, want: 0},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			configuration, err := Build(testCase.values, testCase.overrides)
			if err != nil {
				t.Fatalf("Build(): %v", err)
			}
			if got := configuration.Scheduler.MaxConsecutiveBurns; got != testCase.want {
				t.Errorf("scheduler_max_consecutive_burns = %d, want %d", got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Table-driven: each case configures the failure-rate threshold and ejection-max-seconds from nothing, from the environment, from a runtime override, or from a runtime override winning over the environment.
//  2. For each case, build a config.
//  3. Assert the perf config's threshold and max-seconds match the case's expectation.
func TestEjectionThresholdsAreReachableWithoutARedeploy(t *testing.T) {
	tests := []struct {
		name          string
		values        env.Values
		overrides     Overrides
		wantRate      float64
		wantMaxSecond int64
	}{
		{name: "nothing configured", wantRate: 0.15, wantMaxSecond: 600},
		{
			name:          "set by env",
			values:        env.Values{PerfFailureRateThreshold: float64Pointer(0.5), PerfEjectionMaxSeconds: int64Pointer(180)},
			wantRate:      0.5,
			wantMaxSecond: 180,
		},
		{
			name:          "set at runtime",
			overrides:     Overrides{PerfFailureRateThreshold: float64Pointer(0.5), PerfEjectionMaxSeconds: int64Pointer(180)},
			wantRate:      0.5,
			wantMaxSecond: 180,
		},
		{
			name:          "runtime wins over env",
			values:        env.Values{PerfFailureRateThreshold: float64Pointer(0.5), PerfEjectionMaxSeconds: int64Pointer(180)},
			overrides:     Overrides{PerfFailureRateThreshold: float64Pointer(0.7), PerfEjectionMaxSeconds: int64Pointer(300)},
			wantRate:      0.7,
			wantMaxSecond: 300,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			configuration, err := Build(testCase.values, testCase.overrides)
			if err != nil {
				t.Fatalf("Build(): %v", err)
			}
			if got := configuration.Perf.FailureRateThreshold; got != testCase.wantRate {
				t.Errorf("perf_failure_rate_threshold = %v, want %v", got, testCase.wantRate)
			}
			if got := configuration.Perf.EjectionMaxSeconds; got != testCase.wantMaxSecond {
				t.Errorf("perf_ejection_max_seconds = %d, want %d", got, testCase.wantMaxSecond)
			}
		})
	}
}

func unquoteShellValue(value string) string {
	for _, quote := range []string{"'", `"`} {
		if len(value) >= 2 && strings.HasPrefix(value, quote) && strings.HasSuffix(value, quote) {
			return value[1 : len(value)-1]
		}
	}
	return value
}

// Test flow:
//  1. Table-driven: each case sets the weight-model limit, the PoC one, both, or neither, as a deployed env file would.
//  2. Build a config from those values.
//  3. Assert the PoC limit follows the plain one when only a non-default plain one is set, as a derived default, and is otherwise its own value or default.
func TestThePoCWeightLimitFollowsThePlainOneWhenOnlyThePlainOneIsSet(t *testing.T) {
	plainDefault := Defaults().Limits.Concurrency.RequestsPer10000Weight
	pocDefault := Defaults().Limits.Concurrency.PoCRequestsPer10000Weight
	testCases := []struct {
		name    string
		plain   *float64
		poc     *float64
		wantPoC float64
	}{
		{name: "neither set keeps the default", wantPoC: pocDefault},
		{name: "a plain limit alone carries over to PoC", plain: float64Pointer(7.5), wantPoC: 7.5},
		{name: "a plain limit at its default leaves the PoC default", plain: float64Pointer(plainDefault), wantPoC: pocDefault},
		{name: "an explicit PoC limit wins", plain: float64Pointer(7.5), poc: float64Pointer(12), wantPoC: 12},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			values := env.Values{MaxConcurrentRequestsPer10000Weight: testCase.plain, PoCMaxConcurrentRequestsPer10000Weight: testCase.poc}

			built, err := Build(values, Overrides{})

			if err != nil {
				t.Fatalf("Build() = %v", err)
			}
			if got := built.Limits.Concurrency.PoCRequestsPer10000Weight; got != testCase.wantPoC {
				t.Fatalf("PoC limit = %v, want %v", got, testCase.wantPoC)
			}
		})
	}
}

// Test flow:
//  1. Build a config with no settlement knobs, then one where the environment turns both settlement toggles on, then one where overrides turn them back off.
//  2. Assert both default off, the environment turns each on, and the overrides win over the environment.
func TestBothSettlementTogglesFollowTheEnvironmentAndTheOverrides(t *testing.T) {
	configuration, err := Build(env.Values{}, Overrides{})
	if err != nil || configuration.Rotation.SettlementEnabled || configuration.Rotation.PreviousEpochSettlementEnabled {
		t.Fatalf("Build() rotation = %+v, %v, want both settlement toggles off", configuration.Rotation, err)
	}
	enabled, disabled := true, false
	values := env.Values{RotationSettlementEnabled: &enabled, RotationPreviousEpochSettlementEnabled: &enabled}
	configuration, err = Build(values, Overrides{})
	if err != nil || !configuration.Rotation.SettlementEnabled || !configuration.Rotation.PreviousEpochSettlementEnabled {
		t.Fatalf("Build(environment on) rotation = %+v, %v, want both settlement toggles on", configuration.Rotation, err)
	}
	configuration, err = Build(values, Overrides{RotationSettlementEnabled: &disabled, RotationPreviousEpochSettlementEnabled: &disabled})
	if err != nil || configuration.Rotation.SettlementEnabled || configuration.Rotation.PreviousEpochSettlementEnabled {
		t.Fatalf("Build(overrides off) rotation = %+v, %v, want the overrides to turn both off", configuration.Rotation, err)
	}
}

// Test flow:
//  1. Build a config with no overrides, then one with the settle margin overridden to 900, then ones with 0 and -1.
//  2. Assert the default is 600, the override sets 900, and 0 and -1 are refused as ErrInvalid.
func TestTheSettleMarginDefaultsTo600BlocksAndRefusesLessThanOne(t *testing.T) {
	configuration, err := Build(env.Values{}, Overrides{})
	if err != nil || configuration.Rotation.SettleMarginBlocks != 600 {
		t.Fatalf("Build() settle margin = %d, %v, want 600, nil", configuration.Rotation.SettleMarginBlocks, err)
	}
	wider := int64(900)
	configuration, err = Build(env.Values{}, Overrides{RotationSettleMarginBlocks: &wider})
	if err != nil || configuration.Rotation.SettleMarginBlocks != 900 {
		t.Fatalf("Build(900) settle margin = %d, %v, want 900, nil", configuration.Rotation.SettleMarginBlocks, err)
	}
	for _, refused := range []int64{0, -1} {
		if _, err := Build(env.Values{}, Overrides{RotationSettleMarginBlocks: &refused}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Build(%d) = %v, want ErrInvalid", refused, err)
		}
	}
}

// Test flow:
//  1. Build a config with the dispatch queue depth set in the environment, with no overrides.
//  2. Assert the built scheduler carries that depth.
func TestTheDispatchQueueDepthComesFromTheEnvironment(t *testing.T) {
	t.Parallel()
	built, err := Build(env.Values{DispatchQueueDepth: int64Pointer(4_096)}, Overrides{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if built.Scheduler.DispatchQueueDepth != 4_096 {
		t.Fatalf("scheduler_dispatch_queue_depth = %d, want 4096 from the environment", built.Scheduler.DispatchQueueDepth)
	}
}
