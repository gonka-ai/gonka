package public

import (
	"github.com/labstack/echo/v4"
	"net/http"
	"net/http/httputil"
)

// getExplorerUI is proxy to get UI of ping-pub explorer
// if you change url path, change also base path in vite.config.ts on explorer side
func (s *Server) getExplorerUI(c echo.Context) error {
	if s.explorerTargetUrl == nil || s.explorerTargetUrl.Scheme == "" {
		return echo.NewHTTPError(http.StatusInternalServerError, "no explorer target url")
	}

	proxy := httputil.NewSingleHostReverseProxy(s.explorerTargetUrl)
	originalDirector := proxy.Director

	proxy.Director = func(req *http.Request) {
		originalDirector(req)

		req.URL.Path = c.Request().URL.Path
		req.URL.RawQuery = c.Request().URL.RawQuery
		req.Host = s.explorerTargetUrl.Host
	}

	proxy.ServeHTTP(c.Response(), c.Request())
	return nil
}
