package observability

import (
	"context"
	"net"
	"net/http"
	"sync"

	"github.com/labstack/echo/v4"
)

func RequestIDMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		BindEchoRequestID(c)
		return next(c)
	}
}

func BindEchoRequestID(c echo.Context) context.Context {
	id := c.Request().Header.Get(RequestIDHeader)
	ctx := BindRequestID(c.Request().Context(), id)
	c.SetRequest(c.Request().WithContext(ctx))
	SetRequestIDHeader(ctx, c.Response().Header())
	return ctx
}

func ConnState(server string) func(net.Conn, http.ConnState) {
	ensureMetrics()
	var mu sync.Mutex
	states := make(map[net.Conn]string)

	return func(conn net.Conn, state http.ConnState) {
		next := connStateLabel(state)
		if next == "" {
			return
		}

		mu.Lock()
		defer mu.Unlock()

		if prev := states[conn]; prev != "" {
			httpConnections.WithLabelValues(server, prev).Dec()
		}
		if state == http.StateClosed || state == http.StateHijacked {
			delete(states, conn)
			httpConnectionsTotal.WithLabelValues(server, next).Inc()
			return
		}
		states[conn] = next
		httpConnections.WithLabelValues(server, next).Inc()
		httpConnectionsTotal.WithLabelValues(server, next).Inc()
	}
}

func connStateLabel(state http.ConnState) string {
	switch state {
	case http.StateNew:
		return "new"
	case http.StateActive:
		return "active"
	case http.StateIdle:
		return "idle"
	case http.StateHijacked:
		return "hijacked"
	case http.StateClosed:
		return "closed"
	default:
		return ""
	}
}
