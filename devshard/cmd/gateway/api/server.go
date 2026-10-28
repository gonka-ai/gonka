package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/filters"
	"devshard/cmd/gateway/journal"
	"devshard/cmd/gateway/limits"
	"devshard/cmd/gateway/registry"
	"devshard/cmd/gateway/scheduler"
	"devshard/cmd/gateway/store"
)

const (
	readHeaderTimeout     = 5 * time.Second
	idleConnectionTimeout = 120 * time.Second
	maxRequestHeaderBytes = 1 << 20
	requestIDBytes        = 16
)

// EscrowRegistry is the live escrow set as the HTTP boundary reads it; *registry.Registry satisfies it.
type EscrowRegistry interface {
	Serves(model string) bool
	Models() []string
	Candidates(model string) []scheduler.Escrow
	Routable(escrowID string) (scheduler.Escrow, bool)
	Snapshot() []registry.EscrowState
	RoutableSession(escrowID string) (registry.EscrowSession, bool)
	SettlementSession(escrowID string) (registry.EscrowSession, bool)
	HoldSettlement(escrowID string) (registry.EscrowSession, func(), bool)
	Inspect(ctx context.Context, escrowID string) (registry.EscrowSession, func(), error)
	IsBusy(escrowID string) bool
}

type InferenceEngine interface {
	Run(ctx context.Context, request engine.Request, client io.Writer) (engine.RaceOutcome, error)
}

// GatewayLimiter is the admission slot and input-token budget a request holds while it races.
type GatewayLimiter interface {
	AcquireForModel(ctx context.Context, model string, inputTokens int64, capacity limits.ModelCapacity) error
	ReleaseForModel(model string, inputTokens int64)
}

// CapacityReader yields the already block- and availability-filtered capacity one Acquire needs.
type CapacityReader interface {
	ForModel(model string) limits.ModelCapacity
}

type SnapshotSource interface {
	Snapshot() chain.PhaseSnapshot
}

// RequestLedger is the per-request accounting ledger; *store.Ledger satisfies it.
type RequestLedger interface {
	Record(record store.RequestRecord)
	Find(ctx context.Context, requestID string) (store.RequestRecord, bool, error)
}

type ControlStore interface {
	ListDevshards(ctx context.Context) ([]store.DevshardRecord, error)
	LoadOverrides(ctx context.Context) (config.Overrides, error)
	LoadRotationStatuses(ctx context.Context) ([]store.RotationStatus, error)
}

// CreateEscrowRequest names the variable holding the signing key, never the key. See operations.md, "What is exposed".
type CreateEscrowRequest struct {
	Model         string `json:"model_id"`
	Amount        uint64 `json:"amount"`
	PrivateKeyEnv string `json:"private_key_env"`
	Activate      bool   `json:"activate"`
}

type AddDevshardRequest struct {
	EscrowID      string `json:"id"`
	Model         string `json:"model"`
	PrivateKeyEnv string `json:"private_key_env"`
	Activate      bool   `json:"activate"`
}

type ImportDevshardRequest struct {
	EscrowID      string `json:"id"`
	Model         string `json:"model"`
	PrivateKeyEnv string `json:"private_key_env"`
	SourcePath    string `json:"source_path"`
	Activate      bool   `json:"active"`
}

// SettleDevshardsRequest is the batch settle body. See docs/operations.md, "Settling a list of escrows".
type SettleDevshardsRequest struct {
	EscrowIDs []string `json:"escrow_ids"`
	BatchSize int      `json:"batch_size"`
	Force     bool     `json:"force"`
}

// DeactivateDevshardsRequest is the batch deactivate body. See docs/operations.md, "Deactivating a list of escrows".
type DeactivateDevshardsRequest struct {
	EscrowIDs []string `json:"escrow_ids"`
	BatchSize int      `json:"batch_size"`
}

// Operations are the lifecycle actions the operator routes trigger. See README.md, "What the server is given".
type Operations interface {
	CreateEscrow(ctx context.Context, request CreateEscrowRequest) (chain.CreateEscrowResult, error)
	AddDevshard(ctx context.Context, request AddDevshardRequest) error
	ImportDevshard(ctx context.Context, request ImportDevshardRequest) error
	DeleteDevshard(ctx context.Context, escrowID string) error
	Activate(ctx context.Context, escrowID string) error
	Deactivate(ctx context.Context, escrowID string) error
	Settle(ctx context.Context, escrowID string, force bool) (chain.SettleEscrowResult, error)
	Unquarantine(ctx context.Context, participantKey string) error
	Reconfigure(ctx context.Context, overrides config.Overrides) error
	ResetAccountingEpoch(ctx context.Context, epoch uint64) (cleared int, err error)
}

