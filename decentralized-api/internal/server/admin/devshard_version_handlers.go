package admin

import (
	"decentralized-api/apiconfig"
	"net/http"

	"github.com/labstack/echo/v4"
)

func (s *Server) getDevshardVersionOverrides(c echo.Context) error {
	return c.JSON(http.StatusOK, s.configManager.GetDevshardVersionOverrides())
}

func (s *Server) postDevshardVersionOverride(c echo.Context) error {
	var override apiconfig.DevshardVersionOverride
	if err := c.Bind(&override); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if err := override.Validate(); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if err := s.configManager.SetDevshardVersionOverride(c.Request().Context(), override); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, override)
}

func (s *Server) deleteDevshardVersionOverride(c echo.Context) error {
	var body struct {
		From apiconfig.DevshardBinary `json:"from"`
	}
	if err := c.Bind(&body); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if err := body.From.Validate(); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if err := s.configManager.DeleteDevshardVersionOverride(c.Request().Context(), body.From); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}
