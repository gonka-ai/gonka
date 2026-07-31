package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func serveGateway(handler http.Handler, port string, runtimeCount int) {
	server := &http.Server{
		Addr:              ":" + port,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	log.Printf("devshardctl gateway listening on %s (devshards=%d default_max_tokens=%d max_tokens_cap=%d)",
		server.Addr, runtimeCount, DefaultRequestMaxTokens, RequestMaxTokensCap)

	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe() }()

	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("server: %v", err)
		}
	case <-signalCtx.Done():
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			log.Printf("server shutdown: %v", err)
			_ = server.Close()
		}
	}
}
