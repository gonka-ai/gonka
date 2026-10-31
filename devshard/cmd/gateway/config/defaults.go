package config

import (
	"runtime"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/filters"
)

// fallbackMaxModelLen prices a model no context length has been named for, and processorsPerPromptCount leaves half the processors to requests that are not counting. See capacity.md, "The participant limiter: IOCW", and request.md, "The prompt token count".
const (
	fallbackMaxModelLen      = 1_000_000
	processorsPerPromptCount = 2
)

func Defaults() Config {
	return Config{
		Server: Server{
			Port:                       8080,
			MaxConcurrentRuntimeBuilds: 16,
			MaxConcurrentPromptCounts:  max(int64(runtime.GOMAXPROCS(0))/processorsPerPromptCount, 1),
			SessionRetentionEpochs:     2,
		},
		Chain: Chain{
			GRPCEndpoint:                "localhost:9090",
			PublicAPIBaseURL:            "http://localhost:9000",
			SnapshotMaxAgeSeconds:       60,
			VersionsPollIntervalSeconds: int64(chain.DefaultVersionsPollInterval.Seconds()),
		},
		Tx: Tx{
			FeeDenom:       chain.DefaultFeeDenom,
			FeeAmount:      int64(chain.DefaultFeeAmount),
			GasLimit:       int64(chain.DefaultGasLimit),
			PollIntervalMS: chain.DefaultPollInterval.Milliseconds(),
			PollTimeoutMS:  chain.DefaultPollTimeout.Milliseconds(),
		},
		Limits: Limits{
			DefaultMaxTokens:         int64(filters.DefaultRequestMaxTokens),
			ForceUpstreamStreaming:   true,
			MaxBufferedResponseBytes: 512 << 20,
			MaxTokensCap:             int64(filters.RequestMaxTokensCap),
			Concurrency: Concurrency{
				MaxRequests:               2048,
				RequestsPer10000Weight:    5,
				PoCRequestsPer10000Weight: 10,
			},
			MaxInputTokensInFlight: 0,
			AdmissionQueueWaitMS:   300_000,
			AdmissionQueuePerSlot:  4,
			FallbackMaxModelLen:    fallbackMaxModelLen,
			HostWindows: HostWindows{
				Input:  RequestWindow{MinRequests: 16, InitialRequests: 32},
				Output: RequestWindow{MinRequests: 16, InitialRequests: 32},
			},
			Congestion: Congestion{
				BetaSoft:   0.85,
				BetaHard:   0.70,
				BetaSevere: 0.50,
				BetaCross:  0.90,
				Slack:      0.30,
			},
			HostCutoff: HostCutoff{
				AfterFailures: 3,
				BaseMS:        5_000,
				MaxMS:         60_000,
			},
		},
		Modes: Modes{
			PoCMode: PoCModeRelaxed,
		},
		Rotation: Rotation{
			PrePoCBlocks:       300,
			SettleMarginBlocks: 600,
		},
		Cache: Cache{
			ChatCacheMaxBytes: 256 << 20,
		},
		Accounting: Accounting{
			RetentionHours:   168,
			RetentionMaxRows: 1_000_000,
		},
		NonceAccounting: NonceAccounting{
			Port:            9091,
			RetentionEpochs: 2,
			SnapshotSeconds: 60,
		},
		Capture: Capture{
			SampleRate: 0.01,
			MaxBytes:   1 << 30,
		},
		Stream: Stream{
			DrainTimeoutSeconds:         2_400,
			ClassifyMaxAttemptBytes:     1 << 20,
			ClassifyMaxParticipantBytes: 10 << 20,
			ClassifyMaxGlobalBytes:      100 << 20,
		},
		Perf: Perf{
			EWMAHalfLifeSeconds:      600,
			ConsecutiveFailThreshold: 5,
			FailureRateThreshold:     0.15,
			FailureRateMinVolume:     20,
			EjectionBaseSeconds:      30,
			EjectionMaxSeconds:       600,
			MaxEjectionFraction:      0.5,
			MinAvailableHosts:        4,
			HostStalenessSeconds:     3_600,
		},
		HeightSync: HeightSync{
			Enabled:            true,
			RequireSeed:        true,
			AnchorK:            10,
			AnchorSlots:        1,
			QuietSettleDelayMS: 300_000,
		},
		HostPing: HostPing{
			IntervalMS:  15_000,
			TimeoutMS:   2_000,
			Concurrency: 8,
		},
		Engine: Engine{
			ReceiptTimeoutMS:          5_000,
			FirstTokenFloorMS:         6_000,
			FirstTokenCeilingMS:       30_000,
			InterChunkStallMS:         30_000,
			LoserGraceMS:              600_000,
			MaxAttemptsPerRequest:     2,
			MaxConcurrentTimeoutVotes: 64,
			HedgeFirstTokenFloorMS:    1_500,
		},
		Scheduler: Scheduler{
			MatchWaitMS:         2_000,
			MaxConsecutiveBurns: 2,
			DispatchQueueDepth:  1_024,
			WarmNewEscrows:      true,
		},
		TimeoutSweep: TimeoutSweep{
			BudgetPerTick: 8,
			GraceSeconds:  120,
		},
	}
}
