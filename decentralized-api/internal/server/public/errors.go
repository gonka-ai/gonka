package public

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

var (
	ErrRequestAuth                  = echo.NewHTTPError(http.StatusUnauthorized, "Authorization is required")
	ErrInferenceParticipantNotFound = echo.NewHTTPError(http.StatusNotFound, "Inference participant not found")
	ErrInsufficientBalance          = echo.NewHTTPError(http.StatusPaymentRequired, "Insufficient balance")

	ErrIdRequired           = echo.NewHTTPError(http.StatusBadRequest, "Id is required")
	ErrAddressRequired      = echo.NewHTTPError(http.StatusBadRequest, "Address is required")
	ErrInvalidEpochId       = echo.NewHTTPError(http.StatusBadRequest, "Invalid epoch id")
	ErrInvalidTrainingJobId = echo.NewHTTPError(http.StatusBadRequest, "Invalid training job id")
	ErrEpochIsNotReached    = echo.NewHTTPError(http.StatusBadRequest, "Epoch is not reached")
	ErrInferenceNotFound    = echo.NewHTTPError(http.StatusNotFound, "Inference not found")
	ErrNoModelSpecified     = echo.NewHTTPError(http.StatusBadRequest, "No model specified")
)
