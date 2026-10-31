// Package config owns the gateway's immutable configuration snapshot: defaults ← environment ← admin
// overrides, never mutated after Build. See README.md.
package config

import (
	"slices"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/env"
	"devshard/cmd/gateway/filters"
)

// Server is the listener's own configuration; StorageDir is resolved by main before Build.
type Server struct {
	Port                       int64
	StorageDir                 string
	APIKeys                    []string
	AdminAPIKey                string
	DevshardsJSON              string
	MaxConcurrentRuntimeBuilds int64
	MaxConcurrentPromptCounts  int64
	StatsTokenizerEnabled      bool
	SessionRetentionEpochs     int64
}

// Chain addresses the network; a moved RPC port must be named, not derived. See README.md, "What each group holds".
type Chain struct {
	PublicAPIBaseURL            string
	GRPCEndpoint                string
	RPCEndpoint                 string
	ChainID                     string
	SnapshotMaxAgeSeconds       int64
	VersionsPollIntervalSeconds int64
}

type Tx struct {
	FeeDenom       string
	FeeAmount      int64
	GasLimit       int64
	PollIntervalMS int64
	PollTimeoutMS  int64
}

// Concurrency caps requests in flight; a zero MaxRequests leaves only the per-weight limit.
type Concurrency struct {
	MaxRequests               int64
	RequestsPer10000Weight    float64
	PoCRequestsPer10000Weight float64
}

// RequestWindow is one congestion window's floor and starting size, counted in requests. See capacity.md, "The participant limiter: IOCW".
type RequestWindow struct {
	MinRequests     int64
	InitialRequests int64
}

// HostWindows caps what one host may hold in flight: the input tokens it must prefill and the output tokens it may still produce.
type HostWindows struct {
	Input  RequestWindow
	Output RequestWindow
}

// Congestion is the narrowing ladder and the latency slack a host is judged against.
type Congestion struct {
	BetaSoft   float64
	BetaHard   float64
	BetaSevere float64
	BetaCross  float64
	Slack      float64
}

type HostCutoff struct {
	AfterFailures int64
	BaseMS        int64
	MaxMS         int64
}

// ModelLimits is the per-model override set; a nil pointer field inherits the global limit.
type ModelLimits struct {
	DefaultMaxTokens       int64  `json:"default_max_tokens"`
	MaxTokensCap           int64  `json:"max_tokens_cap"`
	MaxModelLen            *int64 `json:"max_model_len,omitempty"`
	MaxConcurrentRequests  *int64 `json:"max_concurrent_requests,omitempty"`
	MaxInputTokensInFlight *int64 `json:"max_input_tokens_in_flight,omitempty"`
}

// Limits groups admission tuning; several zeroes mean unlimited. See README.md, "What each group holds".
type Limits struct {
	DefaultMaxTokens         int64
	ForceUpstreamStreaming   bool
	LogprobsOptimization     *bool
	MaxBufferedResponseBytes int64
	MaxTokensCap             int64
	FallbackMaxModelLen      int64
	Concurrency              Concurrency
	MaxInputTokensInFlight   int64
	AdmissionQueueWaitMS     int64
	AdmissionQueuePerSlot    int64
	HostWindows              HostWindows
	Congestion               Congestion
	HostCutoff               HostCutoff
	ModelLimits              map[string]ModelLimits
	ModelAccess              map[string]string
}

type Modes struct {
	PoCMode             string
	Disabled            bool
	DisabledMessage     string
	DisabledRedirectURL string
}

type Rotation struct {
	Enabled                        bool
	SettlementEnabled              bool
	PreviousEpochSettlementEnabled bool
	PrePoCBlocks                   int64
	ModelsJSON                     string
	SettleMarginBlocks             int64
}

type Cache struct {
	ChatCacheMaxBytes int64
}

// Accounting bounds the per-request ledger on both axes; neither may be zero.
type Accounting struct {
	RetentionHours   int64
	RetentionMaxRows int64
}

// NonceAccounting configures the per-nonce ledger, not the per-request one; RetentionEpochs must be at least 1 while Enabled. See docs/accounting.md, "Storage".
type NonceAccounting struct {
	Enabled         bool
	Port            int64
	RetentionEpochs int64
	SnapshotSeconds int64
}

// Capture bounds the request-capture sink; an empty Dir means <storageDir>/captured-requests.
type Capture struct {
	Enabled    bool
	Dir        string
	SampleRate float64
	MaxBytes   int64
}

type Stream struct {
	DrainTimeoutSeconds         int64
	ClassifyMaxAttemptBytes     int64
	ClassifyMaxParticipantBytes int64
	ClassifyMaxGlobalBytes      int64
}

// Perf groups Envoy-style host ejection tuning. See README.md, "What each group holds".
type Perf struct {
	EWMAHalfLifeSeconds      int64
	ConsecutiveFailThreshold int64
	FailureRateThreshold     float64
	FailureRateMinVolume     float64
	EjectionBaseSeconds      int64
	EjectionMaxSeconds       int64
	MaxEjectionFraction      float64
	MinAvailableHosts        int64
	HostStalenessSeconds     int64
}

