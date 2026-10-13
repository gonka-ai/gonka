// Package server mounts the shared block-header HTTP API.
//
// The same Mount() is called from production dapi and the testenv
// standalone binary so consumers see an identical wire protocol.
// Live tip motion uses Comet NewBlock, not these routes.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"common/chainoracle/blocks"

	"github.com/labstack/echo/v4"
)

// Mount registers the blockoracle endpoints on g:
//
//	GET /block/:height
//	GET /block/:height/prove?path=
//	GET /healthz
func Mount(g *echo.Group, oracle blocks.BlockOracle) {
	if g == nil {
		panic("blockoracle/server: nil echo group")
	}
	if oracle == nil {
		panic("blockoracle/server: nil oracle")
	}
	g.GET("/healthz", handleHealthz)
	g.GET("/block/:height", handleAt(oracle))
	g.GET("/block/:height/prove", handleProve(oracle))
}

func handleHealthz(c echo.Context) error {
	return c.String(http.StatusOK, "ok")
}

func handleAt(oracle blocks.BlockOracle) echo.HandlerFunc {
	return func(c echo.Context) error {
		height, err := parseHeight(c.Param("height"))
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		h, err := oracle.At(c.Request().Context(), height)
		if err != nil {
			if errors.Is(err, blocks.ErrHeaderNotFound) {
				return echo.NewHTTPError(http.StatusNotFound, err.Error())
			}
			return echo.NewHTTPError(http.StatusBadGateway, err.Error())
		}
		return writeJSON(c, h)
	}
}

func handleProve(oracle blocks.BlockOracle) echo.HandlerFunc {
	return func(c echo.Context) error {
		height, err := parseHeight(c.Param("height"))
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		path := c.QueryParam("path")
		if path == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "path query param is required")
		}
		p, err := oracle.Prove(c.Request().Context(), path, height)
		if err != nil {
			if errors.Is(err, blocks.ErrProveNotImplemented) {
				return echo.NewHTTPError(http.StatusNotImplemented, err.Error())
			}
			if errors.Is(err, blocks.ErrHeaderNotFound) {
				return echo.NewHTTPError(http.StatusNotFound, err.Error())
			}
			return echo.NewHTTPError(http.StatusBadGateway, err.Error())
		}
		return writeJSON(c, p)
	}
}

func parseHeight(raw string) (int64, error) {
	if raw == "" {
		return 0, errors.New("missing height")
	}
	h, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid height %q: %w", raw, err)
	}
	if h < 0 {
		return 0, fmt.Errorf("negative height %d", h)
	}
	return h, nil
}

func writeJSON(c echo.Context, v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("blockoracle/server: marshal: %w", err)
	}
	return c.Blob(http.StatusOK, echo.MIMEApplicationJSON, payload)
}
