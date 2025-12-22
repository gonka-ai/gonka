package admin

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

func (s *Server) getUpgradeStatus(c echo.Context) error {
	plan := s.configManager.GetUpgradePlan()
	if plan.NodeVersion == "" {
		return c.JSON(http.StatusOK, map[string]string{"message": "No upgrade plan active"})
	}

	reports := s.nodeBroker.CheckVersionHealth(plan.NodeVersion)
	return c.JSON(http.StatusOK, reports)
}

type versionStatusRequest struct {
	Version string `json:"version"`
}

func (s *Server) postVersionStatus(c echo.Context) error {
	var req versionStatusRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid request body")
	}

	if req.Version == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "Version field is required")
	}

	reports := s.nodeBroker.CheckVersionHealth(req.Version)
	return c.JSON(http.StatusOK, reports)
}
