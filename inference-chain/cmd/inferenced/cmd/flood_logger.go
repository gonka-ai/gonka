package cmd

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"cosmossdk.io/log"
)

const (
	floodLogWindow = time.Minute
	floodLogBurst  = 20
)

const floodProcessMessagePrefix = "failed to process message"

var floodLogPrefixes = []string{
	"failed attempting to add vote",
	floodProcessMessagePrefix,
	"Peer asking for a block we don't have",
	"received empty txs from peer",
	"Unknown message type",
	"Received unknown message",
	"Ignoring message received during sync",
}

type floodLevel int

const (
	floodLevelInfo floodLevel = iota
	floodLevelWarn
	floodLevelError
)

type floodEntry struct {
	line    string
	msgType string
	level   floodLevel
	logger  log.Logger
	count   int
	lastErr string
}

type floodSummary struct {
	line       string
	msgType    string
	level      floodLevel
	logger     log.Logger
	suppressed int
	lastErr    string
}

type floodLimiter struct {
	mu       sync.Mutex
	now      func() time.Time
	window   time.Duration
	burst    int
	prefixes []string
	start    time.Time
	entries  map[string]*floodEntry
}

func newFloodLimiter(now func() time.Time, window time.Duration, burst int, prefixes []string) *floodLimiter {
	return &floodLimiter{
		now:      now,
		window:   window,
		burst:    burst,
		prefixes: prefixes,
		start:    now(),
		entries:  make(map[string]*floodEntry),
	}
}

func (f *floodLimiter) match(msg string) string {
	for _, p := range f.prefixes {
		if strings.HasPrefix(msg, p) {
			return p
		}
	}
	return ""
}

func keyValue(keyVals []any, key string) string {
	for i := 0; i+1 < len(keyVals); i += 2 {
		if k, ok := keyVals[i].(string); ok && k == key {
			return fmt.Sprint(keyVals[i+1])
		}
	}
	return ""
}

func (f *floodLimiter) rollover() []floodSummary {
	t := f.now()
	if t.Sub(f.start) < f.window {
		return nil
	}
	var summaries []floodSummary
	for _, e := range f.entries {
		if e.count > f.burst {
			summaries = append(summaries, floodSummary{
				line:       e.line,
				msgType:    e.msgType,
				level:      e.level,
				logger:     e.logger,
				suppressed: e.count - f.burst,
				lastErr:    e.lastErr,
			})
		}
	}
	sort.Slice(summaries, func(i, j int) bool {
		if summaries[i].line != summaries[j].line {
			return summaries[i].line < summaries[j].line
		}
		return summaries[i].msgType < summaries[j].msgType
	})
	clear(f.entries)
	f.start = t
	return summaries
}

func (f *floodLimiter) allow(inner log.Logger, level floodLevel, msg string, keyVals []any) (bool, []floodSummary) {
	line := f.match(msg)

	f.mu.Lock()
	defer f.mu.Unlock()

	summaries := f.rollover()
	if line == "" {
		return true, summaries
	}

	msgType := ""
	if line == floodProcessMessagePrefix {
		msgType = keyValue(keyVals, "msg_type")
	}
	key := line + "\x00" + msgType
	e, ok := f.entries[key]
	if !ok {
		e = &floodEntry{line: line, msgType: msgType, level: level, logger: inner}
		f.entries[key] = e
	}
	if level > e.level {
		e.level = level
		e.logger = inner
	}
	e.count++
	if e.count <= f.burst {
		return true, summaries
	}
	if errVal := keyValue(keyVals, "err"); errVal != "" {
		e.lastErr = errVal
	}
	return false, summaries
}

type floodLogger struct {
	log.Logger
	limiter *floodLimiter
}

var _ log.VerboseModeLogger = (*floodLogger)(nil)

func newFloodLogger(inner log.Logger) log.Logger {
	return &floodLogger{
		Logger:  inner,
		limiter: newFloodLimiter(time.Now, floodLogWindow, floodLogBurst, floodLogPrefixes),
	}
}

func emitFloodSummary(s floodSummary, window time.Duration) {
	keyVals := []any{"line", s.line, "suppressed", s.suppressed, "window", window.String()}
	if s.msgType != "" {
		keyVals = append(keyVals, "msg_type", s.msgType)
	}
	if s.lastErr != "" {
		keyVals = append(keyVals, "last_err", s.lastErr)
	}
	const msg = "suppressed repeated log lines"
	switch s.level {
	case floodLevelError:
		s.logger.Error(msg, keyVals...)
	case floodLevelWarn:
		s.logger.Warn(msg, keyVals...)
	default:
		s.logger.Info(msg, keyVals...)
	}
}

func (l *floodLogger) pass(level floodLevel, msg string, keyVals []any) bool {
	ok, summaries := l.limiter.allow(l.Logger, level, msg, keyVals)
	for _, s := range summaries {
		emitFloodSummary(s, l.limiter.window)
	}
	return ok
}

func (l *floodLogger) Info(msg string, keyVals ...any) {
	if l.pass(floodLevelInfo, msg, keyVals) {
		l.Logger.Info(msg, keyVals...)
	}
}

func (l *floodLogger) Warn(msg string, keyVals ...any) {
	if l.pass(floodLevelWarn, msg, keyVals) {
		l.Logger.Warn(msg, keyVals...)
	}
}

func (l *floodLogger) Error(msg string, keyVals ...any) {
	if l.pass(floodLevelError, msg, keyVals) {
		l.Logger.Error(msg, keyVals...)
	}
}

func (l *floodLogger) With(keyVals ...any) log.Logger {
	return &floodLogger{Logger: l.Logger.With(keyVals...), limiter: l.limiter}
}

func (l *floodLogger) SetVerboseMode(enable bool) {
	if v, ok := l.Logger.(log.VerboseModeLogger); ok {
		v.SetVerboseMode(enable)
	}
}
