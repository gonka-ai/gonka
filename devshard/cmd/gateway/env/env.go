// Package env is the single place the gateway reads environment variables. Load returns what is SET
// (nil pointer = unset); defaults belong to config, except LogFormat's. See README.md.
package env

import (
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"devshard/cmd/gateway/internal/logkey"
	"devshard/internal/boolvalue"
	"devshard/logging"
)

// Values mirrors every environment variable the gateway reads; nil = unset.
type Values struct {
	Port                       *int64
	StorageDir                 *string
	APIKeys                    *string
	AdminAPIKey                *string
	DevshardsJSON              *string
	MaxConcurrentRuntimeBuilds *int64
	MaxConcurrentPromptCounts  *int64
	StatsTokenizerEnabled      *bool
	SessionRetentionEpochs     *int64

	ChainGRPC        *string
	PublicAPI        *string
	ChainID          *string
	ChainRPC         *string
	TxFeeDenom       *string
	TxFeeAmount      *int64
	TxGasLimit       *int64
	TxPollIntervalMS *int64
	TxPollTimeoutMS  *int64

	DefaultMaxTokens                       *int64
	MaxTokensCap                           *int64
	MaxConcurrentRequests                  *int64
	MaxConcurrentRequestsPer10000Weight    *float64
	PoCMaxConcurrentRequestsPer10000Weight *float64
	MaxInputTokensInFlight                 *int64
	AdmissionQueueWaitMS                   *int64
	AdmissionQueuePerSlot                  *int64

	PoCMode             *string
	Disabled            *bool
	DisabledMessage     *string
	DisabledRedirectURL *string

	RotationEnabled                        *bool
	RotationPrePoCBlocks                   *int64
	MatchWaitMS                            *int64
	MaxConsecutiveBurns                    *int64
	DispatchQueueDepth                     *int64
	ForceUpstreamStreaming                 *bool
	LogprobsOptimization                   *bool
	MaxBufferedResponseBytes               *int64
	WarmNewEscrows                         *bool
	RotationSettlementEnabled              *bool
	RotationPreviousEpochSettlementEnabled *bool
	RotationModelsJSON                     *string
	RotationSettleMarginBlocks             *int64

	ChatCacheMaxBytes *int64

	AccountingRetentionHours   *int64
	AccountingRetentionMaxRows *int64

	NonceAccountingEnabled         *bool
	NonceAccountingPort            *int64
	NonceAccountingRetentionEpochs *int64
	NonceAccountingSnapshotSeconds *int64

	TimeoutSweepBudgetPerTick *int64
	TimeoutSweepGraceSeconds  *int64

	CaptureEnabled    *bool
	CaptureDir        *string
	CaptureSampleRate *float64
	CaptureMaxBytes   *int64

	PerfEWMAHalfLifeSeconds      *int64
	PerfConsecutiveFailThreshold *int64
	PerfFailureRateThreshold     *float64
	PerfFailureRateMinVolume     *float64
	PerfEjectionBaseSeconds      *int64
	PerfEjectionMaxSeconds       *int64
	PerfMaxEjectionFraction      *float64
	PerfMinAvailableHosts        *int64
	PerfHostStalenessSeconds     *int64

	HeightSyncEnabled            *bool
	HeightSyncRequireSeed        *bool
	HeightSyncChainOracle        *bool
	HeightSyncHeartbeat          *bool
	HeightSyncAnchorK            *int64
	HeightSyncAnchorSlots        *int64
	HeightSyncQuietSettleDelayMS *int64

	HostPingDisabled    *bool
	HostPingIntervalMS  *int64
	HostPingTimeoutMS   *int64
	HostPingConcurrency *int64

	ChainSnapshotMaxAgeSeconds       *int64
	ChainVersionsPollIntervalSeconds *int64

	EngineReceiptTimeoutMS    *int64
	EngineFirstTokenFloorMS   *int64
	EngineFirstTokenCeilingMS *int64
	EngineInterChunkStallMS   *int64
	EngineLoserGraceMS        *int64

	EngineMaxConcurrentTimeoutVotes *int64
	EngineHedgeFirstTokenFloorMS    *int64

	ContentionProfiles *bool
}

