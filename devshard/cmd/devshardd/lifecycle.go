package main

import (
	"net/http"
	"strings"
	"sync/atomic"

	"devshard/observability"

	"github.com/labstack/echo/v4"
)

type lifecycleState struct {
	ready    atomic.Bool
	draining atomic.Bool
	inflight atomic.Int64
}

type drainStatus struct {
	Ready    bool  `json:"ready"`
	Draining bool  `json:"draining"`
	Inflight int64 `json:"inflight"`
}

func newLifecycleState() *lifecycleState {
	observability.SetLifecycleInflight(0)
	return &lifecycleState{}
}

func (s *lifecycleState) SetReady(ready bool) {
	s.ready.Store(ready)
}

func (s *lifecycleState) StartDrain() {
	s.draining.Store(true)
	s.ready.Store(false)
}

func (s *lifecycleState) Status() drainStatus {
	return drainStatus{
		Ready:    s.ready.Load(),
		Draining: s.draining.Load(),
		Inflight: s.inflight.Load(),
	}
}

func (s *lifecycleState) addInflight(delta int64) {
	inflight := s.inflight.Add(delta)
	observability.SetLifecycleInflight(inflight)
}

func (s *lifecycleState) middleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if isLifecycleBypassPath(c.Request().URL.Path) {
			return next(c)
		}
		// Count first so drain cannot report idle after this request is admitted.
		s.addInflight(1)
		if s.draining.Load() {
			s.addInflight(-1)
			return echo.NewHTTPError(http.StatusServiceUnavailable, "devshardd is draining")
		}
		defer s.addInflight(-1)
		return next(c)
	}
}

func isLifecycleBypassPath(path string) bool {
	clean := "/" + strings.Trim(strings.TrimSpace(path), "/")
	switch clean {
	case "/healthz", "/metrics":
		return true
	default:
		return false
	}
}
