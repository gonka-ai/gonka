package config

import (
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"devshard/cmd/gateway/filters"
	"devshard/user"
)

// Test flow:
//  1. Build the default config.
//  2. Assert `Validate` accepts it with no error.
func TestDefaultsAreValid(t *testing.T) {
	configuration := Defaults()
	if err := configuration.Validate(); err != nil {
		t.Fatalf("Defaults() must validate cleanly, got: %v", err)
	}
}

// Test flow:
//  1. Build the default config and a table of every documented default field paired with its expected value.
//  2. Assert each field matches the spec's documented default.
func TestDefaultsMatchSpec(t *testing.T) {
	configuration := Defaults()
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"Server.Port", configuration.Server.Port, int64(8080)},
		{"Chain.GRPCEndpoint", configuration.Chain.GRPCEndpoint, "localhost:9090"},
		{"Chain.PublicAPIBaseURL", configuration.Chain.PublicAPIBaseURL, "http://localhost:9000"},
		{"Tx.FeeDenom", configuration.Tx.FeeDenom, "ngonka"},
		{"Tx.FeeAmount", configuration.Tx.FeeAmount, int64(1_000_000)},
		{"Tx.GasLimit", configuration.Tx.GasLimit, int64(700_000)},
		{"Tx.PollIntervalMS", configuration.Tx.PollIntervalMS, int64(2_000)},
		{"Tx.PollTimeoutMS", configuration.Tx.PollTimeoutMS, int64(45_000)},
		{"Tx.CallTimeoutMS", configuration.Tx.CallTimeoutMS, int64(10_000)},
		{"Limits.DefaultMaxTokens", configuration.Limits.DefaultMaxTokens, int64(4096)},
		{"Limits.MaxTokensCap", configuration.Limits.MaxTokensCap, int64(4096)},
		{"Limits.Concurrency.MaxRequests", configuration.Limits.Concurrency.MaxRequests, int64(2_048)},
		{"Limits.Concurrency.RequestsPer10000Weight", configuration.Limits.Concurrency.RequestsPer10000Weight, 5.0},
		{"Limits.Concurrency.PoCRequestsPer10000Weight", configuration.Limits.Concurrency.PoCRequestsPer10000Weight, 10.0},
		{"Limits.MaxInputTokensInFlight", configuration.Limits.MaxInputTokensInFlight, int64(0)},
		{"Limits.AdmissionQueueWaitMS", configuration.Limits.AdmissionQueueWaitMS, int64(300_000)},
		{"Limits.MaxBufferedRequestBytesPerCaller", configuration.Limits.MaxBufferedRequestBytesPerCaller, int64(32 << 20)},
		{"Limits.MinClientReadBytesPerSecond", configuration.Limits.MinClientReadBytesPerSecond, int64(65_536)},
		{"Limits.FallbackMaxModelLen", configuration.Limits.FallbackMaxModelLen, int64(1_000_000)},
		{"Limits.HostWindows.Input.MinRequests", configuration.Limits.HostWindows.Input.MinRequests, int64(16)},
		{"Limits.HostWindows.Input.InitialRequests", configuration.Limits.HostWindows.Input.InitialRequests, int64(32)},
		{"Limits.HostWindows.Output.MinRequests", configuration.Limits.HostWindows.Output.MinRequests, int64(16)},
		{"Limits.HostWindows.Output.InitialRequests", configuration.Limits.HostWindows.Output.InitialRequests, int64(32)},
		{"Limits.Congestion.BetaSoft", configuration.Limits.Congestion.BetaSoft, 0.85},
		{"Limits.Congestion.BetaHard", configuration.Limits.Congestion.BetaHard, 0.70},
		{"Limits.Congestion.BetaSevere", configuration.Limits.Congestion.BetaSevere, 0.50},
		{"Limits.Congestion.BetaCross", configuration.Limits.Congestion.BetaCross, 0.90},
		{"Limits.Congestion.Slack", configuration.Limits.Congestion.Slack, 0.30},
		{"Limits.HostCutoff.AfterFailures", configuration.Limits.HostCutoff.AfterFailures, int64(3)},
		{"Limits.HostCutoff.BaseMS", configuration.Limits.HostCutoff.BaseMS, int64(5_000)},
		{"Limits.HostCutoff.MaxMS", configuration.Limits.HostCutoff.MaxMS, int64(60_000)},
		{"Modes.PoCMode", configuration.Modes.PoCMode, "relaxed"},
		{"Rotation.PrePoCBlocks", configuration.Rotation.PrePoCBlocks, int64(300)},
		{"Cache.ChatCacheMaxBytes", configuration.Cache.ChatCacheMaxBytes, int64(268_435_456)},
		{"Server.MaxConcurrentRuntimeBuilds", configuration.Server.MaxConcurrentRuntimeBuilds, int64(16)},
		{"Server.MaxConcurrentPromptCounts", configuration.Server.MaxConcurrentPromptCounts, max(int64(runtime.GOMAXPROCS(0))/processorsPerPromptCount, 1)},
		{"Server.StatsTokenizerEnabled", configuration.Server.StatsTokenizerEnabled, false},
		{"Server.SessionRetentionEpochs", configuration.Server.SessionRetentionEpochs, int64(2)},
		{"Stream.DrainTimeoutSeconds", configuration.Stream.DrainTimeoutSeconds, int64(2_400)},
		{"Stream.ClassifyMaxAttemptBytes", configuration.Stream.ClassifyMaxAttemptBytes, int64(1_048_576)},
		{"Stream.ClassifyMaxParticipantBytes", configuration.Stream.ClassifyMaxParticipantBytes, int64(10_485_760)},
		{"Stream.ClassifyMaxGlobalBytes", configuration.Stream.ClassifyMaxGlobalBytes, int64(104_857_600)},
		{"Perf.EWMAHalfLifeSeconds", configuration.Perf.EWMAHalfLifeSeconds, int64(600)},
		{"Perf.ConsecutiveFailThreshold", configuration.Perf.ConsecutiveFailThreshold, int64(5)},
		{"Perf.FailureRateThreshold", configuration.Perf.FailureRateThreshold, 0.15},
		{"Perf.FailureRateMinVolume", configuration.Perf.FailureRateMinVolume, 20.0},
		{"Perf.EjectionBaseSeconds", configuration.Perf.EjectionBaseSeconds, int64(30)},
		{"Perf.EjectionMaxSeconds", configuration.Perf.EjectionMaxSeconds, int64(60)},
		{"Perf.MaxEjectionFraction", configuration.Perf.MaxEjectionFraction, 0.5},
		{"Perf.MinAvailableHosts", configuration.Perf.MinAvailableHosts, int64(4)},
		{"Perf.HostStalenessSeconds", configuration.Perf.HostStalenessSeconds, int64(3_600)},
		{"Scheduler.MatchWaitMS", configuration.Scheduler.MatchWaitMS, int64(2_000)},
		{"Scheduler.MaxConsecutiveBurns", configuration.Scheduler.MaxConsecutiveBurns, int64(2)},
		{"Scheduler.MaxBurnsPerRequest", configuration.Scheduler.MaxBurnsPerRequest, int64(16)},
		{"Scheduler.DispatchQueueDepth", configuration.Scheduler.DispatchQueueDepth, int64(1_024)},
		{"Engine.ReceiptTimeoutMS", configuration.Engine.ReceiptTimeoutMS, int64(5_000)},
		{"Engine.FirstTokenFloorMS", configuration.Engine.FirstTokenFloorMS, int64(6_000)},
		{"Engine.InterChunkStallMS", configuration.Engine.InterChunkStallMS, int64(30_000)},
		{"Engine.FirstTokenCeilingMS", configuration.Engine.FirstTokenCeilingMS, int64(30_000)},
		{"Chain.SnapshotMaxAgeSeconds", configuration.Chain.SnapshotMaxAgeSeconds, int64(60)},
		{"Chain.VersionsPollIntervalSeconds", configuration.Chain.VersionsPollIntervalSeconds, int64(15)},
		{"Engine.LoserGraceMS", configuration.Engine.LoserGraceMS, int64(600_000)},
		{"Engine.MaxAttemptsPerRequest", configuration.Engine.MaxAttemptsPerRequest, int64(2)},
		{"Engine.MaxStartedAttemptsPerRequest", configuration.Engine.MaxStartedAttemptsPerRequest, int64(3)},
		{"Engine.HedgeFirstTokenFloorMS", configuration.Engine.HedgeFirstTokenFloorMS, int64(1_500)},
		{"Engine.MaxConcurrentTimeoutVotes", configuration.Engine.MaxConcurrentTimeoutVotes, int64(64)},
		{"TimeoutSweep.BudgetPerTick", configuration.TimeoutSweep.BudgetPerTick, int64(8)},
		{"TimeoutSweep.GraceSeconds", configuration.TimeoutSweep.GraceSeconds, int64(120)},
		{"HeightSync.Enabled", configuration.HeightSync.Enabled, true},
		{"HeightSync.RequireSeed", configuration.HeightSync.RequireSeed, true},
		{"HeightSync.QuietSettleDelayMS", configuration.HeightSync.QuietSettleDelayMS, user.DefaultQuietSettleDelay.Milliseconds()},
		{"NonceAccounting.RetentionEpochs", configuration.NonceAccounting.RetentionEpochs, int64(2)},
		{"NonceAccounting.Port", configuration.NonceAccounting.Port, int64(9091)},
	}
	for _, check := range checks {
		if check.got != check.want {
			t.Errorf("%s = %v, want %v", check.name, check.got, check.want)
		}
	}
}