// HeightSync groups the escrow's height cadence. See ../docs/escrows.md, "Height sync".
type HeightSync struct {
	Enabled            bool
	RequireSeed        bool
	ChainOracle        bool
	Heartbeat          bool
	AnchorK            int64
	AnchorSlots        int64
	QuietSettleDelayMS int64
}

// HostPing groups the probe of the live escrows' hosts. See ../hostping/README.md.
type HostPing struct {
	Disabled    bool
	IntervalMS  int64
	TimeoutMS   int64
	Concurrency int64
}

// Engine groups race-escalation tuning. See race.md, "Tunables and backstops".
type Engine struct {
	ReceiptTimeoutMS          int64
	FirstTokenFloorMS         int64
	FirstTokenCeilingMS       int64
	InterChunkStallMS         int64
	LoserGraceMS              int64
	MaxAttemptsPerRequest     int64
	MaxConcurrentTimeoutVotes int64
	HedgeFirstTokenFloorMS    int64
}

// Scheduler groups nonce-holding tuning; MatchWaitMS of 0 burns an unmatched nonce immediately, and MaxConsecutiveBurns of 0 never forces a send. See routing.md, "The forced send".
type Scheduler struct {
	MatchWaitMS             int64
	MaxConsecutiveBurns     int64
	DispatchQueueDepth      int64
	WarmNewEscrows          bool
	ParticipantAllowlist    []string
	UnthrottledParticipants []string
}

// TimeoutSweep bounds the retry of execution timeouts no race is left to post; a zero BudgetPerTick turns the sweep off.
type TimeoutSweep struct {
	BudgetPerTick int64
	GraceSeconds  int64
}

// Config is the complete immutable gateway configuration snapshot.
type Config struct {
	Server     Server
	Chain      Chain
	Tx         Tx
	Limits     Limits
	Modes      Modes
	Rotation   Rotation
	Cache      Cache
	Accounting Accounting
	Capture    Capture
	Stream     Stream
	Perf       Perf
	HostPing   HostPing
	HeightSync HeightSync
	Engine     Engine
	Scheduler  Scheduler

	TimeoutSweep TimeoutSweep

	NonceAccounting NonceAccounting
}

// PoCMode values are what Modes.PoCMode accepts, ModelAccess values what Limits.ModelAccess accepts, and NoContextLength is a model whose length neither governance nor the operator names.
const (
	NoContextLength = chain.NoMaxModelLen

	PoCModeOff     = env.PoCModeOff
	PoCModeRelaxed = env.PoCModeRelaxed

	ModelAccessOpen      = "open"
	ModelAccessAPIKey    = "api_key"
	ModelAccessAdminOnly = "admin_only"

	minAdminAPIKeyLength = 16
)

// AdminEnabled is what callers must gate on: comparing against AdminAPIKey would authenticate an empty key.
func (s Server) AdminEnabled() bool { return s.AdminAPIKey != "" }

// AccessFor resolves a model's access tier. See operations.md, "Who may call what".
func (l Limits) AccessFor(model string) string {
	if access, ok := l.ModelAccess[model]; ok {
		return access
	}
	return ModelAccessAdminOnly
}

// ContextLength is the --max-model-len governance registers, else the operator's max_model_len pin; NoContextLength when neither names a usable one. See race.md, "Escalation".
func (l Limits) ContextLength(model string, governedMaxModelLen uint64) uint64 {
	if governedMaxModelLen != chain.NoMaxModelLen && governedMaxModelLen <= MaxContextTokens {
		return governedMaxModelLen
	}
	if pinned := l.ModelLimits[model].MaxModelLen; pinned != nil && *pinned > 0 {
		return uint64(*pinned)
	}
	return NoContextLength
}

// RetirementReserve is what an escrow of the model must still afford, in the chain's reserve units: its context length as prompt bytes, never less than one capped answer. See capacity.md, "The balance floor".
func (l Limits) RetirementReserve(model string, governedMaxModelLen uint64) uint64 {
	return max(uint64(max(l.MaxTokensCap, 0)), filters.PromptBytesPerToken*l.ContextLength(model, governedMaxModelLen))
}

// OpenModels lists, sorted, the models any caller may reach without a key. See operations.md, "Who may call what".
func (l Limits) OpenModels() []string {
	var open []string
	for model, access := range l.ModelAccess {
		if access == ModelAccessOpen {
			open = append(open, model)
		}
	}
	slices.Sort(open)
	return open
}

// Offers reports whether the operator names the model in ModelAccess or ModelLimits. See operations.md, "Who may call what".
func (l Limits) Offers(model string) bool {
	if _, ok := l.ModelAccess[model]; ok {
		return true
	}
	_, ok := l.ModelLimits[model]
	return ok
}

// BlocksRequests folds relaxed PoC mode's override into the chain's raw blocking state. See root README.md, "Relaxed mode, in one place".
func (modes Modes) BlocksRequests(snapshot chain.PhaseSnapshot) bool {
	return snapshot.RequestsBlocked && modes.PoCMode != PoCModeRelaxed
}
