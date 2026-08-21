package httpx

import (
	"bufio"
	"log/slog"
	"net"
	"net/http"
	"time"

	"trainshard/internal/contract"
	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/ports"
)

var errNoHijack = shared.New("STREAM_UNSUPPORTED", shared.ErrUnavailable, "this server cannot hand over the connection")

func Log(log *slog.Logger, clock ports.Clock) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started := clock.Now()
			served := &recorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(served, r)

			attrs := []slog.Attr{
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", served.status),
				slog.Duration("took", clock.Now().Sub(started).Round(time.Millisecond)),
				slog.String("request_id", r.Header.Get(contract.HeaderRequestID)),
			}
			if served.cause != nil {
				attrs = append(attrs, slog.String("error", served.cause.Error()))
			}
			log.LogAttrs(r.Context(), level(served.status), "served request", attrs...)
		})
	}
}

func level(status int) slog.Level {
	switch {
	case status >= http.StatusInternalServerError:
		return slog.LevelError
	case status >= http.StatusBadRequest:
		return slog.LevelWarn
	default:
		return slog.LevelInfo
	}
}

type recorder struct {
	http.ResponseWriter
	status int
	cause  error
}

func (r *recorder) keep(err error) {
	r.cause = err
}

func (r *recorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *recorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (r *recorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errNoHijack
	}
	return hijacker.Hijack()
}
