package devshardobs

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

// Mount registers versionless /devshard observability routes on e.
// Paths match the Phase 0 locked set (intact /devshard/… prefix).
func Mount(e *echo.Echo, h http.Handler) {
	if e == nil || h == nil {
		return
	}
	wrap := echo.WrapHandler(h)
	e.GET("/devshard/sessions/:id/diffs", wrap)
	e.GET("/devshard/sessions/:id/mempool", wrap)
	e.GET("/devshard/sessions/:id/signatures", wrap)
	e.GET("/devshard/stats/shards", wrap)
	e.GET("/devshard/stats/shards/:escrow_id", wrap)
	e.GET("/devshard/metrics", wrap)
}
