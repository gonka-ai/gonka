package public

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
)

const MaxRequestBodyLimit = "10485760"

func NewNoRedirectClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func (s *Server) getAllowedPubKeys(ctx echo.Context, granterAddress string) ([]string, error) {
	return s.authzCache.GetPubKeys(ctx.Request().Context(), granterAddress, "/inference.inference.MsgClaimRewards")
}
