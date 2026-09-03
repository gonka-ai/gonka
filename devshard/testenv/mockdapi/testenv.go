package mockdapi

import (
	"context"
	"net/http"

	"common/nodemanager/gen"
	"github.com/labstack/echo/v4"

	cosrv "devshard/chainoracle/server"
	"devshard/testenv/mockchain/adminface"
)

// EscrowCreatedRequest triggers a mock host-events escrow-created event so the
// devshardd GetHostEvents long-poll warm can be exercised in citest.
type EscrowCreatedRequest struct {
	EscrowID   uint64 `json:"escrow_id"`
	EpochIndex uint64 `json:"epoch_index,omitempty"`
	ModelID    string `json:"model_id,omitempty"`
	Creator    string `json:"creator,omitempty"`
	Amount     string `json:"amount,omitempty"`
}

func mountTestenvProxy(g *echo.Group, admin *adminface.Client, refresh func(context.Context) error, versions *versionStore, hostEvents *hostEventRing) {
	g.POST("/testenv/params", func(c echo.Context) error {
		if admin == nil || admin.BaseURL() == "" {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "mock-chain testenv admin not configured")
		}
		var req adminface.ParamsRequest
		if err := c.Bind(&req); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		if err := admin.PatchParams(c.Request().Context(), req); err != nil {
			return echo.NewHTTPError(http.StatusBadGateway, err.Error())
		}
		if refresh != nil {
			if err := refresh(c.Request().Context()); err != nil {
				return echo.NewHTTPError(http.StatusBadGateway, err.Error())
			}
		}
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})
	g.POST("/testenv/epoch", func(c echo.Context) error {
		if admin == nil || admin.BaseURL() == "" {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "mock-chain testenv admin not configured")
		}
		var req adminface.EpochRequest
		if err := c.Bind(&req); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		if err := admin.PatchEpoch(c.Request().Context(), req); err != nil {
			return echo.NewHTTPError(http.StatusBadGateway, err.Error())
		}
		if refresh != nil {
			if err := refresh(c.Request().Context()); err != nil {
				return echo.NewHTTPError(http.StatusBadGateway, err.Error())
			}
		}
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})
	g.POST("/testenv/escrow", func(c echo.Context) error {
		if admin == nil || admin.BaseURL() == "" {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "mock-chain testenv admin not configured")
		}
		var req adminface.EscrowRequest
		if err := c.Bind(&req); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		if err := admin.PatchEscrow(c.Request().Context(), req); err != nil {
			return echo.NewHTTPError(http.StatusBadGateway, err.Error())
		}
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})
	g.POST("/testenv/grantees", func(c echo.Context) error {
		if admin == nil || admin.BaseURL() == "" {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "mock-chain testenv admin not configured")
		}
		var req adminface.GranteesRequest
		if err := c.Bind(&req); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		if err := admin.PatchGrantees(c.Request().Context(), req); err != nil {
			return echo.NewHTTPError(http.StatusBadGateway, err.Error())
		}
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})
	g.GET("/testenv/versions", func(c echo.Context) error {
		if versions == nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "mock-dapi versions not configured")
		}
		current, err := versions.Versions(c.Request().Context())
		if err != nil {
			return echo.NewHTTPError(http.StatusBadGateway, err.Error())
		}
		return c.JSON(http.StatusOK, cosrv.VersionConfig{Versions: current})
	})
	g.POST("/testenv/versions", func(c echo.Context) error {
		if versions == nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "mock-dapi versions not configured")
		}
		var req cosrv.VersionConfig
		if err := c.Bind(&req); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		versions.Set(req.Versions)
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})
	g.POST("/testenv/host-events/escrow-created", func(c echo.Context) error {
		if hostEvents == nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "mock-dapi host events not configured")
		}
		var req EscrowCreatedRequest
		if err := c.Bind(&req); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		if req.EscrowID == 0 {
			return echo.NewHTTPError(http.StatusBadRequest, "escrow_id is required")
		}
		seq := hostEvents.AppendEscrowCreated(&gen.EscrowPayload{
			EscrowId:   req.EscrowID,
			EpochIndex: req.EpochIndex,
			ModelId:    req.ModelID,
			Creator:    req.Creator,
			Amount:     req.Amount,
		})
		return c.JSON(http.StatusOK, map[string]any{"status": "ok", "seq": seq})
	})
	g.POST("/testenv/escrow-query-fault", func(c echo.Context) error {
		if admin == nil || admin.BaseURL() == "" {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "mock-chain testenv admin not configured")
		}
		var req adminface.EscrowQueryFaultRequest
		if err := c.Bind(&req); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		if err := admin.PatchEscrowQueryFault(c.Request().Context(), req); err != nil {
			return echo.NewHTTPError(http.StatusBadGateway, err.Error())
		}
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})
}
