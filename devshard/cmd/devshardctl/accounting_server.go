package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"devshard/accounting"
)

func (r *gatewayAccountingRecorder) handler(current accounting.CurrentEpochFunc) http.Handler {
	if r == nil || r.tracker == nil {
		return http.NotFoundHandler()
	}
	return accounting.NewHandler(r.tracker, current)
}

func accountingRetentionEpochs() uint64 {
	value := readInt64Env("DEVSHARD_STATS_RETENTION_EPOCHS", 0)
	if value < 0 {
		log.Printf("invalid DEVSHARD_STATS_RETENTION_EPOCHS=%d, using 0", value)
		return 0
	}
	return uint64(value)
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

// accountingStatsPort returns the port for the private accounting API.
// Always on; DEVSHARD_STATS_PORT overrides the default.
func accountingStatsPort() string {
	if port := strings.TrimSpace(os.Getenv("DEVSHARD_STATS_PORT")); port != "" {
		return port
	}
	return "9091"
}

func startAccountingServer(g *Gateway, port string) (*http.Server, error) {
	if g == nil || g.accounting == nil {
		return nil, nil
	}
	address := ":" + port
	server := &http.Server{
		Addr:              address,
		Handler:           g.accounting.handler(accountingCurrentEpoch(g)),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       time.Minute,
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("listen on accounting address %s: %w", address, err)
	}
	go func() {
		log.Printf("devshard accounting API listening on %s", address)
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("devshard accounting API stopped: %v", err)
		}
	}()
	return server, nil
}
