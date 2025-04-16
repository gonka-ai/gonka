package public

import (
	"cosmossdk.io/errors"
	cosmos_client "decentralized-api/cosmosclient"
	"decentralized-api/logging"
	"decentralized-api/merkleproof"
	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
	"net/http"
	"strconv"
)

func (s *Server) debugPubKeyToAddr(ctx echo.Context) error {
	pubkey := ctx.Param("pubkey")
	addr, err := cosmos_client.PubKeyToAddress(pubkey)
	if err != nil {
		logging.Error("Failed to convert pubkey to address", types.Participants, "error", err)
		return echo.NewHTTPError(http.StatusBadRequest, errors.Wrap(err, "invalid pubkey"))
	}
	return ctx.String(http.StatusOK, addr)
}

func (s *Server) debugVerify(ctx echo.Context) error {
	heightStr := ctx.Param("height")
	height, err := strconv.ParseInt(heightStr, 10, 64)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, errors.Wrap(err, "invalid height"))
	}

	logging.Debug("Verifying block signatures", types.System, "height", height)
	if err := merkleproof.VerifyBlockSignatures(s.configManager.GetChainNodeConfig().Url, height); err != nil {
		logging.Error("Failed to verify block signatures", types.Participants, "error", err)
		return err
	}
	return ctx.String(http.StatusOK, "Block signatures verified")
}
