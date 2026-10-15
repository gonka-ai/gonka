package httpx

import (
	"errors"
	"net/http"

	"trainshard/internal/domain/shared"
)

func StatusOf(err error) (int, string) {
	switch {
	case errors.Is(err, shared.ErrValidation):
		return http.StatusUnprocessableEntity, codeOr(err, "VALIDATION_ERROR")
	case errors.Is(err, shared.ErrUnauthorized):
		return http.StatusUnauthorized, codeOr(err, "UNAUTHORIZED")
	case errors.Is(err, shared.ErrForbidden):
		return http.StatusForbidden, codeOr(err, "FORBIDDEN")
	case errors.Is(err, shared.ErrNotFound):
		return http.StatusNotFound, codeOr(err, "NOT_FOUND")
	case errors.Is(err, shared.ErrConflict):
		return http.StatusConflict, codeOr(err, "CONFLICT")
	case errors.Is(err, shared.ErrUnavailable):
		return http.StatusServiceUnavailable, codeOr(err, "UNAVAILABLE")
	default:
		return http.StatusInternalServerError, shared.CodeInternal
	}
}

func codeOr(err error, fallback string) string {
	if code := shared.CodeOf(err); code != shared.CodeInternal {
		return code
	}
	return fallback
}
