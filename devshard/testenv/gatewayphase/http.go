// Package gatewayphase serves minimal public-API stubs for devshardctl's
// ChainPhaseGate in the testenv (epochs/latest + current participants).
package gatewayphase

import (
	"context"
	"fmt"
	"net/http"

	"github.com/labstack/echo/v4"
)

// Config tunes the stub epoch/participant responses.
type Config struct {
	BlockHeight int64
	EpochIndex  uint64
	// PoCStartBlockHeight is latest_epoch.poc_start_block_height.
	PoCStartBlockHeight int64
	// Source, when set, supplies live chain state on every request and the
	// static fields above are ignored. A Source error answers 503.
	Source Source
	// Schedule overrides the reported phase; nil reports Inference. It is
	// also exposed as GET/POST /testenv/phase.
	Schedule *Schedule
}

// Participant is one entry of the current participants list.
type Participant struct {
	Address      string
	InferenceURL string
}

// State is the chain view the stub reports.
type State struct {
	BlockHeight         int64
	EpochIndex          uint64
	PoCStartBlockHeight int64
	Participants        []Participant
}

// Source reads the chain state behind the stub.
type Source func(ctx context.Context) (State, error)

// participantPoCWeight is each participant's single ML-node poc_weight. The
// gateway allows floor(weight * 5 / 10000) requests per participant by
// default, so 1 gave zero capacity; this gives 5000. Equal weights keep routing even.
const participantPoCWeight = 10_000_000

// Mount registers GET /v1/epochs/latest, GET /v1/epochs/current/participants
// and GET/POST /testenv/phase.
func Mount(g *echo.Group, cfg Config) {
	if g == nil {
		return
	}
	if cfg.BlockHeight == 0 {
		cfg.BlockHeight = 150
	}
	if cfg.EpochIndex == 0 {
		cfg.EpochIndex = 1
	}
	if cfg.PoCStartBlockHeight == 0 {
		cfg.PoCStartBlockHeight = cfg.BlockHeight - 50
	}
	if cfg.Schedule == nil {
		cfg.Schedule = NewSchedule(nil)
	}
	source := cfg.Source
	if source == nil {
		static := State{BlockHeight: cfg.BlockHeight, EpochIndex: cfg.EpochIndex, PoCStartBlockHeight: cfg.PoCStartBlockHeight}
		source = func(context.Context) (State, error) { return static, nil }
	}
	g.GET("/v1/epochs/latest", func(c echo.Context) error {
		st, err := source(c.Request().Context())
		if err != nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable, err.Error())
		}
		step := cfg.Schedule.Current()
		body := map[string]any{
			"block_height": fmt.Sprintf("%d", st.BlockHeight),
			"phase":        step.Phase,
			"latest_epoch": map[string]any{
				"index":                  fmt.Sprintf("%d", st.EpochIndex),
				"poc_start_block_height": fmt.Sprintf("%d", st.PoCStartBlockHeight),
			},
			"epoch_stages": map[string]any{
				"epoch_index":        fmt.Sprintf("%d", st.EpochIndex),
				"set_new_validators": fmt.Sprintf("%d", st.BlockHeight+30),
				"next_poc_start":     fmt.Sprintf("%d", st.BlockHeight+50),
			},
			"next_epoch_stages": map[string]any{
				"epoch_index":        fmt.Sprintf("%d", st.EpochIndex+1),
				"set_new_validators": fmt.Sprintf("%d", st.BlockHeight+450),
			},
			"is_confirmation_poc_active": step.ConfirmationPoCActive(),
		}
		if step.ConfirmationPoCActive() {
			body["active_confirmation_poc_event"] = map[string]any{
				"phase":          step.ConfirmationPoCPhase,
				"trigger_height": fmt.Sprintf("%d", st.BlockHeight),
			}
		}
		return c.JSON(http.StatusOK, body)
	})
	g.GET("/v1/epochs/current/participants", func(c echo.Context) error {
		st, err := source(c.Request().Context())
		if err != nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable, err.Error())
		}
		participants := make([]any, 0, len(st.Participants))
		for _, p := range st.Participants {
			participants = append(participants, map[string]any{
				"index":         p.Address,
				"inference_url": p.InferenceURL,
				"ml_nodes": []any{map[string]any{"ml_nodes": []any{map[string]any{
					"node_id":             p.Address + "-ml",
					"timeslot_allocation": []bool{true, false},
					"poc_weight":          fmt.Sprintf("%d", participantPoCWeight),
				}}}},
			})
		}
		return c.JSON(http.StatusOK, map[string]any{
			"active_participants": map[string]any{
				"participants": participants,
			},
		})
	})
	g.GET("/testenv/phase", func(c echo.Context) error {
		return c.JSON(http.StatusOK, cfg.Schedule.Snapshot())
	})
	g.POST("/testenv/phase", func(c echo.Context) error {
		var req ScheduleRequest
		if err := c.Bind(&req); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		steps, err := req.Steps.parse()
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		cfg.Schedule.Set(steps)
		return c.JSON(http.StatusOK, cfg.Schedule.Snapshot())
	})
}
