package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"devshard/observability"
	"devshard/storage"
	"devshard/transport"
)

// ErrInitializing means devshard storage is not ready to serve session state yet.
var ErrInitializing = errors.New("devshard initializing")

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
	g.Use(observability.RequestIDMiddleware)

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
				recordSessionResolution(c, err)
				return sessionHTTPError(err)
			}
			observability.IncSessionResolution(routeLabel(c), observability.ReasonOK, observability.ReasonOK)
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
			recordSessionResolution(c, err)
			return sessionHTTPError(err)
		}
		observability.IncSessionResolution(routeLabel(c), observability.ReasonOK, observability.ReasonOK)
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
			recordSessionResolution(c, err)
			return sessionHTTPError(err)
		}
		observability.IncSessionResolution(routeLabel(c), observability.ReasonOK, observability.ReasonOK)
		return srv.AuthMiddleware(pick(srv))(c)
	}
}

func recordSessionResolution(c echo.Context, err error) {
	status, reason := sessionResolutionStatus(err)
	route := routeLabel(c)
	escrowID := c.Param("id")
	ctx := c.Request().Context()
	observability.IncSessionResolution(route, status, reason)
	observability.Log(ctx, observability.LevelWarn, "devshard session resolution failed", observability.StageSessionResolved, observability.WhereRoutesSessionResolve, escrowID, status, reason, err)
	if strings.HasSuffix(c.Request().URL.Path, "/chat/completions") {
		observability.RecordNoReceiptInterrupted(ctx, escrowID, reason, observability.WhereRoutesSessionResolve)
	}
}

func sessionResolutionStatus(err error) (observability.Reason, observability.Reason) {
	if errors.Is(err, ErrInitializing) {
		return observability.ReasonInitializing, observability.ReasonInitializing
	}
	if errors.Is(err, storage.ErrSessionVersionConflict) {
		return observability.ReasonVersionConflict, observability.ReasonVersionConflict
	}
	if errors.Is(err, storage.ErrSessionEpochConflict) {
		return observability.ReasonEpochConflict, observability.ReasonEpochConflict
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "build group"):
		return observability.ReasonError, observability.ReasonBuildGroupErr
	case strings.Contains(msg, "get escrow"):
		return observability.ReasonError, observability.ReasonGetEscrowErr
	case strings.Contains(msg, "storage"):
		return observability.ReasonError, observability.ReasonStorageErr
	default:
		return observability.ReasonError, observability.ReasonSessionResolveErr
	}
}

func routeLabel(c echo.Context) string {
	path := c.Path()
	if path == "" {
		path = c.Request().URL.Path
	}
	switch {
	case strings.HasSuffix(path, "/chat/completions"):
		return "chat_completions"
	case strings.HasSuffix(path, "/payloads"):
		return "payloads"
	case strings.Contains(path, "verify-timeout"):
		return "verify_timeout"
	case strings.Contains(path, "challenge-receipt"):
		return "challenge_receipt"
	case strings.Contains(path, "gossip"):
		return "gossip"
	default:
		return "other"
	}
}

func sessionHTTPError(err error) error {
	if errors.Is(err, ErrInitializing) {
		return echo.NewHTTPError(http.StatusServiceUnavailable, err.Error())
	}
	if errors.Is(err, storage.ErrSessionVersionConflict) || errors.Is(err, storage.ErrSessionEpochConflict) {
		return echo.NewHTTPError(http.StatusConflict, err.Error())
	}
	return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
}
