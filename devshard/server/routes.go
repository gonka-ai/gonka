package server

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"devshard/transport"
)

// SessionResolver resolves a lazy per-escrow transport server.
type SessionResolver interface {
	SessionServer(escrowID string) (*transport.Server, error)
}

// PayloadHandler serves GET /sessions/:id/payloads for a resolved session.
type PayloadHandler interface {
	HandlePayloads(c echo.Context, srv *transport.Server) error
}

// RegisterLazySessionRoutes mounts the standard devshard HTTP surface on g.
// Session servers are resolved lazily per request via SessionResolver.
func RegisterLazySessionRoutes(g *echo.Group, resolver SessionResolver, payloadHandler PayloadHandler) {
	g.POST("/sessions/:id/chat/completions", withSessionAuth(resolver,
		func(srv *transport.Server) echo.HandlerFunc { return srv.HandleInference }))
	g.POST("/sessions/:id/verify-timeout", withSessionAuth(resolver,
		func(srv *transport.Server) echo.HandlerFunc { return srv.HandleVerifyTimeout }))
	g.POST("/sessions/:id/challenge-receipt", withSessionAuth(resolver,
		func(srv *transport.Server) echo.HandlerFunc { return srv.HandleChallengeReceipt }))
	g.POST("/sessions/:id/gossip/nonce", withSessionAuth(resolver,
		func(srv *transport.Server) echo.HandlerFunc { return srv.HandleGossipNonce }))
	g.POST("/sessions/:id/gossip/txs", withSessionAuth(resolver,
		func(srv *transport.Server) echo.HandlerFunc { return srv.HandleGossipTxs }))

	g.GET("/sessions/:id/diffs", withSession(resolver,
		func(srv *transport.Server) echo.HandlerFunc { return srv.HandleGetDiffs }))
	g.GET("/sessions/:id/mempool", withSession(resolver,
		func(srv *transport.Server) echo.HandlerFunc { return srv.HandleGetMempool }))
	g.GET("/sessions/:id/signatures", withSession(resolver,
		func(srv *transport.Server) echo.HandlerFunc { return srv.HandleGetSignatures }))

	if payloadHandler != nil {
		g.GET("/sessions/:id/payloads", func(c echo.Context) error {
			srv, err := resolver.SessionServer(c.Param("id"))
			if err != nil {
				return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
			}
			return payloadHandler.HandlePayloads(c, srv)
		})
	}
}

func withSession(
	resolver SessionResolver,
	pick func(*transport.Server) echo.HandlerFunc,
) echo.HandlerFunc {
	return func(c echo.Context) error {
		srv, err := resolver.SessionServer(c.Param("id"))
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		return pick(srv)(c)
	}
}

func withSessionAuth(
	resolver SessionResolver,
	pick func(*transport.Server) echo.HandlerFunc,
) echo.HandlerFunc {
	return func(c echo.Context) error {
		srv, err := resolver.SessionServer(c.Param("id"))
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		return srv.AuthMiddleware(pick(srv))(c)
	}
}
