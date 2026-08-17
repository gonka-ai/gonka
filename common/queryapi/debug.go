package queryapi

import (
	"fmt"
	"net/http"

	"cosmossdk.io/errors"
	comettypes "github.com/cometbft/cometbft/types"
	"github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"

	"common/logging"
	"common/utils"
)

// Ported from decentralized-api/internal/server/public/debug_handlers.go:14
func (h *Handlers) DebugPubKeyToAddr(ctx echo.Context, pubkey string) error {
	addr, err := utils.PubKeyToAddress(pubkey)
	if err != nil {
		logging.Error("Failed to convert pubkey to address", types.Participants, "error", err)
		return echo.NewHTTPError(http.StatusBadRequest, errors.Wrap(err, "invalid pubkey"))
	}
	return ctx.String(http.StatusOK, addr)
}

// Ported from decentralized-api/internal/server/public/debug_handlers.go:24
// Change: original called a Tendermint RPC helper (VerifyBlockSignatures); this fetches
// the block and validator set explicitly via CometServiceClient gRPC calls.
func (h *Handlers) DebugVerifyBlockSignatures(ctx echo.Context, height int64) error {
	blockResp, err := h.chain.CometServiceClient().GetBlockByHeight(
		ctx.Request().Context(),
		&cmtservice.GetBlockByHeightRequest{Height: height},
	)
	if err != nil {
		return grpcErrorToHTTP(err)
	}

	commit, err := comettypes.CommitFromProto(blockResp.SdkBlock.LastCommit)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, fmt.Sprintf("failed to decode commit: %v", err))
	}

	validators, err := h.validatorsAtHeight(ctx, height)
	if err != nil {
		return grpcErrorToHTTP(err)
	}

	logging.Debug("Verifying block signatures", types.System, "height", height)
	if err := utils.VerifyCommit(blockResp.SdkBlock.Header.ChainID, commit, blockResp.SdkBlock.Header.Height, validators); err != nil {
		logging.Error("Failed to verify block signatures", types.Participants, "height", height, "error", err)
		return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("block signature verification failed: %v", err))
	}
	return ctx.String(http.StatusOK, "Block signatures verified")
}
