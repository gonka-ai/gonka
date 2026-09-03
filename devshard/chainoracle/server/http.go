// Package server mounts the unified chainoracle HTTP surface: block lookup
// (GET /block/:height) and versiond oracle routes (/versions).
package server

import (
	"context"
	"net/http"

	"common/chainoracle/blocks"
	blockserver "common/chainoracle/blocks/server"
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

// VersionProvider returns the currently approved versions.
type VersionProvider interface {
	Versions(context.Context) ([]Version, error)
}

// VersionProviderFunc adapts a function to VersionProvider.
type VersionProviderFunc func(context.Context) ([]Version, error)

// Versions calls f(ctx).
func (f VersionProviderFunc) Versions(ctx context.Context) ([]Version, error) {
	return f(ctx)
}

// Config wires the HTTP mux.
type Config struct {
	Blocks blocks.BlockOracle
	// OmitBlocks skips /block/* (old dapi stand-in). /healthz and /versions stay.
	OmitBlocks bool
	// Versions is served at GET /versions for VERSIOND_ORACLE_URL polling.
	Versions []Version
	// VersionProvider, when set, overrides Versions for dynamic tests and dapi
	// implementations.
	VersionProvider VersionProvider
}

// Mount registers chainoracle HTTP routes on g.
func Mount(g *echo.Group, cfg Config) {
	if g == nil {
		panic("chainoracle/server: nil echo group")
	}
	if cfg.OmitBlocks {
		g.GET("/healthz", func(c echo.Context) error {
			return c.String(http.StatusOK, "ok")
		})
	} else {
		if cfg.Blocks == nil {
			panic("chainoracle/server: Blocks oracle is required")
		}
		blockserver.Mount(g, cfg.Blocks)
	}
	if cfg.VersionProvider != nil {
		g.GET("/versions", handleVersionProvider(cfg.VersionProvider))
	} else {
		g.GET("/versions", handleVersions(cfg.Versions))
	}
}

func handleVersions(versions []Version) echo.HandlerFunc {
	return func(c echo.Context) error {
		return c.JSON(http.StatusOK, VersionConfig{Versions: versions})
	}
}

func handleVersionProvider(provider VersionProvider) echo.HandlerFunc {
	return func(c echo.Context) error {
		versions, err := provider.Versions(c.Request().Context())
		if err != nil {
			return echo.NewHTTPError(http.StatusBadGateway, err.Error())
		}
		return c.JSON(http.StatusOK, VersionConfig{Versions: versions})
	}
}
