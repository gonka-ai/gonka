package logging

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

type countingLogger struct {
	calls *atomic.Uint64
}

func (l countingLogger) Info(string, ...any)  { l.calls.Add(1) }
func (l countingLogger) Error(string, ...any) { l.calls.Add(1) }
func (l countingLogger) Warn(string, ...any)  { l.calls.Add(1) }
func (l countingLogger) Debug(string, ...any) { l.calls.Add(1) }

type countingContextLogger struct {
	countingLogger
	contextCalls *atomic.Uint64
}

func (l countingContextLogger) InfoContext(context.Context, string, ...any) {
	l.contextCalls.Add(1)
}
func (l countingContextLogger) ErrorContext(context.Context, string, ...any) {
	l.contextCalls.Add(1)
}
func (l countingContextLogger) WarnContext(context.Context, string, ...any) {
	l.contextCalls.Add(1)
}
func (l countingContextLogger) DebugContext(context.Context, string, ...any) {
	l.contextCalls.Add(1)
}

func TestSetLoggerConcurrentWithLogging(t *testing.T) {
	previous := currentLogger()
	previousStructured := structuredStages.Load()
	t.Cleanup(func() {
		SetLogger(previous)
		structuredStages.Store(previousStructured)
	})
	structuredStages.Store(true)
	var calls, contextCalls atomic.Uint64
	plain := countingLogger{calls: &calls}
	contextual := countingContextLogger{countingLogger: plain, contextCalls: &contextCalls}
	SetLogger(plain)
	const iterations = 1000
	start := make(chan struct{})
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		<-start
		for i := 0; i < iterations; i++ {
			SetLogger(contextual)
			SetLogger(plain)
		}
	}()
	go func() {
		defer workers.Done()
		<-start
		ctx := context.Background()
		for i := 0; i < iterations; i++ {
			Info("info")
			Error("error")
			Warn("warn")
			Debug("debug")
			InfoCtx(ctx, "info")
			ErrorCtx(ctx, "error")
			WarnCtx(ctx, "warn")
			DebugCtx(ctx, "debug")
			Stage(ctx, "stage")
		}
	}()
	close(start)
	workers.Wait()
	require.Equal(t, uint64(9*iterations), calls.Load()+contextCalls.Load())

	calls.Store(0)
	contextCalls.Store(0)
	SetLogger(contextual)
	ctx := context.Background()
	InfoCtx(ctx, "info")
	ErrorCtx(ctx, "error")
	WarnCtx(ctx, "warn")
	DebugCtx(ctx, "debug")
	Stage(ctx, "stage")
	require.Equal(t, uint64(5), contextCalls.Load())
	require.Zero(t, calls.Load(), "context-aware calls must not fall back to plain logging")
}
