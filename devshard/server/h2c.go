package server

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"devshard/transport"
)

// H2CServer is the HTTP/2 settings for the child listen. MaxConcurrentStreams
// matches the Phase 4 per-peer stream cap so SETTINGS does not silently
// throttle below the interceptor.
func H2CServer() *http2.Server {
	return &http2.Server{MaxConcurrentStreams: transport.DefaultRPCMaxStreams}
}

// H2CHandler wraps h so one TCP connection can carry HTTP/1.1 and h2c
// (prior-knowledge HTTP/2). Multiplexing is on: concurrent streams share that
// connection. A server without this wrapper cannot complete an HTTP/2 client.
func H2CHandler(h http.Handler) http.Handler {
	return h2c.NewHandler(h, H2CServer())
}

// EnableH2C installs H2CHandler on Echo's listen. Echo.Start uses
// e.Server.Handler; ServeHTTP on the Echo itself is unchanged (tests).
func EnableH2C(e *echo.Echo) {
	e.Server.Handler = H2CHandler(e)
}