// See README.md, "The values it names".
const (
	PoCModeOff     = "off"
	PoCModeRelaxed = "relaxed"

	LogFormatJSON = "json"
	LogFormatText = "text"

	AllowPrivateAddressesVariable = "DEVSHARD_ALLOW_PRIVATE_ADDRESSES"
)

// ErrPrivateKeyMissing marks a devshard whose signing key the environment does not hold.
var ErrPrivateKeyMissing = errors.New("private key missing")

// LogFormat is read apart from Load because it must be applied before anything can log. See README.md, "The log format".
func LogFormat() string {
	raw := lookup("DEVSHARD_LOG_FORMAT")
	if raw == "" || strings.EqualFold(raw, LogFormatJSON) {
		return LogFormatJSON
	}
	return LogFormatText
}

// AllowPrivateAddresses is read apart from Load because the dial guard is armed before anything dials. See operations.md.
func AllowPrivateAddresses() bool {
	allowed, err := boolvalue.Parse(lookup(AllowPrivateAddressesVariable))
	return err == nil && allowed
}

// PrivateKey reads the key held by the named variable; errors and logs name the variable, never the value.
func PrivateKey(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("%w: no environment variable named", ErrPrivateKeyMissing)
	}
	if key := lookup(name); key != "" {
		return key, nil
	}
	return "", fmt.Errorf("%w: %s is unset", ErrPrivateKeyMissing, name)
}

func lookup(names ...string) string {
	for _, name := range names {
		if raw := strings.TrimSpace(os.Getenv(name)); raw != "" {
			return raw
		}
	}
	return ""
}

func ignoreUnusableValue(name, raw string) {
	logging.Warn("environment value ignored: it is not a usable value, so the default applies",
		logkey.Subsystem, "env", logkey.Recorded, name, logkey.Used, raw)
}

