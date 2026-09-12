package logger

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
)

func New(out io.Writer, level, format string) *slog.Logger {
	if strings.EqualFold(strings.TrimSpace(format), "json") {
		return slog.New(slog.NewJSONHandler(out, &slog.HandlerOptions{Level: parse(level)}))
	}
	return slog.New(&console{out: out, level: parse(level), writing: &sync.Mutex{}})
}

type console struct {
	out     io.Writer
	level   slog.Level
	base    string
	writing *sync.Mutex
}

func (c *console) Enabled(_ context.Context, level slog.Level) bool { return level >= c.level }

func (c *console) Handle(_ context.Context, record slog.Record) error {
	fields := strings.Builder{}
	fields.WriteString(c.base)
	record.Attrs(func(attr slog.Attr) bool {
		fields.WriteString(field(attr))
		return true
	})

	var line strings.Builder
	fmt.Fprintf(&line, "%s %-5s ", record.Time.Format("15:04:05.000"), record.Level)
	if fields.Len() == 0 {
		line.WriteString(record.Message + "\n")
	} else {
		fmt.Fprintf(&line, "%-24s%s\n", record.Message, fields.String())
	}

	c.writing.Lock()
	defer c.writing.Unlock()
	_, err := io.WriteString(c.out, line.String())
	return err
}

func (c *console) WithAttrs(attrs []slog.Attr) slog.Handler {
	added := *c
	for _, attr := range attrs {
		added.base += field(attr)
	}
	return &added
}

func (c *console) WithGroup(string) slog.Handler { return c }

func field(attr slog.Attr) string {
	value := attr.Value.String()
	if value == "" || strings.ContainsAny(value, " \t\n\"") {
		value = fmt.Sprintf("%q", value)
	}
	return " " + attr.Key + "=" + value
}

func parse(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
