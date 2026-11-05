package env

import (
	"errors"
	"strings"
	"testing"
)

// Test flow:
//  1. Clear the port variable to empty (`t.Setenv` treats empty as unset) and set its retired GATEWAY_ spelling.
//  2. Call `Load`.
//  3. Assert `Port` comes back nil.
func TestLoadReturnsNilForUnsetVariables(t *testing.T) {
	t.Setenv("DEVSHARD_PORT", "")
	t.Setenv("GATEWAY_PORT", "9191")

	values, err := Load()
	if err != nil {
		t.Fatalf("Load() with clean environment: unexpected error: %v", err)
	}
	if values.Port != nil {
		t.Fatalf("Port = %v, want nil for unset variable", *values.Port)
	}
}

// Test flow:
//  1. Set one environment variable of each type `Load` parses: int, bool, float.
//  2. Call `Load`.
//  3. Assert each parsed field matches its set value.
func TestLoadParsesTypedValues(t *testing.T) {
	t.Setenv("DEVSHARD_PORT", "9191")
	t.Setenv("DEVSHARD_ESCROW_ROTATION_ENABLED", "true")
	t.Setenv("DEVSHARD_GATEWAY_DISABLED", "true")
	t.Setenv("DEVSHARD_TX_FEE_AMOUNT", "500")
	t.Setenv("GATEWAY_PERF_EWMA_HALFLIFE_SECONDS", "900")
	t.Setenv("GATEWAY_CAPTURE_SAMPLE_RATE", "0.25")
	t.Setenv("GATEWAY_CAPTURE_MAX_BYTES", "4096")

	values, err := Load()
	if err != nil {
		t.Fatalf("Load(): unexpected error: %v", err)
	}
	if values.Port == nil || *values.Port != 9191 {
		t.Fatalf("Port = %v, want 9191", values.Port)
	}
	if values.RotationEnabled == nil || *values.RotationEnabled != true {
		t.Fatalf("RotationEnabled = %v, want true", values.RotationEnabled)
	}
	if values.Disabled == nil || *values.Disabled != true {
		t.Fatalf("Disabled = %v, want true", values.Disabled)
	}
	if values.TxFeeAmount == nil || *values.TxFeeAmount != 500 {
		t.Fatalf("TxFeeAmount = %v, want 500", values.TxFeeAmount)
	}
	if values.PerfEWMAHalfLifeSeconds == nil || *values.PerfEWMAHalfLifeSeconds != 900 {
		t.Fatalf("PerfEWMAHalfLifeSeconds = %v, want 900", values.PerfEWMAHalfLifeSeconds)
	}
	if values.CaptureSampleRate == nil || *values.CaptureSampleRate != 0.25 {
		t.Fatalf("CaptureSampleRate = %v, want 0.25", values.CaptureSampleRate)
	}
	if values.CaptureMaxBytes == nil || *values.CaptureMaxBytes != 4096 {
		t.Fatalf("CaptureMaxBytes = %v, want 4096", values.CaptureMaxBytes)
	}
}

// Test flow:
//  1. Set a string variable padded with whitespace and an int variable containing only whitespace.
//  2. Call `Load`.
//  3. Assert the string value is trimmed and the blank int variable loads as unset (nil).
func TestLoadWhitespaceIsTrimmedAndEmptyMeansUnset(t *testing.T) {
	t.Setenv("DEVSHARD_GATEWAY_DISABLED_MESSAGE", "  gateway paused  ")
	t.Setenv("DEVSHARD_TX_GAS_LIMIT", "   ")

	values, err := Load()
	if err != nil {
		t.Fatalf("Load(): unexpected error: %v", err)
	}
	if values.DisabledMessage == nil || *values.DisabledMessage != "gateway paused" {
		t.Fatalf("DisabledMessage = %v, want trimmed \"gateway paused\"", values.DisabledMessage)
	}
	if values.TxGasLimit != nil {
		t.Fatalf("TxGasLimit = %v, want nil for blank value", *values.TxGasLimit)
	}
}

