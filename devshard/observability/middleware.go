package observability

import (
	"context"
	"net"
	"net/http"
	"sync"

	"github.com/labstack/echo/v4"
)

var untracedRoutes = map[string]struct{}{
	"/metrics": {},
	"/healthz": {},
}

// EchoMiddleware extracts W3C trace context from incoming requests, opens a
// server-side span around handler execution, and records HTTP status / errors.
//
// Use as `e.Use(observability.EchoMiddleware())` on the root router so the
// span is the parent of any further spans the handler creates via the
// observability façade.
func EchoMiddleware() echo.MiddlewareFunc {
	tracer := Default.Request()
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			req := c.Request()

			route := c.Path()
			if route == "" {
				route = req.URL.Path
			}
			if _, skip := untracedRoutes[route]; skip {
				return next(c)
			}

			ctx := ExtractRequestContext(req.Context(), req.Header)
			ctx, op := tracer.StartRequest(ctx, req.Method, route)
			c.SetRequest(req.WithContext(ctx))

			err := next(c)

			status := c.Response().Status
			if err != nil {
				if httpErr, ok := err.(*echo.HTTPError); ok {
					status = httpErr.Code
				}
			}
			tracer.SetHTTPStatus(op, status)
			op.Finish(err)
			return err
		}
	}
}

// RequestIDMiddleware binds X-Request-Id from the inbound request to the
// context (generating one if absent) and echoes it back on the response.
// Use after EchoMiddleware so the span context is already extracted.
func RequestIDMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		BindEchoRequestID(c)
		return next(c)
	}
}

// BindEchoRequestID stores the inbound X-Request-Id (or a fresh one) on the
// echo request context and writes it onto the response header.
func BindEchoRequestID(c echo.Context) context.Context {
	id := c.Request().Header.Get(RequestIDHeader)
	ctx := BindRequestID(c.Request().Context(), id)
	c.SetRequest(c.Request().WithContext(ctx))
	SetRequestIDHeader(ctx, c.Response().Header())
	return ctx
}

// ConnState returns an http.Server.ConnState callback that maintains the
// devshard_http_connections gauge and devshard_http_connections_total counter
// for the named server (e.g. "ml", "devshardd").
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