// Test flow:
//  1. Break four unrelated fields in a default config at once (port, max tokens cap, host window initial requests, chain gRPC endpoint).
//  2. Call `Validate`.
//  3. Assert it returns an error naming every one of the four broken fields.
func TestValidateRejectsBrokenConfigAndNamesEveryProblem(t *testing.T) {
	configuration := Defaults()
	configuration.Server.Port = 0
	configuration.Limits.MaxTokensCap = 0
	configuration.Limits.HostWindows.Input.InitialRequests = 0
	configuration.Chain.GRPCEndpoint = "not-host-port"

	err := configuration.Validate()
	if err == nil {
		t.Fatal("Validate() on broken config: want error, got nil")
	}
	for _, fragment := range []string{"port", "max_tokens_cap", "host_input_window_initial_requests", "chain_grpc"} {
		if !strings.Contains(err.Error(), fragment) {
			t.Errorf("Validate() error %q does not mention %q", err.Error(), fragment)
		}
	}
}

// Test flow:
//  1. Table-driven: each case mutates a default config to break exactly one validation rule and names the message fragment the error must contain.
//  2. For each case, mutate a fresh default config and call `Validate`.
//  3. Assert it returns an error naming the case's own fragment, so a flipped comparison anywhere fails exactly one case.
func TestValidateCatchesEveryRuleBreach(t *testing.T) {
	testCases := []struct {
		name            string
		mutate          func(configuration *Config)
		messageFragment string
	}{
		{"port too low", func(c *Config) { c.Server.Port = 0 }, "port"},
		{"port too high", func(c *Config) { c.Server.Port = 65536 }, "port"},
		{"max_concurrent_runtime_builds too low", func(c *Config) { c.Server.MaxConcurrentRuntimeBuilds = 0 }, "max_concurrent_runtime_builds"},
		{"max_concurrent_prompt_counts too low", func(c *Config) { c.Server.MaxConcurrentPromptCounts = 0 }, "max_concurrent_prompt_counts"},
		{"session_retention_epochs negative", func(c *Config) { c.Server.SessionRetentionEpochs = -1 }, "session_retention_epochs"},
		{"quiet_settle_delay_ms too low", func(c *Config) { c.HeightSync.QuietSettleDelayMS = 0 }, "quiet_settle_delay_ms"},
		{"public_api not a url", func(c *Config) { c.Chain.PublicAPIBaseURL = "://not-a-url" }, "public_api"},
		{"chain_versions_poll_interval_seconds too low", func(c *Config) { c.Chain.VersionsPollIntervalSeconds = 0 }, "chain_versions_poll_interval_seconds"},
		{"chain_versions_poll_interval_seconds too high", func(c *Config) { c.Chain.VersionsPollIntervalSeconds = maxVersionsPollIntervalSeconds + 1 }, "chain_versions_poll_interval_seconds"},
		{"tx_fee_amount negative", func(c *Config) { c.Tx.FeeAmount = -1 }, "tx_fee_amount"},
		{"tx_gas_limit too low", func(c *Config) { c.Tx.GasLimit = 0 }, "tx_gas_limit"},
		{"tx_poll_interval_ms too low", func(c *Config) { c.Tx.PollIntervalMS = 0 }, "tx_poll_interval_ms"},
		{"tx_poll_timeout_ms below interval", func(c *Config) { c.Tx.PollTimeoutMS = 100 }, "tx_poll_timeout_ms"},
		{"tx_call_timeout_ms too low", func(c *Config) { c.Tx.CallTimeoutMS = 0 }, "tx_call_timeout_ms"},
		{"tx_call_timeout_ms above the poll timeout", func(c *Config) { c.Tx.CallTimeoutMS = c.Tx.PollTimeoutMS + 1 }, "tx_call_timeout_ms"},
		{"default_max_tokens too low", func(c *Config) { c.Limits.DefaultMaxTokens = 0 }, "default_max_tokens"},
		{"max_tokens_cap too low", func(c *Config) { c.Limits.MaxTokensCap = 0 }, "max_tokens_cap"},
		{"max_concurrent_requests negative", func(c *Config) { c.Limits.Concurrency.MaxRequests = -1 }, "max_concurrent_requests"},
		{"max_concurrent_requests_per_10000_weight negative", func(c *Config) { c.Limits.Concurrency.RequestsPer10000Weight = -1 }, "max_concurrent_requests_per_10000_weight"},
		{"poc_max_concurrent_requests_per_10000_weight negative", func(c *Config) { c.Limits.Concurrency.PoCRequestsPer10000Weight = -1 }, "poc_max_concurrent_requests_per_10000_weight"},
		{"max_concurrent_requests_per_10000_weight above the request cap", func(c *Config) { c.Limits.Concurrency.RequestsPer10000Weight = maxConcurrentRequests + 1 }, "max_concurrent_requests_per_10000_weight"},
		{"poc_max_concurrent_requests_per_10000_weight above the request cap", func(c *Config) { c.Limits.Concurrency.PoCRequestsPer10000Weight = maxConcurrentRequests + 1 }, "poc_max_concurrent_requests_per_10000_weight"},
		{"max_input_tokens_in_flight negative", func(c *Config) { c.Limits.MaxInputTokensInFlight = -1 }, "max_input_tokens_in_flight"},
		{"max_buffered_request_bytes below one body", func(c *Config) { c.Limits.MaxBufferedRequestBytes = filters.MaxBodyBytes - 1 }, "max_buffered_request_bytes"},
		{"max_buffered_request_bytes_per_caller below one body", func(c *Config) { c.Limits.MaxBufferedRequestBytesPerCaller = filters.MaxBodyBytes - 1 }, "max_buffered_request_bytes_per_caller"},
		{"admission_queue_wait_ms negative", func(c *Config) { c.Limits.AdmissionQueueWaitMS = -1 }, "admission_queue_wait_ms"},
		{"min_client_read_bytes_per_second too low", func(c *Config) { c.Limits.MinClientReadBytesPerSecond = 0 }, "min_client_read_bytes_per_second"},
		{"min_client_read_bytes_per_second too high", func(c *Config) { c.Limits.MinClientReadBytesPerSecond = maxMinClientReadBytesPerSecond + 1 }, "min_client_read_bytes_per_second"},
		{"host_input_window_min_requests too low", func(c *Config) { c.Limits.HostWindows.Input.MinRequests = 0 }, "host_input_window_min_requests"},
		{"host_input_window_initial_requests below min", func(c *Config) {
			c.Limits.HostWindows.Input.InitialRequests = c.Limits.HostWindows.Input.MinRequests - 1
		}, "host_input_window_initial_requests"},
		{"host_output_window_min_requests too low", func(c *Config) { c.Limits.HostWindows.Output.MinRequests = 0 }, "host_output_window_min_requests"},
		{"host_output_window_initial_requests below min", func(c *Config) {
			c.Limits.HostWindows.Output.InitialRequests = c.Limits.HostWindows.Output.MinRequests - 1
		}, "host_output_window_initial_requests"},
		{"fallback_max_model_len too low", func(c *Config) { c.Limits.FallbackMaxModelLen = 0 }, "fallback_max_model_len"},
		{"io_aimd_beta_soft never narrows", func(c *Config) { c.Limits.Congestion.BetaSoft = 1 }, "io_aimd_beta_soft"},
		{"io_aimd_beta_hard closes the window", func(c *Config) { c.Limits.Congestion.BetaHard = 0 }, "io_aimd_beta_hard"},
		{"io_aimd_beta_severe widens instead", func(c *Config) { c.Limits.Congestion.BetaSevere = 1.5 }, "io_aimd_beta_severe"},
		{"io_aimd_beta_cross negative", func(c *Config) { c.Limits.Congestion.BetaCross = -0.1 }, "io_aimd_beta_cross"},
		{"host_congestion_slack negative", func(c *Config) { c.Limits.Congestion.Slack = -0.1 }, "host_congestion_slack"},
		{"host_cutoff_after_failures too low", func(c *Config) { c.Limits.HostCutoff.AfterFailures = 0 }, "host_cutoff_after_failures"},
		{"host_cutoff_ms too low", func(c *Config) { c.Limits.HostCutoff.BaseMS = 0 }, "host_cutoff_ms"},
		{"host_cutoff_max_ms below base", func(c *Config) { c.Limits.HostCutoff.MaxMS = 1 }, "host_cutoff_max_ms"},
		{"host_cutoff_max_ms above perf ejection horizon", func(c *Config) { c.Limits.HostCutoff.MaxMS = c.Perf.EjectionMaxSeconds*1000 + 1 }, "dominant ejection authority"},
		{"access bad enum", func(c *Config) { c.Limits.Access = "bogus" }, "access"},
		{"admin_api_key too short to be safe", func(c *Config) { c.Server.AdminAPIKey = "short" }, "admin_api_key"},
		{"poc_mode bad enum", func(c *Config) { c.Modes.PoCMode = "bogus" }, "poc_mode"},
		{"rotation_pre_poc_blocks negative", func(c *Config) { c.Rotation.PrePoCBlocks = -1 }, "rotation_pre_poc_blocks"},
		{"chat_cache_max_bytes negative", func(c *Config) { c.Cache.ChatCacheMaxBytes = -1 }, "chat_cache_max_bytes"},
		{"capture_sample_rate negative", func(c *Config) { c.Capture.SampleRate = -0.1 }, "capture_sample_rate"},
		{"capture_sample_rate above one", func(c *Config) { c.Capture.SampleRate = 1.1 }, "capture_sample_rate"},
		{"capture_max_bytes uncapped", func(c *Config) { c.Capture.MaxBytes = 0 }, "capture_max_bytes"},
		{"drain_timeout_seconds too low", func(c *Config) { c.Stream.DrainTimeoutSeconds = 0 }, "drain_timeout_seconds"},
		{"classify_max_attempt_bytes too low", func(c *Config) { c.Stream.ClassifyMaxAttemptBytes = 0 }, "classify_max_attempt_bytes"},
		{"classify_max_participant_bytes below attempt", func(c *Config) { c.Stream.ClassifyMaxParticipantBytes = 100 }, "classify_max_participant_bytes"},
		{"classify_max_global_bytes below participant", func(c *Config) { c.Stream.ClassifyMaxGlobalBytes = 100 }, "classify_max_global_bytes"},
		{"perf_ewma_half_life_seconds too low", func(c *Config) { c.Perf.EWMAHalfLifeSeconds = 0 }, "perf_ewma_half_life_seconds"},
		{"perf_consecutive_fail_threshold too low", func(c *Config) { c.Perf.ConsecutiveFailThreshold = 0 }, "perf_consecutive_fail_threshold"},
		{"perf_failure_rate_threshold zero", func(c *Config) { c.Perf.FailureRateThreshold = 0 }, "perf_failure_rate_threshold"},
		{"perf_failure_rate_threshold above one", func(c *Config) { c.Perf.FailureRateThreshold = 1.5 }, "perf_failure_rate_threshold"},
		{"perf_failure_rate_min_volume too low", func(c *Config) { c.Perf.FailureRateMinVolume = 0 }, "perf_failure_rate_min_volume"},
		{"perf_ejection_base_seconds too low", func(c *Config) { c.Perf.EjectionBaseSeconds = 0 }, "perf_ejection_base_seconds"},
		{"perf_ejection_max_seconds below base", func(c *Config) { c.Perf.EjectionMaxSeconds = 1 }, "perf_ejection_max_seconds"},
		{"perf_max_ejection_fraction zero", func(c *Config) { c.Perf.MaxEjectionFraction = 0 }, "perf_max_ejection_fraction"},
		{"perf_max_ejection_fraction above one", func(c *Config) { c.Perf.MaxEjectionFraction = 1.5 }, "perf_max_ejection_fraction"},
		{"perf_min_available_hosts negative", func(c *Config) { c.Perf.MinAvailableHosts = -1 }, "perf_min_available_hosts"},
		{"perf_host_staleness_seconds too low", func(c *Config) { c.Perf.HostStalenessSeconds = 0 }, "perf_host_staleness_seconds"},
		{"nonce_accounting_retention_epochs negative", func(c *Config) { c.NonceAccounting.RetentionEpochs = -1 }, "nonce_accounting_retention_epochs"},
		{"nonce_accounting_port zero while the ledger is on", func(c *Config) {
			c.NonceAccounting.Enabled = true
			c.NonceAccounting.Port = 0
		}, "nonce_accounting_port"},
		{"nonce_accounting_port above the port range while the ledger is on", func(c *Config) {
			c.NonceAccounting.Enabled = true
			c.NonceAccounting.Port = 65_536
		}, "nonce_accounting_port"},
		{"match_wait_ms negative", func(c *Config) { c.Scheduler.MatchWaitMS = -1 }, "match_wait_ms"},
		{"match_wait_ms above ceiling", func(c *Config) { c.Scheduler.MatchWaitMS = 5_001 }, "match_wait_ms"},
		{"max_consecutive_burns negative", func(c *Config) { c.Scheduler.MaxConsecutiveBurns = -1 }, "max_consecutive_burns"},
		{"max_consecutive_burns above ceiling", func(c *Config) { c.Scheduler.MaxConsecutiveBurns = 1_001 }, "max_consecutive_burns"},
		{"max_burns_per_request negative", func(c *Config) { c.Scheduler.MaxBurnsPerRequest = -1 }, "max_burns_per_request"},
		{"max_burns_per_request above ceiling", func(c *Config) { c.Scheduler.MaxBurnsPerRequest = 1_001 }, "max_burns_per_request"},
		{"scheduler_dispatch_queue_depth zero", func(c *Config) { c.Scheduler.DispatchQueueDepth = 0 }, "scheduler_dispatch_queue_depth"},
		{"scheduler_dispatch_queue_depth above ceiling", func(c *Config) { c.Scheduler.DispatchQueueDepth = 65_537 }, "scheduler_dispatch_queue_depth"},
		{"engine_receipt_timeout_ms too low", func(c *Config) { c.Engine.ReceiptTimeoutMS = 0 }, "engine_receipt_timeout_ms"},
		{"engine_first_token_floor_ms too low", func(c *Config) { c.Engine.FirstTokenFloorMS = 0 }, "engine_first_token_floor_ms"},
		{"engine_inter_chunk_stall_ms too low", func(c *Config) { c.Engine.InterChunkStallMS = 0 }, "engine_inter_chunk_stall_ms"},
		{"engine_loser_grace_ms below inter-chunk stall", func(c *Config) { c.Engine.LoserGraceMS = c.Engine.InterChunkStallMS - 1 }, "engine_loser_grace_ms"},
		{"engine_hedge_first_token_floor_ms negative", func(c *Config) { c.Engine.HedgeFirstTokenFloorMS = -1 }, "engine_hedge_first_token_floor_ms"},
		{"engine_max_attempts_per_request negative", func(c *Config) { c.Engine.MaxAttemptsPerRequest = -1 }, "engine_max_attempts_per_request"},
		{"engine_max_started_attempts_per_request negative", func(c *Config) { c.Engine.MaxStartedAttemptsPerRequest = -1 }, "engine_max_started_attempts_per_request"},
		{"engine_max_started_attempts_per_request above ceiling", func(c *Config) { c.Engine.MaxStartedAttemptsPerRequest = 1_001 }, "engine_max_started_attempts_per_request"},
		{"engine_max_concurrent_timeout_votes negative", func(c *Config) { c.Engine.MaxConcurrentTimeoutVotes = -1 }, "engine_max_concurrent_timeout_votes"},
		{"chain_grpc without a port", func(c *Config) { c.Chain.GRPCEndpoint = "node.example" }, "chain_grpc"},
		{"chain_grpc as a URL", func(c *Config) { c.Chain.GRPCEndpoint = "http://node.example:9090" }, "chain_grpc"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			configuration := Defaults()
			testCase.mutate(&configuration)
			err := configuration.Validate()
			if err == nil {
				t.Fatalf("Validate() accepted a config broken by %q", testCase.name)
			}
			if !strings.Contains(err.Error(), testCase.messageFragment) {
				t.Fatalf("error %q does not mention %q", err.Error(), testCase.messageFragment)
			}
		})
	}
}

