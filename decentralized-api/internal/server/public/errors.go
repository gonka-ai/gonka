package public

import (
	"github.com/labstack/echo/v4"
	"net/http"
)

var (
	ErrRequestAuth                  = echo.NewHTTPError(http.StatusUnauthorized, "Authorization is required")
	ErrInferenceParticipantNotFound = echo.NewHTTPError(http.StatusNotFound, "Inference participant not found")
	ErrInsufficientBalance          = echo.NewHTTPError(http.StatusPaymentRequired, "Insufficient balance")

	ErrIdRequired        = echo.NewHTTPError(http.StatusBadRequest, "Id is required")
	ErrInferenceNotFound = echo.NewHTTPError(http.StatusNotFound, "Inference not found")
)
