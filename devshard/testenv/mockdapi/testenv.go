package mockdapi

import (
	"context"
	"net/http"

	"devshard/testenv/mockchain/adminface"

	"github.com/labstack/echo/v4"
)

func mountTestenvProxy(g *echo.Group, admin *adminface.Client, refresh func(context.Context) error) {
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
}