// Test flow:
//  1. Table-driven: build a default config with retention 0, once with the nonce ledger off and once on.
//  2. Assert `Validate` accepts both, since DEVSHARD_STATS_RETENTION_EPOCHS=0 is read as "keep every epoch".
func TestValidateAcceptsAnUnboundedRetention(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		configuration := Defaults()
		configuration.NonceAccounting.Enabled = enabled
		configuration.NonceAccounting.RetentionEpochs = 0

		if err := configuration.Validate(); err != nil {
			t.Fatalf("Validate() with the nonce ledger enabled=%v and retention 0 = %v, want nil", enabled, err)
		}
	}
}

// Test flow:
//  1. Build a default config with `MaxRequests` set to 0.
//  2. Assert `Validate` accepts it, since the limiter reads zero as "no static cap".
func TestValidateAcceptsAnUncappedConcurrency(t *testing.T) {
	configuration := Defaults()
	configuration.Limits.Concurrency.MaxRequests = 0
	if err := configuration.Validate(); err != nil {
		t.Fatalf("Validate() with max_concurrent_requests 0 = %v, want nil", err)
	}
}

// Test flow:
//  1. Build a default config whose default max tokens exceeds a model's own token cap.
//  2. Assert `Validate` accepts it: a request naming no budget takes the operator's default by design, and only an explicit ask is held to the cap.
func TestValidateAcceptsADefaultAboveTheCap(t *testing.T) {
	configuration := Defaults()
	configuration.Limits.DefaultMaxTokens = 10_000
	configuration.Limits.MaxTokensCap = 4_096
	if err := configuration.Validate(); err != nil {
		t.Fatalf("Validate() with a default above the cap = %v, want nil", err)
	}
}