// Test flow:
//  1. Set four gateway-only variables of different types to malformed values.
//  2. Call `Load`.
//  3. Assert it returns an error naming all four variables, since errors must accumulate rather than stop at the first.
func TestLoadRejectsMalformedGatewayValuesWithVariableName(t *testing.T) {
	t.Setenv("GATEWAY_MATCH_WAIT_MS", "not-a-number")
	t.Setenv("GATEWAY_WARM_NEW_ESCROWS", "maybe")
	t.Setenv("GATEWAY_CAPTURE_SAMPLE_RATE", "half")
	t.Setenv("GATEWAY_FORCE_UPSTREAM_STREAMING", "on")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() with malformed values: want error, got nil")
	}
	message := err.Error()
	for _, name := range []string{"GATEWAY_MATCH_WAIT_MS", "GATEWAY_WARM_NEW_ESCROWS", "GATEWAY_CAPTURE_SAMPLE_RATE", "GATEWAY_FORCE_UPSTREAM_STREAMING"} {
		if !strings.Contains(message, name) {
			t.Fatalf("error %q does not name %s (errors must accumulate, not stop at first)", message, name)
		}
	}
}

// Test flow:
//  1. Set deployed variables to values that are ignored or read as "use the default": unparsable, zero, or negative.
//  2. Call `Load`.
//  3. Assert `Load` succeeds and leaves each field unset, so the gateway's default applies as it always has.
func TestAnUnusableValueIsIgnoredNotRefused(t *testing.T) {
	for name, value := range map[string]string{
		"DEVSHARD_PORT":                                    "eighty",
		"DEVSHARD_GATEWAY_DISABLED":                        "maybe",
		"DEVSHARD_TX_FEE_AMOUNT":                           "0",
		"DEVSHARD_TX_POLL_INTERVAL_MS":                     "-5",
		"DEVSHARD_STATS_SNAPSHOT_SECONDS":                  "0",
		"DEVSHARD_STATS_RETENTION_EPOCHS":                  "-1",
		"DEVSHARD_CHAT_CACHE_MAX_BYTES":                    "0",
		"DEVSHARD_POC_REQUEST_MODE":                        "aggressive",
		"DEVSHARD_GATEWAY_HOST_PING_INTERVAL":              "fifteen",
		"DEVSHARD_GATEWAY_HOST_PING_TIMEOUT":               "0s",
		"GATEWAY_DEFAULT_MAX_TOKENS":                       "0",
		"GATEWAY_MAX_TOKENS_CAP":                           "0",
		"GATEWAY_MAX_CONCURRENT_REQUESTS_PER_10000_WEIGHT": "0",
		"GATEWAY_MAX_INPUT_TOKENS_IN_FLIGHT":               "-1",
		"DEVSHARD_ESCROW_ROTATION_PRE_POC_BLOCKS":          "-300",
		"GATEWAY_MAX_CONCURRENT_REQUESTS":                  "lots",
		"DEVSHARD_MAX_CONCURRENT_RUNTIME_BUILDS":           "0",
		"DEVSHARD_MAX_CONCURRENT_PROMPT_COUNTS":            "0",
		"DEVSHARD_STATS_TOKENIZER":                         "maybe",
		"DEVSHARD_SESSION_RETENTION_EPOCHS":                "-1",
		"DEVSHARD_QUIET_SETTLE_DELAY":                      "soon",
		"DEVSHARD_ENABLE_CONTENTION_PROFILES":              "maybe",
	} {
		t.Setenv(name, value)
	}

	values, err := Load()
	if err != nil {
		t.Fatalf("Load() = %v, want the unusable values left unset", err)
	}
	for name, isSet := range map[string]bool{
		"Port":                                values.Port != nil,
		"Disabled":                            values.Disabled != nil,
		"TxFeeAmount":                         values.TxFeeAmount != nil,
		"TxPollIntervalMS":                    values.TxPollIntervalMS != nil,
		"NonceAccountingSnapshotSeconds":      values.NonceAccountingSnapshotSeconds != nil,
		"NonceAccountingRetentionEpochs":      values.NonceAccountingRetentionEpochs != nil,
		"ChatCacheMaxBytes":                   values.ChatCacheMaxBytes != nil,
		"PoCMode":                             values.PoCMode != nil,
		"HostPingIntervalMS":                  values.HostPingIntervalMS != nil,
		"HostPingTimeoutMS":                   values.HostPingTimeoutMS != nil,
		"DefaultMaxTokens":                    values.DefaultMaxTokens != nil,
		"MaxTokensCap":                        values.MaxTokensCap != nil,
		"MaxConcurrentRequestsPer10000Weight": values.MaxConcurrentRequestsPer10000Weight != nil,
		"MaxInputTokensInFlight":              values.MaxInputTokensInFlight != nil,
		"RotationPrePoCBlocks":                values.RotationPrePoCBlocks != nil,
		"MaxConcurrentRequests":               values.MaxConcurrentRequests != nil,
		"MaxConcurrentRuntimeBuilds":          values.MaxConcurrentRuntimeBuilds != nil,
		"MaxConcurrentPromptCounts":           values.MaxConcurrentPromptCounts != nil,
		"StatsTokenizerEnabled":               values.StatsTokenizerEnabled != nil,
		"SessionRetentionEpochs":              values.SessionRetentionEpochs != nil,
		"HeightSyncQuietSettleDelayMS":        values.HeightSyncQuietSettleDelayMS != nil,
		"ContentionProfiles":                  values.ContentionProfiles != nil,
	} {
		if isSet {
			t.Errorf("%s is set, want unset so the default applies", name)
		}
	}
}

