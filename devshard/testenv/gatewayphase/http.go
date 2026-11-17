// Package gatewayphase serves minimal public-API stubs for the gateway's
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
	// Phase is latest_epoch's epoch phase; empty means Inference, the phase that blocks nothing.
	Phase string
	// ConfirmationPoCActive drives is_confirmation_poc_active, the other half of the blocking state.
	ConfirmationPoCActive bool
	// Participants is what current/participants reports; empty means no weights and everyone preserved.
	Participants []Participant
	// Live, when set, is read per request; without it the values are those of the moment Mount ran.
	Live func() Config
	// Source, when set, supplies live chain state on every request and the
	// static fields above are ignored. A Source error answers 503.
	Source Source
	// Schedule overrides the reported phase and is exposed as GET/POST
	// /testenv/phase; nil reports Inference, or Live's phase when Live is set.
	Schedule *Schedule
}

// Participant is one entry of the current-participants response; the weight rides on an ML node.
type Participant struct {
	Address      string
	InferenceURL string
	Models       []string
	PoCWeight    uint64
	Preserved    bool
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

// participantPoCWeight is each Source participant's single ML-node poc_weight. The
// gateway allows floor(weight * 5 / 10000) requests per participant by
// default, so 1 gave zero capacity; this gives 5000. Equal weights keep routing even.
const participantPoCWeight = 10_000_000

func (p Participant) render() map[string]any {
	models := p.Models
	if len(models) == 0 {
		models = []string{"stub-model"}
	}
	nodes := make([]any, 0, len(models))
	for range models {
		nodes = append(nodes, map[string]any{
			"ml_nodes": []any{map[string]any{
				"node_id":             p.Address + "-node",
				"timeslot_allocation": []bool{true, p.Preserved},
				"poc_weight":          fmt.Sprintf("%d", p.PoCWeight),
			}},
		})
	}
	return map[string]any{
		"index":         p.Address,
		"inference_url": p.InferenceURL,
		"models":        models,
		"ml_nodes":      nodes,
	}
}

func (p Participant) renderSourced() map[string]any {
	return map[string]any{
		"index":         p.Address,
		"inference_url": p.InferenceURL,
		"ml_nodes": []any{map[string]any{"ml_nodes": []any{map[string]any{
			"node_id":             p.Address + "-ml",
			"timeslot_allocation": []bool{true, false},
			"poc_weight":          fmt.Sprintf("%d", participantPoCWeight),
		}}}},
	}
}

// phaseOf keeps Inference the default.
func phaseOf(cfg Config) string {
	if cfg.Phase == "" {
		return PhaseInference
	}
	return cfg.Phase
}

func withDefaults(cfg Config) Config {
	if cfg.BlockHeight == 0 {
		cfg.BlockHeight = 150
	}
	if cfg.EpochIndex == 0 {
		cfg.EpochIndex = 1
	}
	if cfg.PoCStartBlockHeight == 0 {
		cfg.PoCStartBlockHeight = cfg.BlockHeight - 50
	}
	return cfg
}

// Mount registers GET /v1/epochs/latest and GET /v1/epochs/current/participants,
// plus GET/POST /testenv/phase unless Live is set: a Live caller owns that route.
func Mount(g *echo.Group, mounted Config) {
	if g == nil {
		return
	}
	cfg := withDefaults(mounted)
	schedule := mounted.Schedule
	if schedule == nil && mounted.Live == nil {
		schedule = NewSchedule(nil)
	}
	current := func(ctx context.Context) (Config, error) {
		switch {
		case mounted.Live != nil:
			return withDefaults(mounted.Live()), nil
		case mounted.Source != nil:
			st, err := mounted.Source(ctx)
			if err != nil {
				return Config{}, err
			}
			return Config{BlockHeight: st.BlockHeight, EpochIndex: st.EpochIndex, PoCStartBlockHeight: st.PoCStartBlockHeight, Participants: st.Participants}, nil
		default:
			return cfg, nil
		}
	}
	g.GET("/v1/epochs/latest", func(c echo.Context) error {
		cfg, err := current(c.Request().Context())
		if err != nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable, err.Error())
		}
		step := Step{Phase: phaseOf(cfg)}
		if cfg.ConfirmationPoCActive {
			step.ConfirmationPoCPhase = ConfirmationPoCGeneration
		}
		if schedule != nil {
			step = schedule.Current()
		}
		body := map[string]any{
			"block_height": fmt.Sprintf("%d", cfg.BlockHeight),
			"phase":        step.Phase,
			"latest_epoch": map[string]any{
				"index":                  fmt.Sprintf("%d", cfg.EpochIndex),
				"poc_start_block_height": fmt.Sprintf("%d", cfg.PoCStartBlockHeight),
			},
			"epoch_stages": map[string]any{
				"epoch_index":        fmt.Sprintf("%d", cfg.EpochIndex),
				"set_new_validators": fmt.Sprintf("%d", cfg.BlockHeight+30),
				"next_poc_start":     fmt.Sprintf("%d", cfg.BlockHeight+50),
			},
			"next_epoch_stages": map[string]any{
				"epoch_index":        fmt.Sprintf("%d", cfg.EpochIndex+1),
				"set_new_validators": fmt.Sprintf("%d", cfg.BlockHeight+450),
			},
			"is_confirmation_poc_active": step.ConfirmationPoCActive(),
		}
		if schedule != nil && step.ConfirmationPoCActive() {
			body["active_confirmation_poc_event"] = map[string]any{
				"phase":          step.ConfirmationPoCPhase,
				"trigger_height": fmt.Sprintf("%d", cfg.BlockHeight),
			}
		}
		return c.JSON(http.StatusOK, body)
	})
	g.GET("/v1/epochs/current/participants", func(c echo.Context) error {
		cfg, err := current(c.Request().Context())
		if err != nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable, err.Error())
		}
		participants := make([]any, 0, len(cfg.Participants))
		for _, participant := range cfg.Participants {
			if mounted.Source != nil && mounted.Live == nil {
				participants = append(participants, participant.renderSourced())
			} else {
				participants = append(participants, participant.render())
			}
		}
		return c.JSON(http.StatusOK, map[string]any{
			"active_participants": map[string]any{
				"participants": participants,
			},
		})
	})
	if schedule == nil {
		return
	}
	g.GET("/testenv/phase", func(c echo.Context) error {
		return c.JSON(http.StatusOK, schedule.Snapshot())
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
		schedule.Set(steps)
		return c.JSON(http.StatusOK, schedule.Snapshot())
	})
}