// Test flow:
//  1. Build a default config whose per-caller body share equals the whole body budget.
//  2. Assert `Validate` accepts it.
func TestValidateAcceptsAPerCallerShareEqualToTheBodyBudget(t *testing.T) {
	configuration := Defaults()
	configuration.Limits.MaxBufferedRequestBytesPerCaller = configuration.Limits.MaxBufferedRequestBytes
	if err := configuration.Validate(); err != nil {
		t.Fatalf("Validate() with a share equal to the budget = %v, want nil", err)
	}
}

// Test flow:
//  1. Build a default config and lower the body budget to 16 MiB, between one body and the default 32 MiB share, leaving the share at its default.
//  2. Assert `Validate` accepts it, so an operator who lowers only the budget still boots.
func TestValidateAcceptsABodyBudgetBelowTheDefaultPerCallerShare(t *testing.T) {
	configuration := Defaults()
	configuration.Limits.MaxBufferedRequestBytes = 16 << 20
	if configuration.Limits.MaxBufferedRequestBytes >= configuration.Limits.MaxBufferedRequestBytesPerCaller {
		t.Fatalf("budget %d is not below the default share %d, so the test proves nothing", configuration.Limits.MaxBufferedRequestBytes, configuration.Limits.MaxBufferedRequestBytesPerCaller)
	}
	if err := configuration.Validate(); err != nil {
		t.Fatalf("Validate() with a budget below the default share = %v, want nil", err)
	}
}