// SuspiciousHosts is the operator's manual never-trust-this-host list.
type SuspiciousHosts interface {
	List() []string
	Add(ctx context.Context, participantKey string) error
	Remove(ctx context.Context, participantKey string) error
}

// ModelFilterRecord is one operator filter setting as stored.
type ModelFilterRecord struct {
	Setting   filters.ModelSetting
	UpdatedAt string
	Problem   string
}

// Unreadable reports a stored row the gateway could not compose at boot; it is listed but never applied.
func (record ModelFilterRecord) Unreadable() bool {
	return record.Problem != problemNone
}

// ModelFilters is the operator's per-model filter settings; Profiles is what the chat path reads. See README.md, "Model filters".
type ModelFilters interface {
	Profiles() *filters.ProfileSet
	Snapshot() (records map[string]ModelFilterRecord, profiles *filters.ProfileSet)
	Put(ctx context.Context, model string, setting filters.ModelSetting) error
	Delete(ctx context.Context, model string) error
}

type Telemetry interface {
	InstrumentRoute(routeLabel string, next http.Handler) http.Handler
	Handler() http.Handler
}

// CacheResults counts what the response cache did with each reply it was offered; *metrics.CacheResultRecorder satisfies it.
type CacheResults interface {
	CacheResult(model, result string)
}

// LimitRejections counts what the gateway's own limiter turned away; *metrics.LimitRecorder satisfies it.
type LimitRejections interface {
	Rejected(model, reason string)
}

// StatsTokenizerFallbacks counts the requests that found the prompt count gate full and carried the byte estimate; *metrics.StatsTokenizerRecorder satisfies it.
type StatsTokenizerFallbacks interface {
	GateFull(model string)
}

// PromptTokens reads a normalized chat body's messages for the model's pinned tokenizer, known false while there is none; app's pinned tokenizer library satisfies it.
type PromptTokens interface {
	Prompt(model string, body []byte) (prompt PromptText, known bool)
}

// PromptText is a prompt read for one tokenizer: MaximumTokens bounds its count from its bytes alone, Count tokenizes it, and CountWithPieceCap tokenizes it with each long pre-token estimated from its start, known false when the tokenizer fails.
type PromptText interface {
	MaximumTokens() uint64
	Count() (count TokenizedPrompt, known bool)
	CountWithPieceCap() (count TokenizedPrompt, known bool)
}

// TokenizedPrompt is one tokenizer pass over a prompt: Tokens the count, Sampled when it is estimated, and MinimumTokens, from Count, never above the prompt's real count.
type TokenizedPrompt struct {
	Tokens        uint64
	MinimumTokens uint64
	Sampled       bool
}

// RequestJournal is satisfied by *journal.Journal; each method returns without waiting on a log handler.
type RequestJournal interface {
	RequestFinished(line journal.RequestLine)
	RequestThrottled(line journal.RequestLine)
	ReplyNotCached(line journal.RequestLine)
}

// Deps is everything the HTTP boundary reads or calls. See README.md, "What the server is given".
type Deps struct {
	Config                  *config.Holder
	Escrows                 EscrowRegistry
	Inference               InferenceEngine
	Limiter                 GatewayLimiter
	Capacity                CapacityReader
	Snapshots               SnapshotSource
	Control                 ControlStore
	Accounting              RequestLedger
	Operations              Operations
	Suspicious              SuspiciousHosts
	ModelFilters            ModelFilters
	HostStates              HostStates
	HostWindows             HostWindows
	Telemetry               Telemetry
	Buffers                 *BufferBudget
	Rejections              LimitRejections
	CacheResults            CacheResults
	StatsTokenizerFallbacks StatsTokenizerFallbacks
	Journal                 RequestJournal
	StorageDir              string
	Version                 string
	Now                     func() time.Time
	RequestIDs              func() string
	StopTokenVocabulary     func(model string) int
	PromptTokens            PromptTokens
}

type Server struct {
	config       *config.Holder
	escrows      EscrowRegistry
	inference    InferenceEngine
	limiter      GatewayLimiter
	capacity     CapacityReader
	snapshots    SnapshotSource
	control      ControlStore
	accounting   RequestLedger
	operations   Operations
	suspicious   SuspiciousHosts
	modelFilters ModelFilters
	hostStates   HostStates
	hostWindows  HostWindows
	telemetry    Telemetry
	rejections   LimitRejections
	cacheResults CacheResults
	events       RequestJournal
	storageDir   string
	version      string
	now          func() time.Time
	requestIDs   func() string
	buffers      *BufferBudget
	cache        *responseCache
	capture      *requestCapture

	stopTokenVocabulary     func(model string) int
	promptTokens            PromptTokens
	promptCounts            promptCountGate
	statsTokenizerFallbacks StatsTokenizerFallbacks
	statsTokenizerEnabled   bool
	settingsWrites          sync.Mutex

	gates   atomic.Pointer[keyGates]
	verify  func(authorization string) credentials
	handler http.Handler
}

