package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"devshard/accounting"
)

func accountingRetentionEpochs() uint64 {
	value := readInt64Env("DEVSHARD_STATS_RETENTION_EPOCHS", 0)
	if value < 0 {
		log.Printf("invalid DEVSHARD_STATS_RETENTION_EPOCHS=%d, using 0", value)
		return 0
	}
	return uint64(value)
}

// accountingSnapshotInterval bounds what a container stop loses: the gateway
// handles no signals, so the tick is the only writer between settlements.
func accountingSnapshotInterval() time.Duration {
	seconds := readInt64Env("DEVSHARD_STATS_SNAPSHOT_SECONDS", 0)
	if seconds <= 0 {
		return accounting.DefaultSnapshotInterval
	}
	return time.Duration(seconds) * time.Second
}

func accountingCurrentEpoch(g *Gateway) accounting.CurrentEpochFunc {
	return func(context.Context) (uint64, error) {
		if g == nil || g.phaseGate == nil {
			return 0, fmt.Errorf("chain phase is unavailable")
		}
		epoch := g.phaseGate.Snapshot().EpochIndex
		if epoch == 0 {
			return 0, fmt.Errorf("current epoch is unavailable")
		}
		return epoch, nil
	}
}

const defaultStatsPort = "9091"

// accountingStatsAddr binds every interface: the reader is a dashboard sidecar in
// another container, so loopback would be unreachable. The listener has no
// authentication and must stay unpublished outside the private network.
func accountingStatsAddr() string {
	return "0.0.0.0:" + firstNonEmpty(os.Getenv("DEVSHARD_STATS_PORT"), defaultStatsPort)
}

func startAccountingServer(g *Gateway) (*http.Server, error) {
	if g == nil || g.accounting == nil {
		return nil, nil
	}
	addr := accountingStatsAddr()
	server := &http.Server{
		Addr:              addr,
		Handler:           accounting.NewHandler(g.accounting.Tracker(), accountingCurrentEpoch(g)),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       time.Minute,
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen on accounting address %s: %w", addr, err)
	}
	go func() {
		log.Printf("devshard accounting API listening on %s", addr)
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("devshard accounting API stopped: %v", err)
		}
	}()
	return server, nil
}
