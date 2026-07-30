package devshardobs

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

// Mount registers versionless /v1/devshard observability routes on e.
// Paths match the join-proxy versionless set (intact /v1/devshard/… prefix).
func Mount(e *echo.Echo, h http.Handler) {
	if e == nil || h == nil {
		return
	}
	wrap := echo.WrapHandler(h)
	e.GET("/v1/devshard/sessions/:id/diffs", wrap)
	e.GET("/v1/devshard/sessions/:id/mempool", wrap)
	e.GET("/v1/devshard/sessions/:id/signatures", wrap)
	e.GET("/v1/devshard/stats/shards", wrap)
	e.GET("/v1/devshard/stats/shards/:escrow_id", wrap)
	e.GET("/v1/devshard/metrics", wrap)
}
