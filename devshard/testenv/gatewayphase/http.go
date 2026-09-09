// Package gatewayphase serves minimal public-API stubs for devshardctl's
// ChainPhaseGate in the testenv (epochs/latest + current participants).
package gatewayphase

import (
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
}

// Mount registers GET /v1/epochs/latest and GET /v1/epochs/current/participants.
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
	g.GET("/v1/epochs/latest", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]any{
			"block_height": fmt.Sprintf("%d", cfg.BlockHeight),
			"phase":        "Inference",
			"latest_epoch": map[string]any{
				"index":                   fmt.Sprintf("%d", cfg.EpochIndex),
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
			"is_confirmation_poc_active": false,
		})
	})
	g.GET("/v1/epochs/current/participants", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]any{
			"active_participants": map[string]any{
				"participants": []any{},
			},
		})
	})
}
