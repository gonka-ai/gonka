// Package server mounts the unified chainoracle HTTP surface: authenticated
// block headers (SSE) and versiond oracle routes (/versions).
package server

import (
	"net/http"

	"devshard/chainoracle/blocks"
	blockserver "devshard/chainoracle/blocks/server"

	"github.com/labstack/echo/v4"
)

// Version describes one approved devshard binary for versiond.
type Version struct {
	Name   string `json:"name"`
	Binary string `json:"binary"`
	SHA256 string `json:"sha256,omitempty"`
	Port   int    `json:"port,omitempty"`
}

// VersionConfig is the JSON body for GET /versions (versiond oracle contract).
type VersionConfig struct {
	Versions []Version `json:"versions"`
}

// Config wires the HTTP mux.
type Config struct {
	Blocks blocks.BlockOracle
	// Versions is served at GET /versions for VERSIOND_ORACLE_URL polling.
	Versions []Version
}

// Mount registers chainoracle HTTP routes on g.
func Mount(g *echo.Group, cfg Config) {
	if g == nil {
		panic("chainoracle/server: nil echo group")
	}
	if cfg.Blocks == nil {
		panic("chainoracle/server: Blocks oracle is required")
	}
	blockserver.Mount(g, cfg.Blocks)
	g.GET("/versions", handleVersions(cfg.Versions))
}

func handleVersions(versions []Version) echo.HandlerFunc {
	return func(c echo.Context) error {
		return c.JSON(http.StatusOK, VersionConfig{Versions: versions})
	}
}
