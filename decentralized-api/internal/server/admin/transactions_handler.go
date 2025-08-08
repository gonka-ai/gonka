package admin

import (
	"decentralized-api/logging"
	"fmt"
	sdk "github.com/cosmos/cosmos-sdk/types"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
	"io"
	"net/http"
)

func (s *Server) sendTransaction(ctx echo.Context) error {
	logging.Info("Received send transaction request", types.Messages)
	body, err := io.ReadAll(ctx.Request().Body)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("failed to read request body: %v", err))
	}

	var tx txtypes.Tx
	if err := s.cdc.UnmarshalJSON(body, &tx); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("failed to unmarshal tx JSON: %v", err))
	}

	logging.Info("Unmarshalled tx", types.Messages, "tx", tx)

	if len(tx.Body.Messages) == 0 {
		return ErrNoMessagesFoundInTx
	}

	var msg sdk.Msg
	msgAny := tx.Body.Messages[0]
	if err := s.cdc.UnpackAny(msgAny, &msg); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("failed to unpack message: %v", err))
	}

	logging.Info("Unpacked message", types.Messages, "Message", msg)

	txResp, err := s.recorder.SendTransactionAsyncNoRetry(msg.(sdk.Msg))
	if err != nil {
		return err
	}

	logging.Info("TxResp", types.Messages, "txResp", *txResp)
	return ctx.JSON(http.StatusOK, txResp)
}
