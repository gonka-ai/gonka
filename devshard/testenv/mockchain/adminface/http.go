package adminface

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"

	"devshard/testenv/gatewayphase"
	"devshard/testenv/mockchain/store"
)

// Server serves mock-chain admin /testenv/* routes.
type Server struct {
	echo *echo.Echo
}

// NewServer builds an admin HTTP server for store mutations.
// advancer enables catch-up block simulation on POST /testenv/epoch advance (mock-chain process).
func NewServer(st *store.Store, advancer EpochAdvancer, escrowPub EscrowPublisher) *Server {
	e := echo.New()
	e.HideBanner = true
	Mount(e.Group(""), st, advancer, escrowPub)
	epoch := st.GetEpoch()
	gatewayphase.Mount(e.Group(""), gatewayphase.Config{
		BlockHeight:         st.GetBlockHeight(),
		EpochIndex:          epoch.Index,
		PoCStartBlockHeight: epoch.PocStartBlockHeight,
	})
	return &Server{echo: e}
}

// Serve listens until ctx is cancelled.
func (s *Server) Serve(ctx context.Context, addr string) error {
	if s == nil || s.echo == nil {
		return nil
	}
	s.echo.Server.BaseContext = func(net.Listener) context.Context { return ctx }
	go func() {
		<-ctx.Done()
		shCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.echo.Shutdown(shCtx)
	}()
	if err := s.echo.Start(addr); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return ctx.Err()
}

// Handler returns the root http.Handler (tests).
func (s *Server) Handler() http.Handler {
	if s == nil {
		return nil
	}
	return s.echo
}