// Test flow:
//  1. List every field name on the `Engine` struct via reflection.
//  2. Assert the field list matches exactly the documented set of live tunables, so a retired field (like the legacy per-input-token first-token lag or minimum-samples-for-decision) cannot reappear under a new name.
func TestEngineCarriesOnlyTheLiveTunables(t *testing.T) {
	engineType := reflect.TypeFor[Engine]()
	got := make([]string, 0, engineType.NumField())
	for index := range engineType.NumField() {
		got = append(got, engineType.Field(index).Name)
	}
	want := []string{
		"ReceiptTimeoutMS", "FirstTokenFloorMS", "FirstTokenCeilingMS", "InterChunkStallMS", "LoserGraceMS",
		"MaxAttemptsPerRequest", "MaxStartedAttemptsPerRequest", "MaxConcurrentTimeoutVotes", "HedgeFirstTokenFloorMS",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Engine fields = %v, want %v", got, want)
	}
}

// Test flow:
//  1. Assert `AdminEnabled` reports false for a `Server` with no admin key set.
//  2. Assert it reports true once an admin key is configured.
func TestAdminEnabledDistinguishesUnsetFromConfigured(t *testing.T) {
	if (Server{}).AdminEnabled() {
		t.Error("AdminEnabled() = true for an unset key; admin routes would authenticate an empty credential")
	}
	if !(Server{AdminAPIKey: "0123456789abcdef"}).AdminEnabled() {
		t.Error("AdminEnabled() = false for a configured key")
	}
}

