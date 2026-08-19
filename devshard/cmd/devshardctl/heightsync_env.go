package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"

	blockclient "devshard/chainoracle/blocks/client"
	"devshard/heightsync"
	"devshard/transport"
)

const (
	envChainOracleURL  = "DEVSHARD_CHAINORACLE_URL"
	envHeightSyncK     = "DEVSHARD_HEIGHTSYNC_K"
	envHeightSyncSlots = "DEVSHARD_HEIGHTSYNC_SLOTS"
	envLogLevel        = "DEVSHARD_LOG_LEVEL"
)

type heightSyncProcessState struct {
	sched  *heightsync.AnchorScheduler
	oracle *blockclient.Client
}

var (
	hsOnce sync.Once
	hsSt   *heightSyncProcessState
	hsErr  error
)

// initGatewaySlog raises slog when DEVSHARD_LOG_LEVEL is set.
// Unset leaves the Go default (Info), matching today's gateway logs.
func initGatewaySlog() {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(envLogLevel)))
	if raw == "" {
		return
	}
	level := slog.LevelInfo
	switch raw {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
}

func parseUintEnv(name string) (uint64, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return 0, nil
	}
	v, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	return v, nil
}

func loadHeightSyncProcessState() (*heightSyncProcessState, error) {
	hsOnce.Do(func() {
		url := strings.TrimSpace(os.Getenv(envChainOracleURL))
		if url == "" {
			return
		}
		k, err := parseUintEnv(envHeightSyncK)
		if err != nil {
			hsErr = err
			return
		}
		slots, err := parseUintEnv(envHeightSyncSlots)
		if err != nil {
			hsErr = err
			return
		}
		cli, err := blockclient.NewHTTP(context.Background(), blockclient.HTTPConfig{BaseURL: url})
		if err != nil {
			hsErr = fmt.Errorf("chainoracle http client: %w", err)
			return
		}
		sched, err := heightsync.NewAnchorSchedulerFromOracle(k, slots, cli)
		if err != nil {
			cli.Close()
			hsErr = fmt.Errorf("height-sync scheduler: %w", err)
			return
		}
		hsSt = &heightSyncProcessState{sched: sched, oracle: cli}
		slog.Info("height sync enabled", "oracle_url", url, "k", sched.K(), "slots", sched.SlotsNum())
	})
	return hsSt, hsErr
}

// extraClientConfigFromEnv returns a per-session ClientConfig when
// DEVSHARD_CHAINORACLE_URL is set. Peer-tip caches are not shared across
// escrows. Nil, nil when the env is unset.
func extraClientConfigFromEnv() (*transport.ClientConfig, error) {
	st, err := loadHeightSyncProcessState()
	if err != nil || st == nil {
		return nil, err
	}
	return &transport.ClientConfig{
		HeightSync:          st.sched,
		HeightSyncLogOracle: st.oracle,
		HeightSyncPeerTips:  transport.NewHeightSyncPeerTips(),
	}, nil
}
