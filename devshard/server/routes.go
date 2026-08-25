package server

import (
	"compress/gzip"
	"errors"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

	devshardpkg "devshard"
	"devshard/bridge"
	"devshard/observability"
	"devshard/storage"
	"devshard/transport"
)

// ErrInitializing means devshard storage is not ready to serve session state yet.
var ErrInitializing = errors.New("devshard initializing")

// SessionResolver resolves an already-bound per-escrow transport server.
// Implementations must never CreateSession / bind a protocol version.
type SessionResolver interface {
	SessionServerExisting(escrowID string) (*transport.Server, error)
}

// OwnerChatBinder authenticates the escrow owner and may bind a new session.
type OwnerChatBinder interface {
	BindOwnerChat(c echo.Context) (*transport.Server, error)
}

// PayloadHandler serves GET /sessions/:id/payloads for a resolved session.
type PayloadHandler interface {
	HandlePayloads(c echo.Context, srv *transport.Server) error
}

// RegisterLazySessionRoutes mounts the standard devshard HTTP surface on g.
// Observability and host protocol routes resolve existing sessions only.
// Only owner chat may bind a new session (via OwnerChatBinder).
func RegisterLazySessionRoutes(g *echo.Group, resolver SessionResolver, binder OwnerChatBinder, payloadHandler PayloadHandler) {
	g.Use(observability.EchoMiddleware())
	g.Use(observability.RequestIDMiddleware)
	g.Use(canonicalEscrowIDMiddleware)

	g.POST("/sessions/:id/chat/completions", withOwnerChat(binder, true,
		func(srv *transport.Server) echo.HandlerFunc { return srv.HandleInference }))
	g.POST("/sessions/:id/verify-timeout", withSessionAuth(resolver, false,
		func(srv *transport.Server) echo.HandlerFunc { return srv.HandleVerifyTimeout }))
	g.POST("/sessions/:id/challenge-receipt", withSessionAuth(resolver, false,
		func(srv *transport.Server) echo.HandlerFunc { return srv.HandleChallengeReceipt }))
	g.POST("/sessions/:id/gossip/nonce", withSessionAuth(resolver, false,
		func(srv *transport.Server) echo.HandlerFunc { return srv.HandleGossipNonce }))
	g.POST("/sessions/:id/gossip/txs", withSessionAuth(resolver, false,
		func(srv *transport.Server) echo.HandlerFunc { return srv.HandleGossipTxs }))

	g.GET("/sessions/:id/diffs", withSession(resolver,
		func(srv *transport.Server) echo.HandlerFunc { return srv.HandleGetDiffs }))
	g.GET("/sessions/:id/mempool", withSession(resolver,
		func(srv *transport.Server) echo.HandlerFunc { return srv.HandleGetMempool }))
	g.GET("/sessions/:id/signatures", withSession(resolver,
		func(srv *transport.Server) echo.HandlerFunc { return srv.HandleGetSignatures }))

	if payloadHandler != nil {
		// Scoped to this route: gzip on the inference stream would buffer it.
		g.GET("/sessions/:id/payloads", func(c echo.Context) error {
			srv, err := resolver.SessionServerExisting(c.Param("id"))
			if err != nil {
				recordSessionResolution(c, err, false)
				return sessionHTTPError(c, err)
			}
			observability.IncSessionResolution(routeLabel(c), observability.MetricStatusOK, observability.ReasonOK)
			return payloadHandler.HandlePayloads(c, srv)
		}, middleware.GzipWithConfig(middleware.GzipConfig{Level: gzip.BestSpeed}))
	}
}

func canonicalEscrowIDMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		escrowID := c.Param("id")
		if escrowID == "" {
			return next(c)
		}
		if err := devshardpkg.ValidateEscrowID(escrowID); err != nil {
			observability.IncSessionResolution(routeLabel(c), observability.MetricStatusError, observability.ReasonInvalidEscrowID)
			observability.Log(c.Request().Context(), observability.LevelWarn, "devshard rejected non-canonical escrow id",
				observability.StageSessionResolved, observability.WhereRoutesSessionResolve, escrowID, observability.ReasonInvalidEscrowID, err)
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		return next(c)
	}
}

func withSession(
	resolver SessionResolver,
	pick func(*transport.Server) echo.HandlerFunc,
) echo.HandlerFunc {
	return func(c echo.Context) error {
		srv, err := resolver.SessionServerExisting(c.Param("id"))
		if err != nil {
			recordSessionResolution(c, err, false)
			return sessionHTTPError(c, err)
		}
		observability.IncSessionResolution(routeLabel(c), observability.MetricStatusOK, observability.ReasonOK)
		return pick(srv)(c)
	}
}

