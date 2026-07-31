package queryapi

import (
	"net/http"
	"time"

	"github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
	"github.com/cosmos/cosmos-sdk/version"
	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"

	"common/logging"
	"common/queryapi/gen"
)

// Ported from decentralized-api/internal/server/public/server.go:173
func (h *Handlers) GetStatus(ctx echo.Context) error {
	return ctx.JSON(http.StatusOK, gen.StatusResponse{Status: "ok"})
}

// GetVersions serves thin api/node version info for edge-api.
// Dapi serves a broker-enriched /v1/versions (with mlnodes) as a first-class
// route; the public proxy never steers /v1/versions to edge-api.
func (h *Handlers) GetVersions(ctx echo.Context) error {
	resp, err := h.chain.CometServiceClient().GetNodeInfo(ctx.Request().Context(), &cmtservice.GetNodeInfoRequest{})
	if err != nil {
		logging.Error("Failed to get node info from cosmos node", types.Server, "error", err)
		return ctx.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to get node info",
		})
	}
	nv := resp.ApplicationVersion
	return ctx.JSON(http.StatusOK, gen.VersionsResponse{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		ApiVersion: gen.ApiVersion{
			ApplicationName: version.AppName,
			Version:         version.Version,
			Commit:          version.Commit,
		},
		NodeVersion: gen.NodeVersion{
			ApplicationName: nv.Name,
			Version:         nv.Version,
			Commit:          nv.GitCommit,
		},
	})
}
