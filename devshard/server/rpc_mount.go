package server

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"devshard/observability"
	"devshard/transport/rpcserver"
)

// peerRPCRoute is the Echo route pattern the Connect mux is served on. Kept as
// a constant because skipPeerRPC and canonicalEscrowIDMiddleware match on it.
const peerRPCRoute = "/sessions/:id/rpc/*"

func isPeerRPCPath(c echo.Context) bool {
	return strings.HasSuffix(c.Path(), peerRPCRoute)
}

// skipPeerRPC disables mw on the Connect mount. Connect negotiates its own
// request compression and enforces WithReadMaxBytes while inflating; an Echo
// middleware that consumes Content-Encoding first would bypass both.
func skipPeerRPC(mw echo.MiddlewareFunc) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		wrapped := mw(next)
		return func(c echo.Context) error {
			if isPeerRPCPath(c) {
				return next(c)
			}
			return wrapped(c)
		}
	}
}

func mountPeerRPC(g *echo.Group, h http.Handler) {
	observability.SetPeerRPCEnabled(true)
	g.Any(peerRPCRoute, func(c echo.Context) error {
		escrowID := c.Param("id")
		r := c.Request()
		u := *r.URL
		u.Path = stripRPCPrefix(r.URL.Path, escrowID)
		u.RawPath = ""
		r2 := r.WithContext(rpcserver.WithEscrowID(r.Context(), escrowID))
		r2.URL = &u
		h.ServeHTTP(c.Response(), r2)
		return nil
	})
}

// stripRPCPrefix returns the Connect procedure path. The known mount is
// optional /devshard/{version} then exactly /sessions/{escrowID}/rpc. A decoy
// /sessions/{id}/rpc earlier in the URL is ignored (last occurrence wins).
func stripRPCPrefix(path, escrowID string) string {
	mount := "/sessions/" + escrowID + "/rpc"
	rest := path
	if after, ok := strings.CutPrefix(path, "/devshard/"); ok {
		if i := strings.IndexByte(after, '/'); i >= 0 {
			rest = after[i:]
		}
	}
	after, ok := strings.CutPrefix(rest, mount)
	if !ok || strings.Contains(after, mount) {
		idx := strings.LastIndex(path, mount)
		if idx < 0 {
			return normalizeConnectPath(path)
		}
		after = path[idx+len(mount):]
	}
	return normalizeConnectPath(after)
}

func normalizeConnectPath(rest string) string {
	if rest == "" {
		return "/"
	}
	if !strings.HasPrefix(rest, "/") {
		return "/" + rest
	}
	return rest
}
