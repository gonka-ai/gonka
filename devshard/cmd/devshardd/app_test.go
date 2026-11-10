package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type recordingAppHTTPServer struct {
	closeCalls    int
	shutdownCalls int
	stop          chan struct{}
	stopOnce      sync.Once
}

func (s *recordingAppHTTPServer) Start(string) error {
	if s.stop == nil {
		s.stop = make(chan struct{})
	}
	<-s.stop
	return http.ErrServerClosed
}

func (s *recordingAppHTTPServer) Close() error {
	s.closeCalls++
	if s.stop != nil {
		s.stopOnce.Do(func() { close(s.stop) })
	}
	return nil
}

func (s *recordingAppHTTPServer) Shutdown(context.Context) error {
	s.shutdownCalls++
	if s.stop != nil {
		s.stopOnce.Do(func() { close(s.stop) })
	}
	return nil
}

type blockingChainEvents struct{}

func (blockingChainEvents) Start(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestDevshardAppReturnsImmediatelyOnTerminalStorageFailure(t *testing.T) {
	storageErrors := make(chan error, 1)
	storageErrors <- errors.New("fence lost")
	closed := make(chan struct{})
	server := &recordingAppHTTPServer{stop: make(chan struct{})}
	app := &devshardApp{
		server:        server,
		chainEvents:   blockingChainEvents{},
		port:          0,
		lifecycle:     newLifecycleState(),
		shutdownGrace: 10 * time.Minute,
		storageErrors: storageErrors,
		close:         func() { close(closed) },
	}

	done := make(chan error, 1)
	go func() { done <- app.Run(context.Background()) }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "terminal storage failure") {
			t.Fatalf("Run error = %v, want terminal storage failure", err)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal storage failure waited for the graceful shutdown budget")
	}
	if server.closeCalls != 1 || server.shutdownCalls != 0 {
		t.Fatalf("server stop calls: Close=%d Shutdown=%d, want Close=1 Shutdown=0",
			server.closeCalls, server.shutdownCalls)
	}
	select {
	case <-closed:
	default:
		t.Fatal("application resources were not closed")
	}
}