func withSessionAuth(
	resolver SessionResolver,
	recordChatTerminal bool,
	pick func(*transport.Server) echo.HandlerFunc,
) echo.HandlerFunc {
	return func(c echo.Context) error {
		srv, err := resolver.SessionServerExisting(c.Param("id"))
		if err != nil {
			recordSessionResolution(c, err, recordChatTerminal)
			return sessionHTTPError(c, err)
		}
		observability.IncSessionResolution(routeLabel(c), observability.MetricStatusOK, observability.ReasonOK)
		handler := pick(srv)
		wrapped := srv.RateLimitMiddleware(recordChatTerminal)(handler)
		return srv.AuthMiddleware(wrapped)(c)
	}
}

func withOwnerChat(
	binder OwnerChatBinder,
	recordChatTerminal bool,
	pick func(*transport.Server) echo.HandlerFunc,
) echo.HandlerFunc {
	return func(c echo.Context) error {
		srv, err := binder.BindOwnerChat(c)
		if err != nil {
			recordSessionResolution(c, err, recordChatTerminal)
			return sessionHTTPError(c, err)
		}
		observability.IncSessionResolution(routeLabel(c), observability.MetricStatusOK, observability.ReasonOK)
		handler := pick(srv)
		return srv.RateLimitMiddleware(recordChatTerminal)(handler)(c)
	}
}

func recordSessionResolution(c echo.Context, err error, recordChatTerminal bool) {
	metricStatus, reason := sessionResolutionStatus(err)
	route := routeLabel(c)
	escrowID := c.Param("id")
	ctx := c.Request().Context()
	observability.IncSessionResolution(route, metricStatus, reason)
	observability.Log(ctx, observability.LevelWarn, "devshard session resolution failed", observability.StageSessionResolved, observability.WhereRoutesSessionResolve, escrowID, reason, err)
	if recordChatTerminal {
		observability.RecordNoReceiptInterrupted(ctx, escrowID, reason, observability.WhereRoutesSessionResolve)
	}
}

func sessionResolutionStatus(err error) (observability.MetricStatus, observability.Reason) {
	if errors.Is(err, ErrInitializing) || errors.Is(err, storage.ErrStorageIndexRebuilding) {
		return observability.MetricStatusError, observability.ReasonInitializing
	}
	if errors.Is(err, storage.ErrSessionNotFound) {
		return observability.MetricStatusError, observability.ReasonSessionResolveErr
	}
	if errors.Is(err, bridge.ErrChainUnavailable) {
		return observability.MetricStatusError, observability.ReasonGetEscrowErr
	}
	if errors.Is(err, storage.ErrSessionVersionConflict) {
		return observability.MetricStatusError, observability.ReasonVersionConflict
	}
	if errors.Is(err, storage.ErrSessionEpochConflict) {
		return observability.MetricStatusError, observability.ReasonEpochConflict
	}
	var he *echo.HTTPError
	if errors.As(err, &he) {
		switch he.Code {
		case http.StatusUnauthorized, http.StatusForbidden:
			return observability.MetricStatusError, observability.ReasonOwnerErr
		}
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "build group"):
		return observability.MetricStatusError, observability.ReasonBuildGroupErr
	case strings.Contains(msg, "get escrow"):
		return observability.MetricStatusError, observability.ReasonGetEscrowErr
	case strings.Contains(msg, "storage"):
		return observability.MetricStatusError, observability.ReasonStorageErr
	default:
		return observability.MetricStatusError, observability.ReasonSessionResolveErr
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

func sessionHTTPError(c echo.Context, err error) error {
	var he *echo.HTTPError
	if errors.As(err, &he) {
		return he
	}
	if errors.Is(err, ErrInitializing) || errors.Is(err, storage.ErrStorageIndexRebuilding) {
		return transport.HTTPError(c, http.StatusServiceUnavailable, transport.DevshardErrorInitializing, err.Error())
	}
	if errors.Is(err, storage.ErrSessionNotFound) {
		return echo.NewHTTPError(http.StatusNotFound, "session not found")
	}
	if errors.Is(err, bridge.ErrChainUnavailable) {
		return transport.HTTPError(c, http.StatusServiceUnavailable, transport.DevshardErrorChainUnavailable, err.Error())
	}
	if errors.Is(err, storage.ErrSessionVersionConflict) || errors.Is(err, storage.ErrSessionEpochConflict) {
		return echo.NewHTTPError(http.StatusConflict, err.Error())
	}
	return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
}
