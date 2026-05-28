package admin

import (
	cosmos_client "decentralized-api/cosmosclient"
	"net/http"

	"github.com/labstack/echo/v4"
)

type QueryCacheStatsResponse struct {
	Enabled bool                           `json:"enabled"`
	Stats   *cosmos_client.QueryCacheStats `json:"stats,omitempty"`
}

func (s *Server) getQueryCacheStats(c echo.Context) error {
	stats := s.recorder.GetQueryCacheStats()
	return c.JSON(http.StatusOK, QueryCacheStatsResponse{
		Enabled: stats != nil,
		Stats:   stats,
	})
}

func (s *Server) resetQueryCacheStats(c echo.Context) error {
	enabled := s.recorder.ResetQueryCacheStats()
	if !enabled {
		return c.JSON(http.StatusOK, QueryCacheStatsResponse{Enabled: false})
	}

	stats := s.recorder.GetQueryCacheStats()
	return c.JSON(http.StatusOK, QueryCacheStatsResponse{
		Enabled: true,
		Stats:   stats,
	})
}