// Test flow:
//  1. Resolve models with one access tier of their own, one empty block and none for a third, over a network access of admin_only.
//  2. Assert `For(model).Limits.Access` is the model's own tier when set and admin-only for every other model: a deployment starts with no entries, and an open default would hand every model to anonymous callers.
func TestAccessFailsClosed(t *testing.T) {
	open, keyed := ModelAccessOpen, ModelAccessAPIKey
	configuration := Defaults()
	configuration.Server.APIKeys = []string{"0123456789abcdef"}
	if err := configuration.ResolveModels(map[string]ModelSettings{"opened": {Access: &open}, "keyed": {Access: &keyed}, "empty": {}}); err != nil {
		t.Fatalf("ResolveModels() = %v", err)
	}
	for model, want := range map[string]string{"opened": ModelAccessOpen, "keyed": ModelAccessAPIKey, "empty": ModelAccessAdminOnly, "unnamed": ModelAccessAdminOnly} {
		if got := configuration.For(model).Limits.Access; got != want {
			t.Errorf("For(%q).Limits.Access = %q, want %q", model, got, want)
		}
	}
}

// Test flow:
//  1. Resolve models with two open ones listed out of order, an api_key one and an admin_only one, then ask about a config with no models and one whose network access is open.
//  2. Assert `OpenModels` names the two open models sorted, none for the empty config, and the all-models marker first for an open network access.
func TestOpenModelsNamesOnlyTheModelsAnyCallerReaches(t *testing.T) {
	open, keyed, closed := ModelAccessOpen, ModelAccessAPIKey, ModelAccessAdminOnly
	configuration := Defaults()
	configuration.Server.APIKeys = []string{"0123456789abcdef"}
	if err := configuration.ResolveModels(map[string]ModelSettings{"zeta": {Access: &open}, "keyed": {Access: &keyed}, "alpha": {Access: &open}, "closed": {Access: &closed}}); err != nil {
		t.Fatalf("ResolveModels() = %v", err)
	}
	if got := configuration.OpenModels(); !slices.Equal(got, []string{"alpha", "zeta"}) {
		t.Errorf("OpenModels() = %v, want [alpha zeta]", got)
	}
	empty := Defaults()
	if got := empty.OpenModels(); len(got) != 0 {
		t.Errorf("OpenModels() with no models = %v, want none", got)
	}
	networkOpen := Defaults()
	networkOpen.Limits.Access = ModelAccessOpen
	if got := networkOpen.OpenModels(); !slices.Equal(got, []string{AllModels}) {
		t.Errorf("OpenModels() with an open network access = %v, want [%s]", got, AllModels)
	}
}

