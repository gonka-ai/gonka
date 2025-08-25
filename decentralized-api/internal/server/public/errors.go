package public

import (
	"github.com/labstack/echo/v4"
	"net/http"
)

var (
	ErrRequestAuth                  = echo.NewHTTPError(http.StatusUnauthorized, "Authorization is required")
	ErrInferenceParticipantNotFound = echo.NewHTTPError(http.StatusNotFound, "Inference participant not found")
	ErrInsufficientBalance          = echo.NewHTTPError(http.StatusPaymentRequired, "Insufficient balance")

	ErrIdRequired           = echo.NewHTTPError(http.StatusBadRequest, "Id is required")
	ErrAddressRequired      = echo.NewHTTPError(http.StatusBadRequest, "Address is required")
	ErrInvalidEpochId       = echo.NewHTTPError(http.StatusBadRequest, "Invalid epoch id")
	ErrInvalidBlockHeight   = echo.NewHTTPError(http.StatusBadRequest, "Invalid block height")
	ErrInvalidTrainingJobId = echo.NewHTTPError(http.StatusBadRequest, "Invalid training job id")
	ErrInferenceNotFound    = echo.NewHTTPError(http.StatusNotFound, "Inference not found")
)