// Load reads every gateway environment variable, accumulating parse failures so none is reported alone.
func Load() (Values, error) {
	var values Values
	var problems []error

	readString := func(name string, target **string, fallbacks ...string) {
		raw := lookup(append([]string{name}, fallbacks...)...)
		if raw == "" {
			return
		}
		*target = &raw
	}

	readLenientInt := func(name string, minimum int64, target **int64) {
		raw := lookup(name)
		if raw == "" {
			return
		}
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < minimum {
			ignoreUnusableValue(name, raw)
			return
		}
		*target = &parsed
	}
	readLenientPositiveFloat := func(name string, target **float64) {
		raw := lookup(name)
		if raw == "" {
			return
		}
		parsed, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed <= 0 {
			ignoreUnusableValue(name, raw)
			return
		}
		*target = &parsed
	}
	readLenientBool := func(name string, target **bool) {
		raw := lookup(name)
		if raw == "" {
			return
		}
		parsed, err := boolvalue.Parse(raw)
		if err != nil {
			ignoreUnusableValue(name, raw)
			return
		}
		*target = &parsed
	}
	readDurationAsMilliseconds := func(name string, target **int64) {
		raw := lookup(name)
		if raw == "" {
			return
		}
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed.Milliseconds() <= 0 {
			ignoreUnusableValue(name, raw)
			return
		}
		milliseconds := parsed.Milliseconds()
		*target = &milliseconds
	}

	readStrictInt := func(name string, target **int64) {
		raw := lookup(name)
		if raw == "" {
			return
		}
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			problems = append(problems, fmt.Errorf("%s: %q is not an integer", name, raw))
			return
		}
		*target = &parsed
	}
	readStrictFloat := func(name string, target **float64) {
		raw := lookup(name)
		if raw == "" {
			return
		}
		parsed, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			problems = append(problems, fmt.Errorf("%s: %q is not a number", name, raw))
			return
		}
		*target = &parsed
	}
	readStrictBool := func(name string, target **bool) {
		raw := lookup(name)
		if raw == "" {
			return
		}
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			problems = append(problems, fmt.Errorf("%s: %q is not a boolean", name, raw))
			return
		}
		*target = &parsed
	}

	readLenientInt("DEVSHARD_PORT", 1, &values.Port)
	readString("DEVSHARD_STORAGE_DIR", &values.StorageDir)
	readString("DEVSHARD_API_KEYS", &values.APIKeys)
	readString("DEVSHARD_ADMIN_API_KEY", &values.AdminAPIKey)
	readString("DEVSHARDS_JSON", &values.DevshardsJSON)
	readLenientInt("DEVSHARD_MAX_CONCURRENT_RUNTIME_BUILDS", 1, &values.MaxConcurrentRuntimeBuilds)
	readLenientInt("DEVSHARD_MAX_CONCURRENT_PROMPT_COUNTS", 1, &values.MaxConcurrentPromptCounts)
	readLenientBool("DEVSHARD_STATS_TOKENIZER", &values.StatsTokenizerEnabled)
	readLenientInt("DEVSHARD_SESSION_RETENTION_EPOCHS", 0, &values.SessionRetentionEpochs)

	readString("DEVSHARD_CHAIN_GRPC", &values.ChainGRPC, "NODE_GRPC_URL")
	readString("DEVSHARD_PUBLIC_API", &values.PublicAPI)
	readString("DEVSHARD_CHAIN_ID", &values.ChainID)
	readString("DEVSHARD_CHAIN_RPC", &values.ChainRPC, "NODE_RPC_URL")
	readString("DEVSHARD_TX_FEE_DENOM", &values.TxFeeDenom)
	readLenientInt("DEVSHARD_TX_FEE_AMOUNT", 1, &values.TxFeeAmount)
	readLenientInt("DEVSHARD_TX_GAS_LIMIT", 1, &values.TxGasLimit)
	readLenientInt("DEVSHARD_TX_POLL_INTERVAL_MS", 1, &values.TxPollIntervalMS)
	readLenientInt("DEVSHARD_TX_POLL_TIMEOUT_MS", 1, &values.TxPollTimeoutMS)

	readLenientInt("GATEWAY_DEFAULT_MAX_TOKENS", 1, &values.DefaultMaxTokens)
	readLenientInt("GATEWAY_MAX_TOKENS_CAP", 1, &values.MaxTokensCap)
	readLenientInt("GATEWAY_MAX_CONCURRENT_REQUESTS", 0, &values.MaxConcurrentRequests)
	readLenientPositiveFloat("GATEWAY_MAX_CONCURRENT_REQUESTS_PER_10000_WEIGHT", &values.MaxConcurrentRequestsPer10000Weight)
	readLenientPositiveFloat("GATEWAY_POC_MAX_CONCURRENT_REQUESTS_PER_10000_WEIGHT", &values.PoCMaxConcurrentRequestsPer10000Weight)
	readLenientInt("GATEWAY_MAX_INPUT_TOKENS_IN_FLIGHT", 0, &values.MaxInputTokensInFlight)
	readStrictInt("GATEWAY_ADMISSION_QUEUE_WAIT_MS", &values.AdmissionQueueWaitMS)
	readStrictInt("GATEWAY_ADMISSION_QUEUE_PER_SLOT", &values.AdmissionQueuePerSlot)

	if raw := lookup("DEVSHARD_POC_REQUEST_MODE"); raw != "" {
		if mode := strings.ToLower(raw); mode == PoCModeOff || mode == PoCModeRelaxed {
			values.PoCMode = &mode
		} else {
			ignoreUnusableValue("DEVSHARD_POC_REQUEST_MODE", raw)
		}
	}
	readLenientBool("DEVSHARD_GATEWAY_DISABLED", &values.Disabled)
	readString("DEVSHARD_GATEWAY_DISABLED_MESSAGE", &values.DisabledMessage)
	readString("DEVSHARD_GATEWAY_DISABLED_NEW_URL", &values.DisabledRedirectURL)

	readLenientBool("DEVSHARD_ESCROW_ROTATION_ENABLED", &values.RotationEnabled)
	readLenientInt("DEVSHARD_ESCROW_ROTATION_PRE_POC_BLOCKS", 0, &values.RotationPrePoCBlocks)
	readLenientBool("DEVSHARD_ESCROW_ROTATION_SETTLEMENT_ENABLED", &values.RotationSettlementEnabled)
	readLenientBool("DEVSHARD_ESCROW_ROTATION_PREVIOUS_EPOCH_SETTLEMENT_ENABLED", &values.RotationPreviousEpochSettlementEnabled)
	readString("DEVSHARD_ESCROW_ROTATION_MODELS_JSON", &values.RotationModelsJSON)
	readStrictInt("GATEWAY_MATCH_WAIT_MS", &values.MatchWaitMS)
	readStrictInt("GATEWAY_MAX_CONSECUTIVE_BURNS", &values.MaxConsecutiveBurns)
	readStrictInt("GATEWAY_DISPATCH_QUEUE_DEPTH", &values.DispatchQueueDepth)
	readStrictBool("GATEWAY_FORCE_UPSTREAM_STREAMING", &values.ForceUpstreamStreaming)
	readStrictBool("GATEWAY_LOGPROBS_OPTIMIZATION_OVERRIDE", &values.LogprobsOptimization)
	readStrictInt("GATEWAY_MAX_BUFFERED_RESPONSE_BYTES", &values.MaxBufferedResponseBytes)
	readStrictBool("GATEWAY_WARM_NEW_ESCROWS", &values.WarmNewEscrows)
	readStrictInt("GATEWAY_ROTATION_SETTLE_MARGIN_BLOCKS", &values.RotationSettleMarginBlocks)

	readLenientInt("DEVSHARD_CHAT_CACHE_MAX_BYTES", 1, &values.ChatCacheMaxBytes)

	readStrictInt("GATEWAY_REQUESTS_RETENTION_HOURS", &values.AccountingRetentionHours)
	readStrictInt("GATEWAY_REQUESTS_RETENTION_MAX_ROWS", &values.AccountingRetentionMaxRows)

	readLenientBool("DEVSHARD_STATS_ENABLED", &values.NonceAccountingEnabled)
	readLenientInt("DEVSHARD_STATS_PORT", 1, &values.NonceAccountingPort)
	readLenientInt("DEVSHARD_STATS_RETENTION_EPOCHS", 0, &values.NonceAccountingRetentionEpochs)
	readLenientInt("DEVSHARD_STATS_SNAPSHOT_SECONDS", 1, &values.NonceAccountingSnapshotSeconds)
	readStrictInt("GATEWAY_TIMEOUT_SWEEP_BUDGET_PER_TICK", &values.TimeoutSweepBudgetPerTick)
	readStrictInt("GATEWAY_TIMEOUT_SWEEP_GRACE_SECONDS", &values.TimeoutSweepGraceSeconds)

	readStrictBool("GATEWAY_HEIGHT_SYNC_ENABLED", &values.HeightSyncEnabled)
	if raw := strings.ToLower(lookup("DEVSHARD_REQUIRE_HEIGHT_SEED")); raw != "" {
		requireSeed := !slices.Contains([]string{"0", "false", "off", "no"}, raw)
		values.HeightSyncRequireSeed = &requireSeed
	}
	readOnOnlySwitch := func(name string, target **bool) {
		if raw := strings.ToLower(lookup(name)); raw != "" {
			on := slices.Contains([]string{"true", "1", "on"}, raw)
			*target = &on
		}
	}
	readOnOnlySwitch("DEVSHARD_GATEWAY_CHAIN_ORACLE", &values.HeightSyncChainOracle)
	readOnOnlySwitch("DEVSHARD_HEIGHTSYNC_HEARTBEAT", &values.HeightSyncHeartbeat)
	readLenientInt("DEVSHARD_HEIGHTSYNC_K", 0, &values.HeightSyncAnchorK)
	readLenientInt("DEVSHARD_HEIGHTSYNC_SLOTS", 0, &values.HeightSyncAnchorSlots)
	readDurationAsMilliseconds("DEVSHARD_QUIET_SETTLE_DELAY", &values.HeightSyncQuietSettleDelayMS)
	readLenientBool("DEVSHARD_GATEWAY_HOST_PING_DISABLED", &values.HostPingDisabled)
	readDurationAsMilliseconds("DEVSHARD_GATEWAY_HOST_PING_INTERVAL", &values.HostPingIntervalMS)
	readDurationAsMilliseconds("DEVSHARD_GATEWAY_HOST_PING_TIMEOUT", &values.HostPingTimeoutMS)
	readLenientInt("DEVSHARD_GATEWAY_HOST_PING_CONCURRENCY", 1, &values.HostPingConcurrency)
	readLenientBool("DEVSHARD_REQUEST_CAPTURE_ENABLED", &values.CaptureEnabled)
	readString("DEVSHARD_REQUEST_CAPTURE_DIR", &values.CaptureDir)
	readStrictFloat("GATEWAY_CAPTURE_SAMPLE_RATE", &values.CaptureSampleRate)
	readStrictInt("GATEWAY_CAPTURE_MAX_BYTES", &values.CaptureMaxBytes)

	readStrictInt("GATEWAY_PERF_EWMA_HALFLIFE_SECONDS", &values.PerfEWMAHalfLifeSeconds)
	readStrictInt("GATEWAY_PERF_CONSECUTIVE_FAIL_THRESHOLD", &values.PerfConsecutiveFailThreshold)
	readStrictFloat("GATEWAY_PERF_FAILURE_RATE_THRESHOLD", &values.PerfFailureRateThreshold)
	readStrictFloat("GATEWAY_PERF_FAILURE_RATE_MIN_VOLUME", &values.PerfFailureRateMinVolume)
	readStrictInt("GATEWAY_PERF_EJECTION_BASE_SECONDS", &values.PerfEjectionBaseSeconds)
	readStrictInt("GATEWAY_PERF_EJECTION_MAX_SECONDS", &values.PerfEjectionMaxSeconds)
	readStrictFloat("GATEWAY_PERF_MAX_EJECTION_FRACTION", &values.PerfMaxEjectionFraction)
	readStrictInt("GATEWAY_PERF_MIN_AVAILABLE_HOSTS", &values.PerfMinAvailableHosts)
	readStrictInt("GATEWAY_PERF_HOST_STALENESS_SECONDS", &values.PerfHostStalenessSeconds)

	readStrictInt("GATEWAY_CHAIN_SNAPSHOT_MAX_AGE_SECONDS", &values.ChainSnapshotMaxAgeSeconds)
	readStrictInt("GATEWAY_CHAIN_VERSIONS_POLL_INTERVAL_SECONDS", &values.ChainVersionsPollIntervalSeconds)

	readStrictInt("GATEWAY_ENGINE_RECEIPT_TIMEOUT_MS", &values.EngineReceiptTimeoutMS)
	readStrictInt("GATEWAY_ENGINE_FIRST_TOKEN_FLOOR_MS", &values.EngineFirstTokenFloorMS)
	readStrictInt("GATEWAY_ENGINE_FIRST_TOKEN_CEILING_MS", &values.EngineFirstTokenCeilingMS)
	readStrictInt("GATEWAY_ENGINE_INTER_CHUNK_STALL_MS", &values.EngineInterChunkStallMS)
	readStrictInt("GATEWAY_ENGINE_LOSER_GRACE_MS", &values.EngineLoserGraceMS)
	readStrictInt("GATEWAY_ENGINE_MAX_CONCURRENT_TIMEOUT_VOTES", &values.EngineMaxConcurrentTimeoutVotes)
	readStrictInt("GATEWAY_ENGINE_HEDGE_FIRST_TOKEN_FLOOR_MS", &values.EngineHedgeFirstTokenFloorMS)

	readLenientBool("DEVSHARD_ENABLE_CONTENTION_PROFILES", &values.ContentionProfiles)

	if len(problems) > 0 {
		return Values{}, fmt.Errorf("reading environment: %w", errors.Join(problems...))
	}
	return values, nil
}