// Test flow:
//  1. Set the renamed gateway spellings the gateway used before it went back to the deployed names, and clear the names it reads now.
//  2. Call `Load`.
//  3. Assert none of them is read.
func TestTheRenamedGatewaySpellingsAreNotRead(t *testing.T) {
	for _, name := range []string{
		"GATEWAY_PORT", "GATEWAY_STORAGE_DIR", "GATEWAY_ESCROWS_JSON", "GATEWAY_CHAIN_GRPC",
		"GATEWAY_POC_MODE", "GATEWAY_ROTATION_ENABLED", "GATEWAY_ACCOUNTING_ENABLED", "GATEWAY_HOST_PING_INTERVAL_MS",
	} {
		t.Setenv(name, "1")
	}
	for _, name := range []string{
		"DEVSHARD_PORT", "DEVSHARD_STORAGE_DIR", "DEVSHARDS_JSON", "DEVSHARD_CHAIN_GRPC", "NODE_GRPC_URL",
		"DEVSHARD_POC_REQUEST_MODE", "DEVSHARD_ESCROW_ROTATION_ENABLED", "DEVSHARD_STATS_ENABLED", "DEVSHARD_GATEWAY_HOST_PING_INTERVAL",
	} {
		t.Setenv(name, "")
	}

	values, err := Load()
	if err != nil {
		t.Fatalf("Load() = %v, want nil", err)
	}
	if values.Port != nil || values.StorageDir != nil || values.DevshardsJSON != nil || values.ChainGRPC != nil ||
		values.PoCMode != nil || values.RotationEnabled != nil || values.NonceAccountingEnabled != nil || values.HostPingIntervalMS != nil {
		t.Fatalf("a renamed GATEWAY_ spelling was read: %+v", values)
	}
}

// Test flow:
//  1. Set every host-ping and height-sync variable, in the deployed spellings (durations, on/off/yes).
//  2. Call `Load`.
//  3. Assert each loads, the durations converted to milliseconds.
func TestTheHostPingAndHeightSyncVariablesAreReadUnderTheirDeployedNames(t *testing.T) {
	t.Setenv("DEVSHARD_GATEWAY_HOST_PING_DISABLED", "yes")
	t.Setenv("DEVSHARD_GATEWAY_HOST_PING_INTERVAL", "15s")
	t.Setenv("DEVSHARD_GATEWAY_HOST_PING_TIMEOUT", "2s")
	t.Setenv("DEVSHARD_GATEWAY_HOST_PING_CONCURRENCY", "8")
	t.Setenv("DEVSHARD_HEIGHTSYNC_K", "12")
	t.Setenv("DEVSHARD_HEIGHTSYNC_SLOTS", "3")
	t.Setenv("DEVSHARD_REQUIRE_HEIGHT_SEED", "off")
	t.Setenv("DEVSHARD_GATEWAY_CHAIN_ORACLE", "on")

	values, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if values.HostPingDisabled == nil || !*values.HostPingDisabled {
		t.Errorf("HostPingDisabled = %v, want true from yes", values.HostPingDisabled)
	}
	if values.HostPingIntervalMS == nil || *values.HostPingIntervalMS != 15_000 {
		t.Errorf("HostPingIntervalMS = %v, want 15000 from the duration 15s", values.HostPingIntervalMS)
	}
	if values.HostPingTimeoutMS == nil || *values.HostPingTimeoutMS != 2_000 {
		t.Errorf("HostPingTimeoutMS = %v, want 2000 from the duration 2s", values.HostPingTimeoutMS)
	}
	if values.HostPingConcurrency == nil || *values.HostPingConcurrency != 8 {
		t.Errorf("HostPingConcurrency = %v, want 8", values.HostPingConcurrency)
	}
	if values.HeightSyncAnchorK == nil || *values.HeightSyncAnchorK != 12 {
		t.Errorf("HeightSyncAnchorK = %v, want 12", values.HeightSyncAnchorK)
	}
	if values.HeightSyncAnchorSlots == nil || *values.HeightSyncAnchorSlots != 3 {
		t.Errorf("HeightSyncAnchorSlots = %v, want 3", values.HeightSyncAnchorSlots)
	}
	if values.HeightSyncRequireSeed == nil || *values.HeightSyncRequireSeed {
		t.Errorf("HeightSyncRequireSeed = %v, want false from off", values.HeightSyncRequireSeed)
	}
	if values.HeightSyncChainOracle == nil || !*values.HeightSyncChainOracle {
		t.Errorf("HeightSyncChainOracle = %v, want true from on", values.HeightSyncChainOracle)
	}
}

