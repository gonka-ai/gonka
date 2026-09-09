package admin

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

func blsRequestDeprecated(c echo.Context) error {
	c.Response().Header().Set("Deprecation", "true")
	return c.JSON(http.StatusGone, map[string]string{
		"error":   "deprecated",
		"message": "admin bls request is deprecated",
	})
}
