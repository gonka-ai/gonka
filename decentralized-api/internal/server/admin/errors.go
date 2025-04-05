package admin

import (
	"github.com/labstack/echo/v4"
	"net/http"
)

var ErrNoMessagesFoundInTx = echo.NewHTTPError(http.StatusBadRequest, "no messages found in tx")