func New(deps Deps) (*Server, error) {
	switch {
	case deps.Config == nil:
		return nil, errors.New("api: Config is required")
	case deps.Escrows == nil:
		return nil, errors.New("api: Escrows is required")
	case deps.Inference == nil:
		return nil, errors.New("api: Inference is required")
	case deps.Limiter == nil:
		return nil, errors.New("api: Limiter is required")
	case deps.Capacity == nil:
		return nil, errors.New("api: Capacity is required")
	case deps.Snapshots == nil:
		return nil, errors.New("api: Snapshots is required")
	case deps.Control == nil:
		return nil, errors.New("api: Control is required")
	case deps.Accounting == nil:
		return nil, errors.New("api: Accounting is required")
	case deps.Operations == nil:
		return nil, errors.New("api: Operations is required")
	case deps.Suspicious == nil:
		return nil, errors.New("api: Suspicious is required")
	case deps.ModelFilters == nil:
		return nil, errors.New("api: ModelFilters is required")
	case deps.Telemetry == nil:
		return nil, errors.New("api: Telemetry is required")
	case deps.Journal == nil:
		return nil, errors.New("api: Journal is required")
	case deps.StorageDir == "":
		return nil, errors.New("api: StorageDir is required")
	case deps.Now == nil:
		return nil, errors.New("api: Now is required")
	}
	server := &Server{
		buffers:      deps.Buffers,
		config:       deps.Config,
		escrows:      deps.Escrows,
		inference:    deps.Inference,
		limiter:      deps.Limiter,
		capacity:     deps.Capacity,
		snapshots:    deps.Snapshots,
		control:      deps.Control,
		accounting:   deps.Accounting,
		operations:   deps.Operations,
		suspicious:   deps.Suspicious,
		modelFilters: deps.ModelFilters,
		hostStates:   deps.HostStates,
		hostWindows:  deps.HostWindows,
		telemetry:    deps.Telemetry,
		rejections:   deps.Rejections,
		cacheResults: deps.CacheResults,
		events:       deps.Journal,
		storageDir:   deps.StorageDir,
		version:      deps.Version,
		now:          deps.Now,
		requestIDs:   deps.RequestIDs,

		stopTokenVocabulary:     deps.StopTokenVocabulary,
		promptTokens:            deps.PromptTokens,
		statsTokenizerFallbacks: deps.StatsTokenizerFallbacks,
	}
	if server.requestIDs == nil {
		server.requestIDs = randomRequestID
	}
	configuration := deps.Config.Load()
	server.cache = newResponseCache(configuration.Cache.ChatCacheMaxBytes)
	server.promptCounts = newPromptCountGate(configuration.Server.MaxConcurrentPromptCounts)
	server.statsTokenizerEnabled = configuration.Server.StatsTokenizerEnabled
	capture, err := newRequestCapture(configuration.Capture, deps.StorageDir, server.now)
	if err != nil {
		return nil, err
	}
	server.capture = capture
	server.verify = server.compareKeys
	server.handler = server.buildHandler()
	return server, nil
}

// Handler is the process handler: one mux, no wrapper above it.
func (s *Server) Handler() http.Handler { return s.handler }

// HTTPServer sets neither ReadTimeout nor WriteTimeout, which would cut an SSE stream, and returns the cancel of every request context, which Shutdown alone never calls. See README.md, "Reading a body" and "Streaming the reply".
func (s *Server) HTTPServer(address string) (*http.Server, context.CancelFunc) {
	requests, cancelRequests := context.WithCancel(context.Background())
	return &http.Server{
		Addr:              address,
		Handler:           s.handler,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleConnectionTimeout,
		MaxHeaderBytes:    maxRequestHeaderBytes,
		BaseContext:       func(net.Listener) context.Context { return requests },
	}, cancelRequests
}

func randomRequestID() string {
	raw := make([]byte, requestIDBytes)
	if _, err := rand.Read(raw); err != nil {
		return ""
	}
	return hex.EncodeToString(raw)
}

// CaptureStats reports the request-capture sink's own account of itself, for the metrics collector.
func (s *Server) CaptureStats() (written, refused, failed, held int64) { return s.capture.stats() }

// CacheStats reports the response cache's own account of itself, for the metrics collector.
func (s *Server) CacheStats() (hits, misses, entries, bytes int64) { return s.cache.stats() }