// Test flow:
//  1. Table-driven: each case sets one of the two height-sync switches to a spelling, including ones outside the boolean grammar.
//  2. Call `Load`.
//  3. Assert the switch reads as follows: the chain oracle and the heartbeat are on only for true/1/on, and the seed gate is off only for 0/false/off/no.
func TestTheHeightSyncSwitchesKeepTheirExactSpellings(t *testing.T) {
	testCases := []struct {
		name     string
		variable string
		raw      string
		want     bool
	}{
		{name: "the chain oracle turns on for on", variable: "DEVSHARD_GATEWAY_CHAIN_ORACLE", raw: "ON", want: true},
		{name: "the chain oracle stays off for yes", variable: "DEVSHARD_GATEWAY_CHAIN_ORACLE", raw: "yes", want: false},
		{name: "the chain oracle stays off for an unknown value", variable: "DEVSHARD_GATEWAY_CHAIN_ORACLE", raw: "maybe", want: false},
		{name: "the seed gate turns off for no", variable: "DEVSHARD_REQUIRE_HEIGHT_SEED", raw: "no", want: false},
		{name: "the seed gate stays on for f", variable: "DEVSHARD_REQUIRE_HEIGHT_SEED", raw: "f", want: true},
		{name: "the seed gate stays on for an unknown value", variable: "DEVSHARD_REQUIRE_HEIGHT_SEED", raw: "maybe", want: true},
		{name: "the heartbeat turns on for 1", variable: "DEVSHARD_HEIGHTSYNC_HEARTBEAT", raw: "1", want: true},
		{name: "the heartbeat stays off for yes", variable: "DEVSHARD_HEIGHTSYNC_HEARTBEAT", raw: "yes", want: false},
		{name: "the heartbeat stays off for false", variable: "DEVSHARD_HEIGHTSYNC_HEARTBEAT", raw: "false", want: false},
	}
	switches := map[string]func(Values) *bool{
		"DEVSHARD_GATEWAY_CHAIN_ORACLE": func(values Values) *bool { return values.HeightSyncChainOracle },
		"DEVSHARD_REQUIRE_HEIGHT_SEED":  func(values Values) *bool { return values.HeightSyncRequireSeed },
		"DEVSHARD_HEIGHTSYNC_HEARTBEAT": func(values Values) *bool { return values.HeightSyncHeartbeat },
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv(testCase.variable, testCase.raw)

			values, err := Load()
			if err != nil {
				t.Fatalf("Load(): %v", err)
			}
			switchValue := switches[testCase.variable](values)
			if switchValue == nil || *switchValue != testCase.want {
				t.Fatalf("%s=%s read as %v, want %v", testCase.variable, testCase.raw, switchValue, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Set the PoC mode in a mixed-case spelling.
//  2. Call `Load`.
//  3. Assert it loads lower-cased.
func TestThePoCModeIsReadCaseInsensitively(t *testing.T) {
	t.Setenv("DEVSHARD_POC_REQUEST_MODE", " Relaxed ")

	values, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if values.PoCMode == nil || *values.PoCMode != PoCModeRelaxed {
		t.Fatalf("PoCMode = %v, want %q", values.PoCMode, PoCModeRelaxed)
	}
}

// Test flow:
//  1. Subtest "the chain endpoints fall back to the node's variables": set only NODE_GRPC_URL and NODE_RPC_URL and assert both are read.
//  2. Subtest "the DEVSHARD_ name wins over the node's": set both spellings and assert the DEVSHARD_ one wins.
func TestTheChainEndpointsFallBackToTheNodeVariables(t *testing.T) {
	t.Run("the chain endpoints fall back to the node's variables", func(t *testing.T) {
		t.Setenv("NODE_GRPC_URL", "node:9090")
		t.Setenv("NODE_RPC_URL", "http://node:26657")

		values, err := Load()
		if err != nil {
			t.Fatalf("Load(): %v", err)
		}
		if values.ChainGRPC == nil || *values.ChainGRPC != "node:9090" {
			t.Errorf("ChainGRPC = %v, want node:9090 from NODE_GRPC_URL", values.ChainGRPC)
		}
		if values.ChainRPC == nil || *values.ChainRPC != "http://node:26657" {
			t.Errorf("ChainRPC = %v, want the value of NODE_RPC_URL", values.ChainRPC)
		}
	})

	t.Run("the DEVSHARD_ name wins over the node's", func(t *testing.T) {
		t.Setenv("NODE_GRPC_URL", "node:9090")
		t.Setenv("DEVSHARD_CHAIN_GRPC", "chain:9090")

		values, err := Load()
		if err != nil {
			t.Fatalf("Load(): %v", err)
		}
		if values.ChainGRPC == nil || *values.ChainGRPC != "chain:9090" {
			t.Errorf("ChainGRPC = %v, want chain:9090 from DEVSHARD_CHAIN_GRPC", values.ChainGRPC)
		}
	})
}

// Test flow:
//  1. Set the transaction, weight-model, input-token, runtime-build, prompt-count and stats-tokenizer variables under their deployed names.
//  2. Call `Load`.
//  3. Assert each loads with its set value.
func TestTheLimitsAndTransactionVariablesAreReadUnderTheirDeployedNames(t *testing.T) {
	t.Setenv("DEVSHARD_TX_FEE_DENOM", "ngonka")
	t.Setenv("DEVSHARD_TX_POLL_INTERVAL_MS", "250")
	t.Setenv("DEVSHARD_TX_POLL_TIMEOUT_MS", "9000")
	t.Setenv("GATEWAY_MAX_CONCURRENT_REQUESTS_PER_10000_WEIGHT", "7.5")
	t.Setenv("GATEWAY_POC_MAX_CONCURRENT_REQUESTS_PER_10000_WEIGHT", "12")
	t.Setenv("GATEWAY_MAX_INPUT_TOKENS_IN_FLIGHT", "0")
	t.Setenv("DEVSHARD_MAX_CONCURRENT_RUNTIME_BUILDS", "4")
	t.Setenv("DEVSHARD_MAX_CONCURRENT_PROMPT_COUNTS", "3")
	t.Setenv("DEVSHARD_STATS_TOKENIZER", "on")

	values, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if values.TxFeeDenom == nil || *values.TxFeeDenom != "ngonka" {
		t.Errorf("TxFeeDenom = %v, want ngonka", values.TxFeeDenom)
	}
	if values.TxPollIntervalMS == nil || *values.TxPollIntervalMS != 250 {
		t.Errorf("TxPollIntervalMS = %v, want 250", values.TxPollIntervalMS)
	}
	if values.TxPollTimeoutMS == nil || *values.TxPollTimeoutMS != 9000 {
		t.Errorf("TxPollTimeoutMS = %v, want 9000", values.TxPollTimeoutMS)
	}
	if values.MaxConcurrentRequestsPer10000Weight == nil || *values.MaxConcurrentRequestsPer10000Weight != 7.5 {
		t.Errorf("MaxConcurrentRequestsPer10000Weight = %v, want 7.5", values.MaxConcurrentRequestsPer10000Weight)
	}
	if values.PoCMaxConcurrentRequestsPer10000Weight == nil || *values.PoCMaxConcurrentRequestsPer10000Weight != 12 {
		t.Errorf("PoCMaxConcurrentRequestsPer10000Weight = %v, want 12", values.PoCMaxConcurrentRequestsPer10000Weight)
	}
	if values.MaxInputTokensInFlight == nil || *values.MaxInputTokensInFlight != 0 {
		t.Errorf("MaxInputTokensInFlight = %v, want 0", values.MaxInputTokensInFlight)
	}
	if values.MaxConcurrentRuntimeBuilds == nil || *values.MaxConcurrentRuntimeBuilds != 4 {
		t.Errorf("MaxConcurrentRuntimeBuilds = %v, want 4", values.MaxConcurrentRuntimeBuilds)
	}
	if values.MaxConcurrentPromptCounts == nil || *values.MaxConcurrentPromptCounts != 3 {
		t.Errorf("MaxConcurrentPromptCounts = %v, want 3", values.MaxConcurrentPromptCounts)
	}
	if values.StatsTokenizerEnabled == nil || !*values.StatsTokenizerEnabled {
		t.Errorf("StatsTokenizerEnabled = %v, want true", values.StatsTokenizerEnabled)
	}
}

// Test flow:
//  1. Set the request capture's enabled and directory variables under their deployed names.
//  2. Call `Load`.
//  3. Assert both load.
func TestTheRequestCaptureAnswersToItsDeployedNames(t *testing.T) {
	t.Setenv("DEVSHARD_REQUEST_CAPTURE_ENABLED", "true")
	t.Setenv("DEVSHARD_REQUEST_CAPTURE_DIR", "/captures")

	values, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if values.CaptureEnabled == nil || !*values.CaptureEnabled {
		t.Errorf("CaptureEnabled = %v, want true", values.CaptureEnabled)
	}
	if values.CaptureDir == nil || *values.CaptureDir != "/captures" {
		t.Errorf("CaptureDir = %v, want /captures", values.CaptureDir)
	}
}

// Test flow:
//  1. Set a private key variable with surrounding whitespace and read it via `PrivateKey`.
//  2. Assert the key comes back trimmed.
//  3. Look up a blank and an unset variable name.
//  4. Assert both return `ErrPrivateKeyMissing` and neither error embeds the key material.
func TestPrivateKeyReadsTheNamedVariableAndNamesOnlyTheVariableOnFailure(t *testing.T) {
	t.Setenv("DEVSHARD_KEY_A", "  deadbeef  ")
	key, err := PrivateKey("DEVSHARD_KEY_A")
	if err != nil || key != "deadbeef" {
		t.Fatalf("PrivateKey() = %q, %v, want the trimmed key", key, err)
	}

	for _, name := range []string{"", "DEVSHARD_KEY_UNSET"} {
		_, err := PrivateKey(name)
		if !errors.Is(err, ErrPrivateKeyMissing) {
			t.Fatalf("PrivateKey(%q) = %v, want ErrPrivateKeyMissing", name, err)
		}
		if strings.Contains(err.Error(), "deadbeef") {
			t.Fatalf("PrivateKey(%q) error embeds key material: %v", name, err)
		}
	}
}

// Test flow:
//  1. Set the key under the GATEWAY_ spelling of a DEVSHARD_ variable name.
//  2. Call `PrivateKey` with the DEVSHARD_ name.
//  3. Assert it returns `ErrPrivateKeyMissing`, since the key is read under the recorded name only.
func TestASigningKeyIsReadUnderItsRecordedNameOnly(t *testing.T) {
	t.Setenv("GATEWAY_PRIVATE_KEY", "deadbeef")

	if _, err := PrivateKey("DEVSHARD_PRIVATE_KEY"); !errors.Is(err, ErrPrivateKeyMissing) {
		t.Fatalf("PrivateKey() = %v, want ErrPrivateKeyMissing", err)
	}
}

// Test flow:
//  1. Set every nonce-accounting ledger variable under the `DEVSHARD_STATS_` prefix (the ledger is named "stats" in the variables).
//  2. Call `Load`.
//  3. Assert each field loads with its set value.
func TestTheAccountingLedgerAnswersToTheStatsNames(t *testing.T) {
	t.Setenv("DEVSHARD_STATS_ENABLED", "true")
	t.Setenv("DEVSHARD_STATS_PORT", "9292")
	t.Setenv("DEVSHARD_STATS_RETENTION_EPOCHS", "0")
	t.Setenv("DEVSHARD_STATS_SNAPSHOT_SECONDS", "120")

	values, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if values.NonceAccountingEnabled == nil || !*values.NonceAccountingEnabled {
		t.Errorf("NonceAccountingEnabled = %v, want true from DEVSHARD_STATS_ENABLED", values.NonceAccountingEnabled)
	}
	if values.NonceAccountingPort == nil || *values.NonceAccountingPort != 9292 {
		t.Errorf("NonceAccountingPort = %v, want 9292 from DEVSHARD_STATS_PORT", values.NonceAccountingPort)
	}
	if values.NonceAccountingRetentionEpochs == nil || *values.NonceAccountingRetentionEpochs != 0 {
		t.Errorf("NonceAccountingRetentionEpochs = %v, want 0 (keep every epoch) from DEVSHARD_STATS_RETENTION_EPOCHS", values.NonceAccountingRetentionEpochs)
	}
	if values.NonceAccountingSnapshotSeconds == nil || *values.NonceAccountingSnapshotSeconds != 120 {
		t.Errorf("NonceAccountingSnapshotSeconds = %v, want 120 from DEVSHARD_STATS_SNAPSHOT_SECONDS", values.NonceAccountingSnapshotSeconds)
	}
}

// Test flow:
//  1. Set the request-record retention hours and max-rows variables under the `GATEWAY_REQUESTS_` prefix.
//  2. Call `Load`.
//  3. Assert both fields load with their set values.
func TestLoadReadsTheRequestRecordRetentionUnderTheRequestsPrefix(t *testing.T) {
	t.Setenv("GATEWAY_REQUESTS_RETENTION_HOURS", "24")
	t.Setenv("GATEWAY_REQUESTS_RETENTION_MAX_ROWS", "500")

	values, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if values.AccountingRetentionHours == nil || *values.AccountingRetentionHours != 24 {
		t.Errorf("AccountingRetentionHours = %v, want 24", values.AccountingRetentionHours)
	}
	if values.AccountingRetentionMaxRows == nil || *values.AccountingRetentionMaxRows != 500 {
		t.Errorf("AccountingRetentionMaxRows = %v, want 500", values.AccountingRetentionMaxRows)
	}
}

// Test flow:
//  1. Set the escrow list under `DEVSHARDS_JSON`.
//  2. Call `Load`.
//  3. Assert the escrow list loads.
func TestTheEscrowListIsReadFromDevshardsJSON(t *testing.T) {
	t.Setenv("DEVSHARDS_JSON", `[{"id":"1"}]`)

	values, err := Load()
	if err != nil {
		t.Fatalf("Load() = %v, want nil", err)
	}
	if values.DevshardsJSON == nil || *values.DevshardsJSON != `[{"id":"1"}]` {
		t.Fatalf("escrow list = %v, want the value of DEVSHARDS_JSON", values.DevshardsJSON)
	}
}

// Test flow:
//  1. Set all five rotation environment variables (enabled, settlement enabled, previous-epoch settlement enabled, pre-PoC blocks, models JSON).
//  2. Call `Load`.
//  3. Assert every one of the five knobs loaded its own variable.
func TestEveryRotationKnobIsReachableFromTheEnvironment(t *testing.T) {
	t.Setenv("DEVSHARD_ESCROW_ROTATION_ENABLED", "true")
	t.Setenv("DEVSHARD_ESCROW_ROTATION_SETTLEMENT_ENABLED", "true")
	t.Setenv("DEVSHARD_ESCROW_ROTATION_PREVIOUS_EPOCH_SETTLEMENT_ENABLED", "true")
	t.Setenv("DEVSHARD_ESCROW_ROTATION_PRE_POC_BLOCKS", "42")
	t.Setenv("DEVSHARD_ESCROW_ROTATION_MODELS_JSON", "[]")

	values, err := Load()
	if err != nil {
		t.Fatalf("Load() = %v, want nil", err)
	}
	if values.RotationPrePoCBlocks == nil || *values.RotationPrePoCBlocks != 42 {
		t.Fatalf("RotationPrePoCBlocks = %v, want the value the environment set", values.RotationPrePoCBlocks)
	}
	if values.RotationEnabled == nil || values.RotationSettlementEnabled == nil || values.RotationPreviousEpochSettlementEnabled == nil || values.RotationModelsJSON == nil {
		t.Fatal("a rotation knob stopped reading its variable")
	}
}

// Test flow:
//  1. Set every engine timing, the chain snapshot max age and the chain versions poll interval environment variable.
//  2. Call `Load`.
//  3. Assert each field loads with its set value.
func TestLoadParsesEngineTimings(t *testing.T) {
	for name, value := range map[string]string{
		"GATEWAY_ENGINE_RECEIPT_TIMEOUT_MS":            "7000",
		"GATEWAY_ENGINE_FIRST_TOKEN_FLOOR_MS":          "1500",
		"GATEWAY_ENGINE_FIRST_TOKEN_CEILING_MS":        "25000",
		"GATEWAY_ENGINE_INTER_CHUNK_STALL_MS":          "45000",
		"GATEWAY_ENGINE_LOSER_GRACE_MS":                "120000",
		"GATEWAY_ENGINE_HEDGE_FIRST_TOKEN_FLOOR_MS":    "900",
		"GATEWAY_CHAIN_SNAPSHOT_MAX_AGE_SECONDS":       "45",
		"GATEWAY_CHAIN_VERSIONS_POLL_INTERVAL_SECONDS": "20",
	} {
		t.Setenv(name, value)
	}

	values, err := Load()
	if err != nil {
		t.Fatalf("Load(): unexpected error: %v", err)
	}

	for _, field := range []struct {
		name string
		got  *int64
		want int64
	}{
		{"GATEWAY_ENGINE_RECEIPT_TIMEOUT_MS", values.EngineReceiptTimeoutMS, 7000},
		{"GATEWAY_ENGINE_FIRST_TOKEN_FLOOR_MS", values.EngineFirstTokenFloorMS, 1500},
		{"GATEWAY_ENGINE_FIRST_TOKEN_CEILING_MS", values.EngineFirstTokenCeilingMS, 25000},
		{"GATEWAY_ENGINE_INTER_CHUNK_STALL_MS", values.EngineInterChunkStallMS, 45000},
		{"GATEWAY_ENGINE_LOSER_GRACE_MS", values.EngineLoserGraceMS, 120000},
		{"GATEWAY_ENGINE_HEDGE_FIRST_TOKEN_FLOOR_MS", values.EngineHedgeFirstTokenFloorMS, 900},
		{"GATEWAY_CHAIN_SNAPSHOT_MAX_AGE_SECONDS", values.ChainSnapshotMaxAgeSeconds, 45},
		{"GATEWAY_CHAIN_VERSIONS_POLL_INTERVAL_SECONDS", values.ChainVersionsPollIntervalSeconds, 20},
	} {
		if field.got == nil || *field.got != field.want {
			t.Errorf("%s = %v, want %d", field.name, field.got, field.want)
		}
	}
}

// Test flow:
//  1. Set the settle margin variable to 900.
//  2. Call `Load`.
//  3. Assert the value loads unchanged.
func TestTheSettleMarginReadsItsVariable(t *testing.T) {
	t.Setenv("GATEWAY_ROTATION_SETTLE_MARGIN_BLOCKS", "900")

	values, err := Load()
	if err != nil {
		t.Fatalf("Load() = %v, want nil", err)
	}
	if values.RotationSettleMarginBlocks == nil || *values.RotationSettleMarginBlocks != 900 {
		t.Fatalf("RotationSettleMarginBlocks = %v, want 900", values.RotationSettleMarginBlocks)
	}
}

// Test flow:
//  1. Set GATEWAY_LOGPROBS_OPTIMIZATION_OVERRIDE to off, then leave it unset.
//  2. Call `Load`.
//  3. Assert off reads as a forced false and unset reads as no override at all.
func TestTheLogprobsOptimizationOverrideReadsAsATriState(t *testing.T) {
	t.Setenv("GATEWAY_LOGPROBS_OPTIMIZATION_OVERRIDE", "false")
	forced, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if forced.LogprobsOptimization == nil || *forced.LogprobsOptimization {
		t.Fatalf("LogprobsOptimization = %v, want a forced false", forced.LogprobsOptimization)
	}

	t.Setenv("GATEWAY_LOGPROBS_OPTIMIZATION_OVERRIDE", "")
	unset, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if unset.LogprobsOptimization != nil {
		t.Fatalf("LogprobsOptimization = %v, want nil so the host decides", *unset.LogprobsOptimization)
	}
}

// Test flow:
//  1. Set GATEWAY_DISPATCH_QUEUE_DEPTH to a number, call `Load`, then set it to a word and call `Load` again.
//  2. Assert the number is read as the depth and the word is refused with an error naming the variable.
func TestTheDispatchQueueDepthIsReadStrictly(t *testing.T) {
	t.Setenv("GATEWAY_DISPATCH_QUEUE_DEPTH", "2048")
	values, err := Load()
	if err != nil || values.DispatchQueueDepth == nil || *values.DispatchQueueDepth != 2048 {
		t.Fatalf("Load() with GATEWAY_DISPATCH_QUEUE_DEPTH=2048 = (%v, %v), want 2048", values.DispatchQueueDepth, err)
	}

	t.Setenv("GATEWAY_DISPATCH_QUEUE_DEPTH", "deep")
	_, err = Load()

	if err == nil || !strings.Contains(err.Error(), "GATEWAY_DISPATCH_QUEUE_DEPTH") {
		t.Fatalf("Load() with GATEWAY_DISPATCH_QUEUE_DEPTH=deep = %v, want an error naming the variable", err)
	}
}
