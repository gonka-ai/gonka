// Package server mounts the blockoracle HTTP + SSE API on a host router.
//
// The same Mount() is called in:
//
//   - the standalone height-sync binary (testenv)
//   - real decentralized-api (production)
//
// so devshardd consumers see an identical wire protocol in both
// environments.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"devshard/chainoracle/blocks"

	"github.com/labstack/echo/v4"
)

// Mount registers the blockoracle endpoints on g:
//
//	GET /block/latest
//	GET /block/:height
//	GET /block/:height/prove?path=
//	GET /block/stream?from=
//	GET /healthz
//
// The handlers JSON-encode blocks.Header byte-for-byte stable across
// producers. The subscriber endpoint is a Server-Sent Events stream.
//
// Content-type negotiation is omitted deliberately: the client treats
// this as a private wire format between blockoracle producer and
// blockoracle/client.
func Mount(g *echo.Group, oracle blocks.BlockOracle) {
	if g == nil {
		panic("blockoracle/server: nil echo group")
	}
	if oracle == nil {
		panic("blockoracle/server: nil oracle")
	}
	g.GET("/healthz", handleHealthz)
	g.GET("/block/latest", handleLatest(oracle))
	g.GET("/block/:height", handleAt(oracle))
	g.GET("/block/:height/prove", handleProve(oracle))
	g.GET("/block/stream", handleStream(oracle))
}

func handleHealthz(c echo.Context) error {
	return c.String(http.StatusOK, "ok")
}

func handleLatest(oracle blocks.BlockOracle) echo.HandlerFunc {
	return func(c echo.Context) error {
		h, err := oracle.Latest(c.Request().Context())
		if err != nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable, err.Error())
		}
		return writeJSON(c, h)
	}
}

func handleAt(oracle blocks.BlockOracle) echo.HandlerFunc {
	return func(c echo.Context) error {
		height, err := parseHeight(c.Param("height"))
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		h, err := oracle.At(c.Request().Context(), height)
		if err != nil {
			return echo.NewHTTPError(http.StatusNotFound, err.Error())
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
			return echo.NewHTTPError(http.StatusNotFound, err.Error())
		}
		return writeJSON(c, p)
	}
}

func handleStream(oracle blocks.BlockOracle) echo.HandlerFunc {
	return func(c echo.Context) error {
		from := int64(0)
		if raw := c.QueryParam("from"); raw != "" {
			parsed, err := parseHeight(raw)
			if err != nil {
				return echo.NewHTTPError(http.StatusBadRequest, err.Error())
			}
			from = parsed
		}
		ctx := c.Request().Context()
		ch, err := oracle.Subscribe(ctx, from)
		if err != nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable, err.Error())
		}

		resp := c.Response()
		resp.Header().Set(echo.HeaderContentType, "text/event-stream")
		resp.Header().Set("Cache-Control", "no-cache")
		resp.Header().Set("Connection", "keep-alive")
		resp.Header().Set("X-Accel-Buffering", "no")
		resp.WriteHeader(http.StatusOK)

		flusher, ok := resp.Writer.(http.Flusher)
		if !ok {
			return errors.New("blockoracle/server: streaming unsupported")
		}

		ping := time.NewTicker(15 * time.Second)
		defer ping.Stop()

		for {
			select {
			case <-ctx.Done():
				return nil
			case h, alive := <-ch:
				if !alive {
					return nil
				}
				payload, err := json.Marshal(h)
				if err != nil {
					return fmt.Errorf("blockoracle/server: marshal: %w", err)
				}
				if _, err := fmt.Fprintf(resp.Writer, "event: header\nid: %d\ndata: %s\n\n", h.Height, payload); err != nil {
					return err
				}
				flusher.Flush()
			case <-ping.C:
				if _, err := fmt.Fprint(resp.Writer, ": ping\n\n"); err != nil {
					return err
				}
				flusher.Flush()
			}
		}
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
	// json.Marshal is used (instead of c.JSON) so the payload is
	// byte-identical to what blockoracle/client expects when it
	// re-verifies over the wire. No HTML escaping, no indentation.
	payload, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("blockoracle/server: marshal: %w", err)
	}
	return c.Blob(http.StatusOK, echo.MIMEApplicationJSON, payload)
}