// Test flow:
//  1. Resolve a config naming one model, with an empty block.
//  2. Assert `Offers` is true for the named model and false for any other.
func TestOffersNamesTheModelsOfTheModelsSetting(t *testing.T) {
	configuration := Defaults()
	if err := configuration.ResolveModels(map[string]ModelSettings{"model-a": {}}); err != nil {
		t.Fatalf("ResolveModels() = %v", err)
	}
	if !configuration.Offers("model-a") || configuration.Offers("model-b") {
		t.Fatalf("Offers = %t for model-a and %t for model-b, want true and false", configuration.Offers("model-a"), configuration.Offers("model-b"))
	}
}

// Test flow:
//  1. Build a default config with the admin API key cleared.
//  2. Assert `Validate` accepts it, since an unset admin key disables admin routes rather than being invalid.
func TestValidateAcceptsUnsetAdminKey(t *testing.T) {
	configuration := Defaults()
	configuration.Server.AdminAPIKey = ""
	if err := configuration.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil: an unset admin key disables admin routes rather than being invalid", err)
	}
}

// Test flow:
//  1. For each table case's operator pin in `max_model_len` and governance's --max-model-len for the model, call `Limits.ContextLength`.
//  2. Assert governance wins, the operator's pin fills in without a usable governed length, and a model neither names is unknown (0).
func TestContextLengthTakesGovernanceOverTheOperatorsPin(t *testing.T) {
	pinned := int64(262_144)
	pinnedLimits := Limits{MaxModelLen: &pinned}
	testCases := []struct {
		name     string
		limits   Limits
		governed uint64
		want     uint64
	}{
		{name: "governance_over_a_pin", limits: pinnedLimits, governed: 400_000, want: 400_000},
		{name: "governance_without_a_pin", limits: Limits{}, governed: 400_000, want: 400_000},
		{name: "pin_when_governance_names_none", limits: pinnedLimits, want: 262_144},
		{name: "pin_when_governance_names_past_what_a_window_could_hold", limits: pinnedLimits, governed: MaxContextTokens + 1, want: 262_144},
		{name: "neither_names_the_model"},
		{name: "governance_past_what_a_window_could_hold", governed: MaxContextTokens + 1},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := testCase.limits.ContextLength(testCase.governed); got != testCase.want {
				t.Fatalf("ContextLength = %d, want %d", got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Resolve models with one that sets its own max_tokens_cap and one with an empty block, over a network cap of 4096.
//  2. Ask `For(model).Limits.OutputTokenCap` for each model.
//  3. Assert the model's own cap wins, and the network cap answers for an empty block and for an unnamed model.
func TestOutputTokenCapTakesTheModelsOwnCapElseTheNetworkOne(t *testing.T) {
	t.Parallel()
	own := int64(512)
	configuration := Defaults()
	configuration.Limits.MaxTokensCap = 4096
	if err := configuration.ResolveModels(map[string]ModelSettings{"capped": {MaxTokensCap: &own}, "inheriting": {}}); err != nil {
		t.Fatalf("ResolveModels() = %v", err)
	}
	for model, want := range map[string]uint64{"capped": 512, "inheriting": 4096, "unlisted": 4096} {
		if got := configuration.For(model).Limits.OutputTokenCap(); got != want {
			t.Errorf("OutputTokenCap(%q) = %d, want %d", model, got, want)
		}
	}
}

// Test flow:
//  1. Build a default config with the dispatch queue depth at each edge of its range, 1 and 65 536.
//  2. Assert `Validate` accepts both.
func TestValidateAcceptsTheDispatchQueueDepthEdges(t *testing.T) {
	for _, depth := range []int64{1, maxDispatchQueueDepth} {
		configuration := Defaults()
		configuration.Scheduler.DispatchQueueDepth = depth

		if err := configuration.Validate(); err != nil {
			t.Fatalf("Validate() with scheduler_dispatch_queue_depth %d = %v, want nil", depth, err)
		}
	}
}
