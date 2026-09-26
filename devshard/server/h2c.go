package server

import (
	"errors"
	"net"
	"net/http"

	"github.com/labstack/echo/v4"
	"golang.org/x/net/http2"

	"devshard/transport"
)

// H2CServer is the HTTP/2 settings for the child listen. SETTINGS is per
// TCP connection: versiond uses one process-wide http2.Transport, so this
// is a child-wide cap on that mux, not the per-peer interceptor
// (DefaultRPCMaxStreams). Keep lockstep with versiond
// DefaultH2MaxConcurrentStreams and HAProxy tune.h2.max-concurrent-streams.
func H2CServer() *http2.Server {
	return &http2.Server{MaxConcurrentStreams: transport.DefaultH2MaxConcurrentStreams}
}

// ConfigureCleartextHTTP2 enables HTTP/1.1 and prior-knowledge HTTP/2 on srv.
// http.Server tracks those connections, so Shutdown sends GOAWAY and waits
// for the handler. h2c.NewHandler hijacks the conn without that tracking,
// and Shutdown returns while the stream is still running.
// MaxConcurrentStreams is applied by ConfigureServer (4096).
func ConfigureCleartextHTTP2(srv *http.Server) error {
	if srv == nil {
		return errors.New("nil http server")
	}
	if srv.Protocols == nil {
		srv.Protocols = new(http.Protocols)
	}
	srv.Protocols.SetHTTP1(true)
	srv.Protocols.SetUnencryptedHTTP2(true)
	return http2.ConfigureServer(srv, H2CServer())
}

// EnableH2C turns on cleartext HTTP/2 for Echo's Server. The setting lives
// on the Server, so Echo.Start replacing Handler does not drop it.
// Production uses StartH2C.
func EnableH2C(e *echo.Echo) {
	if e == nil {
		return
	}
	if err := ConfigureCleartextHTTP2(e.Server); err != nil {
		e.Logger.Error(err)
	}
}

// StartH2C listens and serves e with cleartext HTTP/2. Echo.Start is not
// used: this listen is started from the already-built Echo.
func StartH2C(e *echo.Echo, address string) error {
	if e == nil {
		return errors.New("nil echo")
	}
	ln, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	e.Listener = ln
	e.Server.Addr = address
	e.Server.ErrorLog = e.StdLogger
	e.Server.Handler = e
	if err := ConfigureCleartextHTTP2(e.Server); err != nil {
		_ = ln.Close()
		return err
	}
	return e.Server.Serve(ln)
}
